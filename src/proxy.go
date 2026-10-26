package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ProxyService struct {
	app *App

	mu       sync.Mutex
	port     int
	running  bool
	bindErr  string
	ln       net.Listener
	tlsSrv   *http.Server
	plainSrv *http.Server
	tlsCh    chan net.Conn
	plainCh  chan net.Conn
	closed   chan struct{}

	httpPort int
	httpLn   net.Listener
	httpSrv  *http.Server
	httpErr  string

	transports map[string]*http.Transport
	proxies    map[string]*httputil.ReverseProxy
	startedAt  time.Time
	active     atomic.Int64
	totalConns atomic.Uint64
}

func NewProxyService(app *App) *ProxyService {
	return &ProxyService{app: app, transports: map[string]*http.Transport{}}
}

func (p *ProxyService) Start(cfg *Config) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()

	addr := fmt.Sprintf(":%d", cfg.Proxy.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		p.bindErr = friendlyBindError(cfg.Proxy.Port, err)
		p.port = cfg.Proxy.Port
		p.running = false
		p.app.logf("错误", "代理端口 %d 监听失败: %v", cfg.Proxy.Port, err)
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
	}

	p.port = cfg.Proxy.Port
	p.bindErr = ""
	p.ln = ln
	p.closed = make(chan struct{})
	p.tlsCh = make(chan net.Conn, 256)
	p.plainCh = make(chan net.Conn, 256)
	p.transports = map[string]*http.Transport{}
	p.proxies = map[string]*httputil.ReverseProxy{}
	p.startedAt = time.Now()

	handler := http.HandlerFunc(p.app.handleProxy)
	p.tlsSrv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	p.plainSrv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          log.New(io.Discard, "", 0),
	}

	tlsCfg := p.app.certs.TLSConfig()
	tlsCfg.GetCertificate = p.app.getCertificate
	tlsListener := &chanListener{
		ch:     p.tlsCh,
		addr:   ln.Addr(),
		closed: p.closed,
		wrap:   func(c net.Conn) net.Conn { return tls.Server(c, tlsCfg) },
	}
	plainListener := &chanListener{ch: p.plainCh, addr: ln.Addr(), closed: p.closed}

	go func() {
		if err := p.tlsSrv.Serve(tlsListener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			p.app.logf("错误", "HTTPS 服务退出: %v", err)
		}
	}()
	go func() {
		if err := p.plainSrv.Serve(plainListener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			p.app.logf("错误", "HTTP 服务退出: %v", err)
		}
	}()
	go p.acceptLoop(ln)

	p.running = true
	p.httpPort = 0
	p.httpErr = ""
	p.httpLn = nil
	p.httpSrv = nil
	if cfg.Proxy.ExtraHTTPPort > 0 {
		p.startExtraHTTPLocked(cfg)
	}

	p.app.logf("系统", "代理端口 %d 已启动（HTTP/HTTPS 单端口复用）", cfg.Proxy.Port)
	return nil
}

func (p *ProxyService) startExtraHTTPLocked(cfg *Config) {
	addr := fmt.Sprintf(":%d", cfg.Proxy.ExtraHTTPPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		p.httpErr = friendlyBindError(cfg.Proxy.ExtraHTTPPort, err)
		phttp := cfg.Proxy.ExtraHTTPPort
		p.httpPort = phttp
		p.app.logf("警告", "额外 HTTP 端口 %d 监听失败: %v", phttp, err)
		return
	}
	p.httpPort = cfg.Proxy.ExtraHTTPPort
	p.httpLn = ln
	p.httpErr = ""
	srv := &http.Server{
		Handler:           http.HandlerFunc(p.app.handleExtraHTTP),
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	p.httpSrv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			p.app.logf("错误", "额外 HTTP 端口服务退出: %v", err)
		}
	}()
	p.app.logf("系统", "额外 HTTP 跳转端口 %d 已启动", p.httpPort)
}

func (p *ProxyService) acceptLoop(ln net.Listener) {
	closed := p.closed
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-closed:
				return
			default:
				if ne, ok := err.(net.Error); ok && ne.Temporary() {
					time.Sleep(50 * time.Millisecond)
					continue
				}
				return
			}
		}
		p.totalConns.Add(1)
		p.active.Add(1)
		go func(c net.Conn) {
			defer p.active.Add(-1)
			_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
			br := bufio.NewReaderSize(c, 4096)
			first, err := br.Peek(1)
			_ = c.SetReadDeadline(time.Time{})
			if err != nil {
				_ = c.Close()
				return
			}
			bc := &bufferedConn{Conn: c, r: br}
			if first[0] == 0x16 {
				select {
				case p.tlsCh <- bc:
				case <-closed:
					_ = c.Close()
				}
				return
			}
			select {
			case p.plainCh <- bc:
			case <-closed:
				_ = c.Close()
			}
		}(c)
	}
}

func (p *ProxyService) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

func (p *ProxyService) stopLocked() {
	if p.closed != nil {
		select {
		case <-p.closed:
		default:
			close(p.closed)
		}
	}
	if p.ln != nil {
		_ = p.ln.Close()
		p.ln = nil
	}
	if p.httpLn != nil {
		_ = p.httpLn.Close()
		p.httpLn = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if p.tlsSrv != nil {
		_ = p.tlsSrv.Shutdown(ctx)
		p.tlsSrv = nil
	}
	if p.plainSrv != nil {
		_ = p.plainSrv.Shutdown(ctx)
		p.plainSrv = nil
	}
	if p.httpSrv != nil {
		_ = p.httpSrv.Shutdown(ctx)
		p.httpSrv = nil
	}
	p.tlsCh = nil
	p.plainCh = nil
	p.closed = nil
	p.running = false
	p.httpPort = 0
	p.httpErr = ""
	p.transports = map[string]*http.Transport{}
	p.proxies = map[string]*httputil.ReverseProxy{}
}

func (p *ProxyService) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *ProxyService) Status() (bool, int, string, int, string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running, p.port, p.bindErr, p.httpPort, p.httpErr, p.startedAt
}

func (p *ProxyService) Active() int64 { return p.active.Load() }

func (p *ProxyService) ResetTransports() {
	p.mu.Lock()
	p.transports = map[string]*http.Transport{}
	p.proxies = map[string]*httputil.ReverseProxy{}
	p.mu.Unlock()
}

func (p *ProxyService) proxyFor(rule *Rule) *httputil.ReverseProxy {
	target := upstreamOf(rule)
	p.mu.Lock()
	tr := p.transportLocked(rule.InsecureSkipVerify)
	key := rule.ID + "|" + rule.Upstream + "|" + strconv.FormatBool(rule.PreserveHost) + "|" + strconv.FormatBool(rule.InsecureSkipVerify)
	if rp, ok := p.proxies[key]; ok {
		p.mu.Unlock()
		return rp
	}
	p.mu.Unlock()

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			if rule.PreserveHost {
				pr.Out.Host = pr.In.Host
			}
			pr.SetXForwarded()
			clientIP, _, _ := net.SplitHostPort(pr.In.RemoteAddr)
			if clientIP == "" {
				clientIP = pr.In.RemoteAddr
			}
			pr.Out.Header.Set("X-Real-IP", clientIP)
			port := ""
			if _, pp, err := net.SplitHostPort(pr.In.Host); err == nil && pp != "" {
				port = pp
			}
			if port == "" {
				if addr, ok := pr.In.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
					if _, pp, err := net.SplitHostPort(addr.String()); err == nil {
						port = pp
					}
				}
			}
			if port == "" {
				if pr.In.TLS != nil {
					port = "443"
				} else {
					port = "80"
				}
			}
			pr.Out.Header.Set("X-Forwarded-Port", port)
			for k, v := range rule.Headers {
				pr.Out.Header.Set(k, v)
			}
		},
		Transport: tr,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.app.stats.Errors.Add(1)
			p.app.logf("上游", "规则「%s」请求 %s 失败: %v", rule.Name, r.URL.Path, err)
			if r.Context().Err() == nil {
				p.app.writeErrorPage(w, http.StatusBadGateway)
			}
		},
		FlushInterval: 100 * time.Millisecond,
	}
	rp.ErrorLog = log.New(io.Discard, "", 0)

	p.mu.Lock()
	if p.proxies == nil {
		p.proxies = map[string]*httputil.ReverseProxy{}
	}
	p.proxies[key] = rp
	p.mu.Unlock()
	return rp
}

func (p *ProxyService) transportLocked(insecure bool) *http.Transport {
	key := "secure"
	if insecure {
		key = "insecure"
	}
	if tr, ok := p.transports[key]; ok {
		return tr
	}
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	p.transports[key] = tr
	return tr
}

type chanListener struct {
	ch     chan net.Conn
	addr   net.Addr
	closed chan struct{}
	wrap   func(net.Conn) net.Conn
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		if c == nil {
			return nil, net.ErrClosed
		}
		if l.wrap != nil {
			c = l.wrap(c)
		}
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error   { return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

type statusRecorder struct {
	http.ResponseWriter
	status   int
	bytes    int64
	hijacked bool
	live     *LiveRequest
	stats    *Stats
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
		if r.live != nil {
			r.live.Status.Store(int32(code))
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
		if r.live != nil {
			r.live.Status.Store(http.StatusOK)
		}
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	if n > 0 {
		if r.live != nil {
			r.live.Down.Add(int64(n))
		}
		if r.stats != nil {
			r.stats.BytesOut.Add(uint64(n))
		}
	}
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("当前连接不支持 Hijack")
	}
	c, rw, err := hj.Hijack()
	if err == nil {
		r.hijacked = true
		r.status = http.StatusSwitchingProtocols
		if r.live != nil {
			r.live.Status.Store(http.StatusSwitchingProtocols)
			c = &countingConn{Conn: c, live: r.live, stats: r.stats}
		}
	}
	return c, rw, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func friendlyBindError(port int, err error) string {
	msg := err.Error()
	if strings.Contains(msg, "address already in use") || strings.Contains(msg, "Only one usage") {
		if port == 80 || port == 443 {
			return fmt.Sprintf("端口 %d 已被占用。飞牛默认会占用 80/443 做网页跳转，可在「系统设置 → 安全性 → 端口设置 → 高级设置」取消勾选「重定向 80 和 443 端口」，或改用 8443 等端口。", port)
		}
		return fmt.Sprintf("端口 %d 已被其他程序占用，请换一个端口。", port)
	}
	if strings.Contains(msg, "permission denied") || strings.Contains(msg, "Access is denied") {
		return fmt.Sprintf("没有权限监听端口 %d（1024 以下端口需要 root 权限）。", port)
	}
	return fmt.Sprintf("端口 %d 监听失败: %v", port, err)
}

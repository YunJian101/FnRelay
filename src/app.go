package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Stats struct {
	Requests atomic.Uint64
	TLS      atomic.Uint64
	Plain    atomic.Uint64
	Errors   atomic.Uint64
	BytesIn  atomic.Uint64
	BytesOut atomic.Uint64
}

type App struct {
	cfgMu     sync.RWMutex
	cfg       *Config
	cfgPath   string
	dataDir   string
	adminPort int
	version   string

	certs    *CertStore
	proxy    *ProxyService
	index    atomic.Pointer[ruleIndex]
	stats    Stats
	logs     *LogRing
	fileLog  *FileLogger
	live     *LiveRegistry
	sessions *SessionStore
	limiter  *LoginLimiter

	saveMu  sync.Mutex
	applyMu sync.Mutex

	startedAt    time.Time
	autoFallback string
	devMode      bool
}

type RuntimeStatus struct {
	Uptime         int64     `json:"uptime"`
	ProxyRunning   bool      `json:"proxy_running"`
	ProxyPort      int       `json:"proxy_port"`
	ProxyError     string    `json:"proxy_error"`
	HTTPPort       int       `json:"http_port"`
	HTTPError      string    `json:"http_error"`
	ActiveConns    int64     `json:"active_conns"`
	ActiveRequests int       `json:"active_requests"`
	TotalConns     uint64    `json:"total_conns"`
	Requests       uint64    `json:"requests"`
	TLSRequests    uint64    `json:"tls_requests"`
	PlainRequests  uint64    `json:"plain_requests"`
	Errors         uint64    `json:"errors"`
	BytesIn        uint64    `json:"bytes_in"`
	BytesOut       uint64    `json:"bytes_out"`
	CertCount      int       `json:"cert_count"`
	CertsLoaded    time.Time `json:"certs_loaded_at"`
	CertsError     string    `json:"certs_error"`
	RuleCount      int       `json:"rule_count"`
	AdminPort      int       `json:"admin_port"`
}

func NewApp(cfg *Config, cfgPath, dataDir string, adminPort int) *App {
	a := &App{
		cfg:       cfg,
		cfgPath:   cfgPath,
		dataDir:   dataDir,
		adminPort: adminPort,
		version:   Version,
		startedAt: time.Now(),
		logs:      NewLogRing(1000),
		sessions:  NewSessionStore(time.Duration(cfg.Admin.SessionDays) * 24 * time.Hour),
		limiter:   NewLoginLimiter(),
	}
	a.certs = NewCertStore(dataDir, cfg.Certs)
	a.fileLog = NewFileLogger(filepath.Join(dataDir, "logs"), cfg.Logs)
	a.live = NewLiveRegistry()
	a.proxy = NewProxyService(a)
	a.index.Store(buildIndex(cfg))
	if err := a.certs.Reload(true); err != nil {
		a.logf("警告", "证书加载: %v", err)
	}
	count, _, _ := a.certs.Stats()
	a.logf("系统", "加载证书 %d 张，规则 %d 条", count, len(cfg.Rules))
	return a
}

func (a *App) Config() *Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.cfg
}

func (a *App) setConfig(next *Config) {
	a.cfgMu.Lock()
	a.cfg = next
	a.cfgMu.Unlock()
	a.index.Store(buildIndex(next))
}

func (a *App) Index() *ruleIndex { return a.index.Load() }

func (a *App) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello != nil {
		host := normalizeHost(hello.ServerName)
		if host != "" {
			if rule := a.Index().Match(host); rule != nil && rule.CertHint != "" {
				if cert := a.certs.Lookup(rule.CertHint); cert != nil {
					return cert, nil
				}
			}
		}
	}
	return a.certs.GetCertificate(hello)
}

func (a *App) Certificates() ([]CertEntry, time.Time, string) { return a.certs.Entries() }

func (a *App) Status() RuntimeStatus {
	ok, port, bindErr, httpPort, httpErr, _ := a.proxy.Status()
	certCount, certsLoaded, certsErr := a.certs.Stats()
	return RuntimeStatus{
		Uptime:         int64(time.Since(a.startedAt).Seconds()),
		ProxyRunning:   ok,
		ProxyPort:      port,
		ProxyError:     bindErr,
		HTTPPort:       httpPort,
		HTTPError:      httpErr,
		ActiveConns:    a.proxy.Active(),
		ActiveRequests: a.live.Count(),
		TotalConns:     a.proxy.totalConns.Load(),
		Requests:       a.stats.Requests.Load(),
		TLSRequests:    a.stats.TLS.Load(),
		PlainRequests:  a.stats.Plain.Load(),
		Errors:         a.stats.Errors.Load(),
		BytesIn:        a.stats.BytesIn.Load(),
		BytesOut:       a.stats.BytesOut.Load(),
		CertCount:      certCount,
		CertsLoaded:    certsLoaded,
		CertsError:     certsErr,
		RuleCount:      a.index.Load().Len(),
		AdminPort:      a.adminPort,
	}
}

func (a *App) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	e := LogEntry{Time: time.Now(), Kind: logKindSystem, Level: level, Message: msg}
	a.logs.Add(e)
	if a.fileLog != nil {
		a.fileLog.Write(e)
	}
	appLog.Printf("[%s] %s", level, msg)
}

func (a *App) recordAccess(r *http.Request, host string, rule *Rule, status int, bytes int64, elapsed time.Duration, requestID string) {
	cfg := a.Config()
	if !cfg.Logs.Access {
		return
	}
	info := requestIPInfo(r, false)
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	e := LogEntry{
		Time:        time.Now(),
		Kind:        logKindAccess,
		Level:       "访问",
		RequestID:   requestID,
		Host:        host,
		Method:      r.Method,
		Path:        truncate(path, 1024),
		Query:       sanitizeQuery(r.URL.RawQuery),
		Scheme:      "http",
		HTTPVersion: r.Proto,
		Status:      status,
		Bytes:       bytes,
		BytesIn:     r.ContentLength,
		Duration:    elapsed.Milliseconds(),
		TLS:         r.TLS != nil,
		UserAgent:   truncate(r.UserAgent(), 512),
		Referer:     sanitizeReferer(r.Referer()),
	}
	if r.TLS != nil {
		e.Scheme = "https"
	}
	if r.ContentLength < 0 {
		e.BytesIn = 0
	}
	if status >= 500 {
		e.Error = "upstream/status error"
	}
	if rule != nil {
		e.Rule = rule.Name
		e.Upstream = redactURL(rule.Upstream)
		if u, err := parseUpstream(rule.Upstream); err == nil {
			e.UpstreamHost = u.Host
		}
	}
	applyIPInfo(&e, info)
	a.logs.Add(e)
	if a.fileLog != nil {
		a.fileLog.Write(e)
	}
}

func (a *App) audit(r *http.Request, action, target, result string, status int, detail map[string]any, errMsg string) {
	if !a.Config().Logs.Audit {
		return
	}
	e := LogEntry{
		Time:      time.Now(),
		Kind:      logKindAudit,
		Level:     "审核",
		RequestID: requestIDFrom(r),
		Status:    status,
		Action:    action,
		Target:    target,
		Result:    result,
		Detail:    detail,
		Error:     errMsg,
		Host:      normalizeHost(r.Host),
		Method:    r.Method,
		Path:      truncate(r.URL.EscapedPath(), 512),
	}
	applyIPInfo(&e, requestIPInfo(r, true))
	e.Actor = actorFrom(r, a.devMode)
	a.logs.Add(e)
	if a.fileLog != nil {
		a.fileLog.Write(e)
	}
}

func (a *App) Close() {
	if a.fileLog != nil {
		a.fileLog.Close()
	}
}

func (a *App) handleProxy(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	requestID := newRequestID()
	w.Header().Set("X-FnRelay-Request-Id", requestID)
	cfg := a.Config()
	host := normalizeHost(r.Host)
	rule := a.Index().Match(host)

	if r.TLS == nil && cfg.Proxy.HTTPRedirect {
		if rule != nil {
			target := a.redirectURL(cfg, r, host, false)
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			a.stats.Requests.Add(1)
			a.stats.Plain.Add(1)
			a.recordAccess(r, host, rule, http.StatusPermanentRedirect, 0, time.Since(started), requestID)
			return
		}
		a.stats.Errors.Add(1)
		a.writeErrorPage(w, http.StatusNotFound)
		a.recordAccess(r, host, nil, http.StatusNotFound, 0, time.Since(started), requestID)
		return
	}

	if rule == nil {
		a.stats.Errors.Add(1)
		a.writeErrorPage(w, http.StatusNotFound)
		a.recordAccess(r, host, nil, http.StatusNotFound, 0, time.Since(started), requestID)
		return
	}

	live := a.live.Start(r, host, rule)
	defer a.live.Finish(live.ID)
	if r.Body != nil {
		r.Body = &countingReadCloser{rc: r.Body, counter: &live.Up, stats: &a.stats}
	}
	rec := &statusRecorder{ResponseWriter: w, live: live, stats: &a.stats}
	if r.TLS != nil && cfg.Proxy.HSTS {
		rec.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	a.stats.Requests.Add(1)
	if r.TLS != nil {
		a.stats.TLS.Add(1)
	} else {
		a.stats.Plain.Add(1)
	}
	a.proxy.proxyFor(rule).ServeHTTP(rec, r)
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	a.recordAccess(r, host, rule, status, rec.bytes, time.Since(started), requestID)
}

func (a *App) handleExtraHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	w.Header().Set("X-FnRelay-Request-Id", requestID)
	cfg := a.Config()
	host := normalizeHost(r.Host)
	rule := a.Index().Match(host)
	if rule == nil {
		a.stats.Errors.Add(1)
		a.writeErrorPage(w, http.StatusNotFound)
		a.recordAccess(r, host, nil, http.StatusNotFound, 0, 0, requestID)
		return
	}
	target := a.redirectURL(cfg, r, host, true)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
	a.stats.Requests.Add(1)
	a.stats.Plain.Add(1)
	a.recordAccess(r, host, rule, http.StatusPermanentRedirect, 0, 0, requestID)
}

func (a *App) redirectURL(cfg *Config, r *http.Request, host string, fromExtraHTTP bool) string {
	if cfg == nil {
		cfg = a.Config()
	}
	port := 443
	switch cfg.Proxy.RedirectMode {
	case "custom_port":
		port = cfg.Proxy.RedirectPort
	default:
		if fromExtraHTTP {
			// 额外 HTTP 端口是拿来跳转到代理端口的，Host 上的端口不可靠。
			port = cfg.Proxy.Port
		} else if hp := requestHostPort(r); hp > 0 && hp != 80 {
			// 路由器/浏览器保留的外部端口优先；默认 80 不作为 HTTPS 目标。
			port = hp
		}
	}
	if host == "" {
		host = normalizeHost(r.Host)
	}
	hostPart := host
	if port == 443 {
		if strings.Contains(host, ":") {
			hostPart = "[" + host + "]"
		}
	} else {
		hostPart = net.JoinHostPort(host, strconv.Itoa(port))
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	return "https://" + hostPart + path
}

func requestHostPort(r *http.Request) int {
	_, p, err := net.SplitHostPort(r.Host)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return n
}

func localListenPort(r *http.Request) int {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return 0
	}
	_, p, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return n
}

func (a *App) writeErrorPage(w http.ResponseWriter, status int) {
	title := "请求无法完成"
	message := "请稍后再试。"
	switch status {
	case http.StatusNotFound:
		title = "页面不存在"
		message = "没有找到对应的站点，请检查域名是否正确，或稍后再试。"
	case http.StatusBadGateway:
		title = "服务暂时不可用"
		message = "上游服务暂时无法连接，请稍后再试。"
	case http.StatusGatewayTimeout:
		title = "服务响应超时"
		message = "上游服务响应超时，请稍后再试。"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// 错误页不暴露软件标识和请求关联 ID。
	w.Header().Del("X-FnRelay-Request-Id")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<title>%d</title>
<style>
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f4f7fb;color:#0f172a;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
.card{width:min(520px,calc(100vw - 40px));padding:38px 36px;border-radius:22px;border:1px solid #e2e8f0;background:#fff;box-shadow:0 20px 60px rgba(15,23,42,.08)}
h1{margin:0 0 10px;font-size:46px;line-height:1;color:#0f172a}
h2{margin:0 0 10px;font-size:20px;font-weight:600;color:#0f172a}
p{margin:0;color:#64748b;line-height:1.7;font-size:14px}
</style></head><body><div class="card">
<h1>%d</h1><h2>%s</h2><p>%s</p>
</div></body></html>`, status, status, title, message)
}

func (a *App) Update(fn func(*Config) error) (*Config, error) {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()

	a.cfgMu.Lock()
	old := a.cfg
	next := old.Clone()
	if err := fn(next); err != nil {
		a.cfgMu.Unlock()
		return nil, err
	}
	next.Normalize()
	if err := next.Validate(a.adminPort); err != nil {
		a.cfgMu.Unlock()
		return nil, err
	}
	if err := SaveConfig(a.cfgPath, next); err != nil {
		a.cfgMu.Unlock()
		return nil, fmt.Errorf("保存配置失败: %w", err)
	}
	a.cfg = next
	a.index.Store(buildIndex(next))
	a.cfgMu.Unlock()

	if err := a.applyRuntime(old, next); err != nil {
		return next, err
	}
	return next, nil
}

func (a *App) applyRuntime(old, next *Config) error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()

	if a.fileLog != nil {
		a.fileLog.Configure(next.Logs)
	}
	a.certs.SetConfig(next.Certs)
	if err := a.certs.Reload(true); err != nil {
		a.logf("警告", "%v", err)
	}
	a.proxy.ResetTransports()

	if old.Proxy.Port != next.Proxy.Port || old.Proxy.ExtraHTTPPort != next.Proxy.ExtraHTTPPort {
		if err := a.proxy.Start(next); err != nil {
			a.autoFallback = err.Error()
			return err
		}
		a.autoFallback = ""
	}
	a.logf("系统", "配置已更新：代理端口 %d，规则 %d 条", next.Proxy.Port, len(next.Rules))
	return nil
}

func (a *App) StartBackground() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := a.certs.Reload(false); err != nil {
				a.logf("警告", "证书自动刷新: %v", err)
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			a.sessions.Cleanup()
			a.limiter.Cleanup()
		}
	}()
}

func (a *App) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

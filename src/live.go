package main

import (
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type LiveRequest struct {
	ID           string
	Start        time.Time
	Host         string
	Method       string
	Path         string
	Query        string
	TLS          bool
	ClientIP     string
	XFF          []string
	IPSource     string
	Rule         string
	Upstream     string
	UpstreamHost string

	Status atomic.Int32
	Down   atomic.Int64
	Up     atomic.Int64

	lastAt   time.Time
	lastDown int64
	lastUp   int64
	downBPS  int64
	upBPS    int64
}

type LiveEntry struct {
	ID            string    `json:"id"`
	Start         time.Time `json:"start"`
	DurationMS    int64     `json:"duration_ms"`
	ClientIP      string    `json:"client_ip"`
	XForwardedFor []string  `json:"x_forwarded_for,omitempty"`
	IPSource      string    `json:"ip_source"`
	Host          string    `json:"host"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Query         string    `json:"query,omitempty"`
	TLS           bool      `json:"tls"`
	Rule          string    `json:"rule,omitempty"`
	Upstream      string    `json:"upstream,omitempty"`
	UpstreamHost  string    `json:"upstream_host,omitempty"`
	Status        int       `json:"status"`
	DownBytes     int64     `json:"down_bytes"`
	UpBytes       int64     `json:"up_bytes"`
	DownBPS       int64     `json:"down_bps"`
	UpBPS         int64     `json:"up_bps"`
}

type LiveSnapshot struct {
	Time     time.Time   `json:"time"`
	Active   int         `json:"active"`
	DownBPS  int64       `json:"down_bps"`
	UpBPS    int64       `json:"up_bps"`
	Requests []LiveEntry `json:"requests"`
}

type LiveRegistry struct {
	mu    sync.Mutex
	items map[string]*LiveRequest
}

func NewLiveRegistry() *LiveRegistry {
	return &LiveRegistry{items: map[string]*LiveRequest{}}
}

func (l *LiveRegistry) Start(r *http.Request, host string, rule *Rule) *LiveRequest {
	info := requestIPInfo(r, false)
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	lr := &LiveRequest{
		ID:       newRequestID(),
		Start:    time.Now(),
		Host:     host,
		Method:   r.Method,
		Path:     truncate(path, 1024),
		Query:    sanitizeQuery(r.URL.RawQuery),
		TLS:      r.TLS != nil,
		ClientIP: info.ClientIP,
		XFF:      append([]string{}, info.XForwardedFor...),
		IPSource: info.Source,
	}
	if rule != nil {
		lr.Rule = rule.Name
		lr.Upstream = redactURL(rule.Upstream)
		if u, err := parseUpstream(rule.Upstream); err == nil {
			lr.UpstreamHost = u.Host
		}
	}
	l.mu.Lock()
	l.items[lr.ID] = lr
	l.mu.Unlock()
	return lr
}

func (l *LiveRegistry) Finish(id string) {
	l.mu.Lock()
	delete(l.items, id)
	l.mu.Unlock()
}

func (l *LiveRegistry) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}

func (l *LiveRegistry) Snapshot() LiveSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	out := LiveSnapshot{Time: now, Requests: make([]LiveEntry, 0, len(l.items))}
	for _, lr := range l.items {
		down := lr.Down.Load()
		up := lr.Up.Load()
		if lr.lastAt.IsZero() {
			if elapsed := now.Sub(lr.Start).Seconds(); elapsed > 0.05 {
				lr.downBPS = int64(float64(down) / elapsed)
				lr.upBPS = int64(float64(up) / elapsed)
			}
			lr.lastAt = now
			lr.lastDown = down
			lr.lastUp = up
		} else if dt := now.Sub(lr.lastAt).Seconds(); dt > 0.05 {
			lr.downBPS = int64(float64(down-lr.lastDown) / dt)
			lr.upBPS = int64(float64(up-lr.lastUp) / dt)
			if lr.downBPS < 0 {
				lr.downBPS = 0
			}
			if lr.upBPS < 0 {
				lr.upBPS = 0
			}
			lr.lastAt = now
			lr.lastDown = down
			lr.lastUp = up
		}
		entry := LiveEntry{
			ID:            lr.ID,
			Start:         lr.Start,
			DurationMS:    now.Sub(lr.Start).Milliseconds(),
			ClientIP:      lr.ClientIP,
			XForwardedFor: append([]string{}, lr.XFF...),
			IPSource:      lr.IPSource,
			Host:          lr.Host,
			Method:        lr.Method,
			Path:          lr.Path,
			Query:         lr.Query,
			TLS:           lr.TLS,
			Rule:          lr.Rule,
			Upstream:      lr.Upstream,
			UpstreamHost:  lr.UpstreamHost,
			Status:        int(lr.Status.Load()),
			DownBytes:     down,
			UpBytes:       up,
			DownBPS:       lr.downBPS,
			UpBPS:         lr.upBPS,
		}
		out.DownBPS += entry.DownBPS
		out.UpBPS += entry.UpBPS
		out.Requests = append(out.Requests, entry)
	}
	sort.SliceStable(out.Requests, func(i, j int) bool {
		return out.Requests[i].Start.After(out.Requests[j].Start)
	})
	out.Active = len(out.Requests)
	return out
}

type countingReadCloser struct {
	rc      io.ReadCloser
	counter *atomic.Int64
	stats   *Stats
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 {
		r.counter.Add(int64(n))
		if r.stats != nil {
			r.stats.BytesIn.Add(uint64(n))
		}
	}
	return n, err
}

func (r *countingReadCloser) Close() error { return r.rc.Close() }

type countingConn struct {
	net.Conn
	live  *LiveRequest
	stats *Stats
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.live.Up.Add(int64(n))
		if c.stats != nil {
			c.stats.BytesIn.Add(uint64(n))
		}
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.live.Down.Add(int64(n))
		if c.stats != nil {
			c.stats.BytesOut.Add(uint64(n))
		}
	}
	return n, err
}

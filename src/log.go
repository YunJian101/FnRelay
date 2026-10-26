package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

var appLog = log.New(os.Stdout, "", log.LstdFlags)

type Actor struct {
	UID      string `json:"uid,omitempty"`
	Username string `json:"username,omitempty"`
	IsAdmin  bool   `json:"is_admin"`
}

type IPInfo struct {
	ClientIP      string   `json:"client_ip"`
	RemoteAddr    string   `json:"remote_addr,omitempty"`
	XRealIP       string   `json:"x_real_ip,omitempty"`
	XForwardedFor []string `json:"x_forwarded_for,omitempty"`
	Source        string   `json:"source"`
}

type LogEntry struct {
	Time      time.Time `json:"time"`
	Kind      string    `json:"kind"`
	Level     string    `json:"level"`
	RequestID string    `json:"request_id,omitempty"`

	Message string `json:"message,omitempty"`

	Host         string `json:"host,omitempty"`
	Method       string `json:"method,omitempty"`
	Path         string `json:"path,omitempty"`
	Query        string `json:"query,omitempty"`
	Scheme       string `json:"scheme,omitempty"`
	HTTPVersion  string `json:"http_version,omitempty"`
	Status       int    `json:"status,omitempty"`
	Rule         string `json:"rule,omitempty"`
	Upstream     string `json:"upstream,omitempty"`
	UpstreamHost string `json:"upstream_host,omitempty"`
	Duration     int64  `json:"duration_ms,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	BytesIn      int64  `json:"bytes_in,omitempty"`
	TLS          bool   `json:"tls,omitempty"`
	UserAgent    string `json:"user_agent,omitempty"`
	Referer      string `json:"referer,omitempty"`
	Error        string `json:"error,omitempty"`

	IP            string   `json:"ip,omitempty"`
	ClientIP      string   `json:"client_ip,omitempty"`
	RemoteAddr    string   `json:"remote_addr,omitempty"`
	XRealIP       string   `json:"x_real_ip,omitempty"`
	XForwardedFor []string `json:"x_forwarded_for,omitempty"`
	IPSource      string   `json:"ip_source,omitempty"`

	Actor  *Actor         `json:"actor,omitempty"`
	Action string         `json:"action,omitempty"`
	Target string         `json:"target,omitempty"`
	Result string         `json:"result,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

type LogRing struct {
	mu    sync.Mutex
	items []LogEntry
	cap   int
}

func NewLogRing(capacity int) *LogRing {
	if capacity <= 0 {
		capacity = 500
	}
	return &LogRing{cap: capacity}
}

func (l *LogRing) Add(e LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.items) >= l.cap {
		copy(l.items, l.items[len(l.items)-l.cap+1:])
		l.items = l.items[:l.cap-1]
	}
	l.items = append(l.items, e)
}

func (l *LogRing) List(limit int, host, kind string) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.items) {
		limit = len(l.items)
	}
	out := make([]LogEntry, 0, limit)
	for i := len(l.items) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.items[i]
		if host != "" && !strings.Contains(strings.ToLower(e.Host), strings.ToLower(host)) {
			continue
		}
		if kind != "" && e.Kind != kind {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (l *LogRing) Search(limit int, kind, text string) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.items) {
		limit = len(l.items)
	}
	text = strings.ToLower(strings.TrimSpace(text))
	out := make([]LogEntry, 0, limit)
	for i := len(l.items) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.items[i]
		if kind != "" && e.Kind != kind {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(entrySearchText(e)), text) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func entrySearchText(e LogEntry) string {
	return strings.Join([]string{
		e.Host, e.Method, e.Path, e.Query, e.Rule, e.Upstream,
		e.ClientIP, e.IP, e.RemoteAddr, e.XRealIP, strings.Join(e.XForwardedFor, " "),
		e.Message, e.Action, e.Target, e.Result, e.Error,
	}, " ")
}

type requestIDKey struct{}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "req-" + time.Now().Format("20060102-150405.000000")
	}
	return "req-" + hex.EncodeToString(b)
}

func withRequestID(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
}

func requestIDFrom(r *http.Request) string {
	if r == nil {
		return newRequestID()
	}
	if v, ok := r.Context().Value(requestIDKey{}).(string); ok && v != "" {
		return v
	}
	return newRequestID()
}

func actorFrom(r *http.Request, devMode bool) *Actor {
	if devMode {
		return &Actor{UID: "dev", Username: "dev", IsAdmin: true}
	}
	if r == nil {
		return nil
	}
	uid := strings.TrimSpace(r.Header.Get("X-Trim-Userid"))
	username := strings.TrimSpace(r.Header.Get("X-Trim-Username"))
	isAdmin := strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Trim-Isadmin")), "true") ||
		strings.TrimSpace(r.Header.Get("X-Trim-Isadmin")) == "1"
	if uid == "" && username == "" {
		return nil
	}
	return &Actor{UID: uid, Username: username, IsAdmin: isAdmin}
}

func normalizeIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return host
	}
	return strings.Trim(raw, "[]")
}

func requestIPInfo(r *http.Request, viaGateway bool) IPInfo {
	info := IPInfo{Source: "remote"}
	if r == nil {
		return info
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		info.RemoteAddr = host
	} else {
		info.RemoteAddr = strings.Trim(r.RemoteAddr, "[]")
	}
	if info.RemoteAddr == "@" || strings.EqualFold(info.RemoteAddr, "unix") {
		info.RemoteAddr = ""
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		info.XRealIP = normalizeIP(v)
	}
	for i, part := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		if i >= 10 {
			break
		}
		p := normalizeIP(part)
		if p != "" {
			info.XForwardedFor = append(info.XForwardedFor, p)
		}
	}
	if viaGateway {
		// X-Real-IP 由飞牛 nginx 直接写入，优先使用；XFF 可能包含客户端
		// 自己伪造的头部，只在没有 X-Real-IP 时取最后一跳。
		if info.XRealIP != "" {
			info.ClientIP = info.XRealIP
			info.Source = "x-real-ip"
		} else if len(info.XForwardedFor) > 0 {
			info.ClientIP = info.XForwardedFor[len(info.XForwardedFor)-1]
			info.Source = "x-forwarded-for"
		} else {
			info.ClientIP = info.RemoteAddr
		}
	} else {
		info.ClientIP = info.RemoteAddr
		info.Source = "remote"
	}
	return info
}

func applyIPInfo(e *LogEntry, info IPInfo) {
	e.ClientIP = info.ClientIP
	e.IP = info.ClientIP
	e.RemoteAddr = info.RemoteAddr
	e.XRealIP = info.XRealIP
	e.XForwardedFor = info.XForwardedFor
	e.IPSource = info.Source
}

func sanitizeQuery(raw string) string {
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return truncate(raw, 512)
	}
	for key := range values {
		if sensitiveKey(key) {
			values[key] = []string{"[REDACTED]"}
		}
	}
	return truncate(values.Encode(), 2048)
}

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return truncate(raw, 512)
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	if u.RawQuery != "" {
		u.RawQuery = sanitizeQuery(u.RawQuery)
	}
	return truncate(u.String(), 512)
}

func sanitizeReferer(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return truncate(raw, 512)
	}
	if u.RawQuery != "" {
		u.RawQuery = sanitizeQuery(u.RawQuery)
	}
	return truncate(u.String(), 512)
}

func sensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "token", "access_token", "refresh_token", "password", "passwd", "pass",
		"secret", "client_secret", "key", "api_key", "apikey", "code",
		"auth", "authorization", "signature", "sig", "credential", "credentials":
		return true
	}
	return strings.Contains(k, "token") || strings.Contains(k, "secret") ||
		strings.Contains(k, "password") || strings.Contains(k, "signature") ||
		strings.Contains(k, "credential")
}

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelfSignedFallback(t *testing.T) {
	dir := t.TempDir()
	store := NewCertStore(dir, CertConfig{})
	if store.self == nil {
		t.Fatalf("self-signed certificate was not generated: %s", store.selfErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "selfsigned.crt")); err != nil {
		t.Fatalf("selfsigned.crt missing: %v", err)
	}
	entries, _, _ := store.Entries()
	found := false
	for _, e := range entries {
		if e.Builtin {
			found = true
		}
	}
	if !found {
		t.Fatal("builtin certificate entry missing")
	}
}

func TestAuxiliaryCertSkipped(t *testing.T) {
	certPEM, err := os.ReadFile("../testdata/certs/app.test.local.crt")
	if err != nil {
		t.Skip("test certs not generated")
	}
	keyPEM, err := os.ReadFile("../testdata/certs/app.test.local.key")
	if err != nil {
		t.Skip("test key not generated")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	certDir := filepath.Join(t.TempDir(), "certs")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(certDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("app.test.local.crt", certPEM)
	write("app.test.local.key", keyPEM)
	write("fullchain.crt", certPEM)
	write("issuer_certificate.crt", certPEM)
	write("intermediate.crt", certPEM)

	store := NewCertStore(dataDir, CertConfig{ExtraDirs: []string{certDir}})
	if err := store.Reload(true); err != nil {
		t.Fatalf("reload: %v", err)
	}
	entries, _, lastErr := store.Entries()
	if lastErr != "" {
		t.Fatalf("unexpected cert load error: %s", lastErr)
	}
	real := 0
	for _, e := range entries {
		if e.Builtin {
			continue
		}
		real++
		if strings.Contains(e.Path, "issuer") || strings.Contains(e.Path, "intermediate") {
			t.Fatalf("auxiliary cert was loaded: %s", e.Path)
		}
	}
	if real != 1 {
		t.Fatalf("expected 1 leaf cert, got %d", real)
	}
}

func TestErrorPageSanitized(t *testing.T) {
	app := &App{}
	for _, status := range []int{http.StatusNotFound, http.StatusBadGateway} {
		rec := httptest.NewRecorder()
		rec.Header().Set("X-FnRelay-Request-Id", "req-test")
		app.writeErrorPage(rec, status)
		body := rec.Body.String()
		for _, bad := range []string{"FnRelay", "飞牛", "反代", "管理后台", "规则", "127.0.0.1", "example.com", "单端口"} {
			if strings.Contains(body, bad) {
				t.Fatalf("status %d page leaked %q: %s", status, bad, body)
			}
		}
		if got := rec.Header().Get("X-FnRelay-Request-Id"); got != "" {
			t.Fatalf("status %d leaked request header: %q", status, got)
		}
		if !strings.Contains(body, "页面不存在") && !strings.Contains(body, "服务暂时不可用") {
			t.Fatalf("status %d missing generic title", status)
		}
		if rec.Code != status {
			t.Fatalf("status %d wrote %d", status, rec.Code)
		}
		if !strings.Contains(body, "#f4f7fb") || !strings.Contains(body, "color-scheme") {
			t.Fatalf("status %d page is not light: %s", status, body)
		}
	}
}

func TestRuleMatchPriority(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Rules = []Rule{
		{ID: "wild", Name: "wild", Enabled: true, Domains: []string{"*.example.com"}, Upstream: "http://127.0.0.1:1"},
		{ID: "exact", Name: "exact", Enabled: true, Domains: []string{"app.example.com"}, Upstream: "http://127.0.0.1:2"},
		{ID: "deep", Name: "deep", Enabled: true, Domains: []string{"*.sub.example.com"}, Upstream: "http://127.0.0.1:3"},
	}
	ri := buildIndex(cfg)
	cases := map[string]string{
		"app.example.com":      "exact",
		"x.example.com":        "wild",
		"a.b.example.com":      "wild",
		"a.sub.example.com":    "deep",
		"deep.sub.example.com": "deep",
		"unmatched.test":       "",
	}
	for host, want := range cases {
		got := ""
		if r := ri.Match(host); r != nil {
			got = r.ID
		}
		if got != want {
			t.Errorf("Match(%q)=%q, want %q", host, got, want)
		}
	}
}

func TestGatewayAuth(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "http://nas/app/fnrelay/", nil)
	if app.isAuthed(req) {
		t.Fatal("request without gateway headers should be rejected")
	}
	req.Header.Set("X-Trim-Userid", "1000")
	if app.isAuthed(req) {
		t.Fatal("non-admin user should be rejected")
	}
	req.Header.Set("X-Trim-Isadmin", "true")
	if !app.isAuthed(req) {
		t.Fatal("gateway admin should be accepted")
	}
	app.devMode = true
	plain := httptest.NewRequest("GET", "http://127.0.0.1:8899/app/fnrelay/", nil)
	if !app.isAuthed(plain) {
		t.Fatal("dev mode should bypass gateway headers")
	}
}

func TestGatewayPrefix(t *testing.T) {
	app := &App{cfg: DefaultConfig()}
	var gotPath, gotBase string
	h := app.WithGateway(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBase = gatewayBase(r)
		w.WriteHeader(http.StatusNoContent)
	}), "/app/fnrelay", true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://nas/app/fnrelay/api/state", nil))
	if gotPath != "/api/state" || gotBase != "/app/fnrelay/" {
		t.Fatalf("prefixed request: path=%q base=%q", gotPath, gotBase)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://nas/api/state", nil))
	if gotPath != "/api/state" || gotBase != "/app/fnrelay/" {
		t.Fatalf("stripped request: path=%q base=%q", gotPath, gotBase)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://nas/app/fnrelay", nil))
	if gotPath != "/" || gotBase != "/app/fnrelay/" {
		t.Fatalf("root request: path=%q base=%q", gotPath, gotBase)
	}
}

func TestRedirectURL(t *testing.T) {
	app := &App{cfg: DefaultConfig()}
	app.cfg.Proxy.Port = 8443
	req := httptest.NewRequest("GET", "http://example.com/a?b=1", nil)
	req.Host = "example.com:8443"
	got := app.redirectURL(app.cfg, req, "example.com", false)
	if got != "https://example.com:8443/a?b=1" {
		t.Fatalf("unexpected redirect: %s", got)
	}
	app.cfg.Proxy.Port = 443
	req.Host = "example.com"
	got = app.redirectURL(app.cfg, req, "example.com", false)
	if got != "https://example.com/a?b=1" {
		t.Fatalf("unexpected redirect without port: %s", got)
	}
	app.cfg.Proxy.RedirectMode = "custom_port"
	app.cfg.Proxy.RedirectPort = 9443
	got = app.redirectURL(app.cfg, req, "example.com", false)
	if got != "https://example.com:9443/a?b=1" {
		t.Fatalf("unexpected custom port redirect: %s", got)
	}
	app.cfg.Proxy.RedirectMode = "same_port"
	app.cfg.Proxy.Port = 8443
	req.Host = "example.com:7777"
	got = app.redirectURL(app.cfg, req, "example.com", false)
	if got != "https://example.com:7777/a?b=1" {
		t.Fatalf("NAT host port should win: %s", got)
	}
	req.Host = "example.com"
	got = app.redirectURL(app.cfg, req, "example.com", false)
	if got != "https://example.com/a?b=1" {
		t.Fatalf("no host port should default to 443: %s", got)
	}
	req.Host = "example.com:7777"
	got = app.redirectURL(app.cfg, req, "example.com", true)
	if got != "https://example.com:8443/a?b=1" {
		t.Fatalf("extra HTTP should redirect to proxy port: %s", got)
	}
}

package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSanitizeQuery(t *testing.T) {
	got := sanitizeQuery("token=abc123&foo=1&password=secret&api_key=keyvalue&ok=2")
	if strings.Contains(got, "abc123") || strings.Contains(got, "secret") || strings.Contains(got, "keyvalue") {
		t.Fatalf("sensitive values leaked: %s", got)
	}
	if !strings.Contains(got, "foo=1") || !strings.Contains(got, "ok=2") {
		t.Fatalf("normal query lost: %s", got)
	}
	if strings.Count(got, "REDACTED") != 3 {
		t.Fatalf("expected 3 redactions: %s", got)
	}
}

func TestRedactURLs(t *testing.T) {
	got := redactURL("http://user:secret@example.com:8080/path?token=abc")
	if strings.Contains(got, "secret") || strings.Contains(got, "abc") {
		t.Fatalf("credentials leaked: %s", got)
	}
	if !strings.Contains(got, "example.com:8080") {
		t.Fatalf("host lost: %s", got)
	}
	ref := sanitizeReferer("https://example.com/page?token=abc&a=1")
	if strings.Contains(ref, "abc") || !strings.Contains(ref, "a=1") {
		t.Fatalf("referer not sanitized: %s", ref)
	}
}

func TestRequestIPInfo(t *testing.T) {
	r := httptest.NewRequest("GET", "http://nas/app/fnrelay/", nil)
	r.RemoteAddr = "1.2.3.4:5678"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 8.8.8.8")
	r.Header.Set("X-Real-IP", "7.7.7.7")

	proxy := requestIPInfo(r, false)
	if proxy.ClientIP != "1.2.3.4" || proxy.Source != "remote" {
		t.Fatalf("proxy client ip: %+v", proxy)
	}
	if len(proxy.XForwardedFor) != 2 {
		t.Fatalf("proxy xff: %+v", proxy)
	}

	gateway := requestIPInfo(r, true)
	if gateway.ClientIP != "7.7.7.7" || gateway.Source != "x-real-ip" {
		t.Fatalf("gateway should prefer x-real-ip: %+v", gateway)
	}

	r.Header.Del("X-Real-IP")
	gateway = requestIPInfo(r, true)
	if gateway.ClientIP != "8.8.8.8" || gateway.Source != "x-forwarded-for" {
		t.Fatalf("gateway should use last xff hop: %+v", gateway)
	}
}

func TestFileLoggerWritesDailyFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{FileEnabled: true, Access: true, Audit: true, RetentionDays: 7, MaxFileMB: 100}
	fl := NewFileLogger(dir, cfg)
	defer fl.Close()

	now := time.Now()
	fl.Write(LogEntry{Time: now, Kind: logKindAccess, Host: "a.test", Method: "GET", Path: "/", Status: 200, ClientIP: "1.2.3.4"})
	fl.Write(LogEntry{Time: now, Kind: logKindAudit, Action: "rule.create", Target: "r1", Result: "ok", Actor: &Actor{UID: "1000", Username: "admin", IsAdmin: true}, Detail: map[string]any{"name": "demo"}})
	fl.Write(LogEntry{Time: now, Kind: logKindSystem, Level: "系统", Message: "started"})
	fl.Close()

	date := now.Format("2006-01-02")
	for _, kind := range []string{logKindAccess, logKindAudit, logKindSystem} {
		path := filepath.Join(dir, kind+"-"+date+".jsonl")
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s missing: %v", kind, err)
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Fatalf("%s permission: %o", kind, st.Mode().Perm())
		}
	}

	entries, err := fl.Read(date, logKindAudit, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read audit: %v %d", err, len(entries))
	}
	if entries[0].Actor == nil || entries[0].Actor.UID != "1000" || entries[0].Action != "rule.create" {
		t.Fatalf("audit fields lost: %+v", entries[0])
	}
	if entries[0].Detail["name"] != "demo" {
		t.Fatalf("audit detail lost: %+v", entries[0].Detail)
	}

	access, err := fl.Read(date, logKindAccess, 10)
	if err != nil || len(access) != 1 || access[0].ClientIP != "1.2.3.4" {
		t.Fatalf("read access: %v %+v", err, access)
	}
}

func TestFileLoggerRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("access-" + now.AddDate(0, 0, -8).Format("2006-01-02") + ".jsonl")
	mk("audit-" + now.AddDate(0, 0, -7).Format("2006-01-02") + ".jsonl")
	mk("access-" + now.AddDate(0, 0, -3).Format("2006-01-02") + ".jsonl")
	mk("access-" + now.Format("2006-01-02") + ".jsonl")

	fl := NewFileLogger(dir, LogConfig{FileEnabled: true, Access: true, Audit: true, RetentionDays: 7, MaxFileMB: 100})
	fl.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if names["access-"+now.AddDate(0, 0, -8).Format("2006-01-02")+".jsonl"] {
		t.Fatal("8-day-old file should be removed")
	}
	if names["access-"+now.Format("2006-01-02")+".jsonl"] == false {
		t.Fatal("today file should be kept")
	}
	if names["access-"+now.AddDate(0, 0, -3).Format("2006-01-02")+".jsonl"] == false {
		t.Fatal("3-day-old file should be kept")
	}
}

func TestFileLoggerNoFilesWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	fl := NewFileLogger(dir, LogConfig{FileEnabled: false, Access: true, Audit: true, RetentionDays: 7, MaxFileMB: 100})
	fl.Write(LogEntry{Time: time.Now(), Kind: logKindAccess, Host: "a.test"})
	fl.Write(LogEntry{Time: time.Now(), Kind: logKindAudit, Action: "x"})
	fl.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("disabled logger wrote files: %v", entries)
	}
}

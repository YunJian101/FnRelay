package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBackupRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Rules = []Rule{{
		ID: "r1", Name: "demo", Enabled: true,
		Domains:  []string{"a.example.com", "*.example.com"},
		Upstream: "http://127.0.0.1:8080",
		Headers:  map[string]string{"X-Test": "1"},
	}}
	app := &App{cfg: cfg}
	b := app.MakeBackup()
	if b.Format != backupFormat || b.Schema != backupSchema || b.Version != Version {
		t.Fatalf("backup header: %+v", b)
	}
	if len(b.Config.Rules) != 1 || b.Config.Rules[0].Domains[1] != "*.example.com" {
		t.Fatalf("rules lost: %+v", b.Config.Rules)
	}
	if err := app.validateBackup(b); err != nil {
		t.Fatalf("round trip validate: %v", err)
	}

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"password_hash", "PRIVATE KEY", "selfsigned.key"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("backup contains secret %q", bad)
		}
	}
}

func TestBackupVersionMismatch(t *testing.T) {
	app := &App{cfg: DefaultConfig()}
	b := app.MakeBackup()
	b.Version = "0.0.0"
	err := app.validateBackup(b)
	if err == nil || !strings.Contains(err.Error(), "版本不一致") {
		t.Fatalf("version mismatch not rejected: %v", err)
	}
}

func TestBackupSchemaMismatch(t *testing.T) {
	app := &App{cfg: DefaultConfig()}
	b := app.MakeBackup()
	b.Schema = 99
	if err := app.validateBackup(b); err == nil {
		t.Fatal("schema mismatch not rejected")
	}
	b = app.MakeBackup()
	b.Format = "other"
	if err := app.validateBackup(b); err == nil {
		t.Fatal("format mismatch not rejected")
	}
}

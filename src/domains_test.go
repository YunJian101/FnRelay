package main

import (
	"strings"
	"testing"
)

func TestReplaceDomainSuffix(t *testing.T) {
	cases := []struct {
		domain, want string
		changed      bool
	}{
		{"1.s.com", "1.c.com", true},
		{"2.s.com", "2.c.com", true},
		{"a.b.s.com", "a.b.c.com", true},
		{"*.s.com", "*.c.com", true},
		{"*.a.s.com", "*.a.c.com", true},
		{"s.com", "c.com", true},
		{"nots.com", "nots.com", false},
		{"other.com", "other.com", false},
	}
	for _, tc := range cases {
		got, changed := replaceDomainSuffix(tc.domain, "s.com", "c.com")
		if got != tc.want || changed != tc.changed {
			t.Fatalf("%s -> %s (changed=%v), want %s (changed=%v)", tc.domain, got, changed, tc.want, tc.changed)
		}
	}
}

func TestNormalizeDomainSuffix(t *testing.T) {
	for _, raw := range []string{"s.com", ".s.com", "*.s.com", "  S.COM.  "} {
		got, err := normalizeDomainSuffix(raw)
		if err != nil || got != "s.com" {
			t.Fatalf("%q -> %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "*.", "s.com/path", "s.com:443", "*.s.com*"} {
		if _, err := normalizeDomainSuffix(raw); err == nil {
			t.Fatalf("%q should be rejected", raw)
		}
	}
}

func TestReplaceRuleDomainsCollision(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Name: "one", Enabled: true, Domains: []string{"1.s.com"}, Upstream: "http://127.0.0.1:8080"},
		{ID: "r2", Name: "two", Enabled: true, Domains: []string{"1.c.com"}, Upstream: "http://127.0.0.1:8081"},
	}
	_, _, _, err := replaceRuleDomains(rules, "s.com", "c.com")
	if err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("expected collision, got %v", err)
	}

	rules = []Rule{{ID: "r1", Name: "one", Domains: []string{"1.s.com", "2.s.com"}, Upstream: "http://127.0.0.1:8080"}}
	out, changedRules, changedDomains, err := replaceRuleDomains(rules, "s.com", "c.com")
	if err != nil {
		t.Fatal(err)
	}
	if changedRules != 1 || changedDomains != 2 || out[0].Domains[0] != "1.c.com" || out[0].Domains[1] != "2.c.com" {
		t.Fatalf("bad replacement: %+v %d %d", out, changedRules, changedDomains)
	}
}

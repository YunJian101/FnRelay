package main

import (
	"net/url"
	"sort"
	"strings"
)

type wildRule struct {
	suffix string
	rule   *Rule
}

type ruleIndex struct {
	exact   map[string]*Rule
	wild    []wildRule
	rules   []*Rule
	defRule *Rule
}

func buildIndex(cfg *Config) *ruleIndex {
	ri := &ruleIndex{exact: map[string]*Rule{}}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if !r.Enabled {
			continue
		}
		ri.rules = append(ri.rules, r)
		for _, d := range r.Domains {
			if strings.HasPrefix(d, "*.") {
				ri.wild = append(ri.wild, wildRule{suffix: strings.TrimPrefix(d, "*."), rule: r})
				continue
			}
			if _, ok := ri.exact[d]; !ok {
				ri.exact[d] = r
			}
		}
	}
	sort.SliceStable(ri.wild, func(i, j int) bool {
		return len(ri.wild[i].suffix) > len(ri.wild[j].suffix)
	})
	if u := strings.TrimSpace(cfg.Proxy.DefaultUpstream); u != "" {
		if _, err := parseUpstream(u); err == nil {
			ri.defRule = &Rule{
				ID:           "default",
				Name:         "默认上游",
				Enabled:      true,
				Upstream:     u,
				PreserveHost: true,
			}
		}
	}
	return ri
}

func (ri *ruleIndex) Match(host string) *Rule {
	if ri == nil {
		return nil
	}
	host = normalizeHost(host)
	if host != "" {
		if r, ok := ri.exact[host]; ok {
			return r
		}
		for _, w := range ri.wild {
			if w.suffix == "" {
				continue
			}
			if strings.HasSuffix(host, "."+w.suffix) {
				return w.rule
			}
		}
	}
	return ri.defRule
}

func (ri *ruleIndex) Len() int {
	if ri == nil {
		return 0
	}
	return len(ri.rules)
}

func upstreamOf(r *Rule) *url.URL {
	u, err := parseUpstream(r.Upstream)
	if err != nil {
		return &url.URL{}
	}
	return u
}

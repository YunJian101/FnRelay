package main

import (
	"fmt"
	"strings"
)

func normalizeDomainSuffix(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.TrimPrefix(s, "*.")
	s = strings.TrimPrefix(s, ".")
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return "", fmt.Errorf("域名后缀不能为空")
	}
	if strings.ContainsAny(s, "*:/ ") {
		return "", fmt.Errorf("域名后缀只能包含域名，不能带通配符、端口或路径")
	}
	if !validDomainPattern(s) {
		return "", fmt.Errorf("域名后缀格式不正确: %s", s)
	}
	return s, nil
}

func replaceDomainSuffix(domain, oldSuffix, newSuffix string) (string, bool) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	wildcard := strings.HasPrefix(d, "*.")
	base := strings.TrimPrefix(d, "*.")
	prefix := ""
	if wildcard {
		prefix = "*."
	}
	if base == oldSuffix {
		return prefix + newSuffix, true
	}
	if strings.HasSuffix(base, "."+oldSuffix) {
		return prefix + base[:len(base)-len(oldSuffix)] + newSuffix, true
	}
	return domain, false
}

func replaceRuleDomains(rules []Rule, oldSuffix, newSuffix string) ([]Rule, int, int, error) {
	out := cloneRules(rules)
	owners := map[string]string{}
	changedRules := 0
	changedDomains := 0
	for i := range out {
		rule := &out[i]
		ruleChanged := false
		for j, domain := range rule.Domains {
			if next, ok := replaceDomainSuffix(domain, oldSuffix, newSuffix); ok {
				rule.Domains[j] = next
				changedDomains++
				ruleChanged = true
			}
		}
		if ruleChanged {
			changedRules++
		}
		for _, domain := range rule.Domains {
			if prev, exists := owners[domain]; exists {
				label := ruleLabel(rule)
				if prev == rule.ID {
					return nil, 0, 0, fmt.Errorf("规则「%s」替换后出现重复域名: %s", label, domain)
				}
				return nil, 0, 0, fmt.Errorf("域名 %s 替换后与另一条规则冲突", domain)
			}
			owners[domain] = rule.ID
		}
	}
	if changedDomains == 0 {
		return nil, 0, 0, fmt.Errorf("没有匹配 .%s 的域名", oldSuffix)
	}
	return out, changedRules, changedDomains, nil
}

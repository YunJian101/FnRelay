package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const configVersion = 1

type Config struct {
	Version int         `json:"version"`
	Admin   AdminConfig `json:"admin"`
	Proxy   ProxyConfig `json:"proxy"`
	Rules   []Rule      `json:"rules"`
	Certs   CertConfig  `json:"certs"`
	Logs    LogConfig   `json:"logs"`
}

type LogConfig struct {
	FileEnabled   bool `json:"file_enabled"`
	Access        bool `json:"access"`
	Audit         bool `json:"audit"`
	RetentionDays int  `json:"retention_days"`
	MaxFileMB     int  `json:"max_file_mb"`
}

type AdminConfig struct {
	PasswordHash string `json:"password_hash"`
	SessionDays  int    `json:"session_days"`
}

func (a AdminConfig) Configured() bool { return a.PasswordHash != "" }

type ProxyConfig struct {
	Port            int    `json:"port"`
	HTTPRedirect    bool   `json:"http_redirect"`
	RedirectMode    string `json:"redirect_mode"` // same_port | custom_port
	RedirectPort    int    `json:"redirect_port"`
	ExtraHTTPPort   int    `json:"extra_http_port"`
	DefaultUpstream string `json:"default_upstream"`
	HSTS            bool   `json:"hsts"`
	AutoFallback    bool   `json:"auto_fallback"`
	Title           string `json:"title"`
}

type Rule struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Enabled            bool              `json:"enabled"`
	Domains            []string          `json:"domains"`
	Upstream           string            `json:"upstream"`
	PreserveHost       bool              `json:"preserve_host"`
	InsecureSkipVerify bool              `json:"insecure_skip_verify"`
	CertHint           string            `json:"cert_hint"`
	Note               string            `json:"note"`
	Headers            map[string]string `json:"headers,omitempty"`
}

type CertConfig struct {
	AutoFnos    bool     `json:"auto_fnos"`
	ExtraDirs   []string `json:"extra_dirs"`
	DefaultCert string   `json:"default_cert"`
}

func DefaultConfig() *Config {
	return &Config{
		Version: configVersion,
		Admin:   AdminConfig{SessionDays: 7},
		Proxy: ProxyConfig{
			Port:         443,
			HTTPRedirect: true,
			RedirectMode: "same_port",
			RedirectPort: 443,
			Title:        "FnRelay",
		},
		Certs: CertConfig{AutoFnos: true},
		Logs: LogConfig{
			FileEnabled:   false,
			Access:        true,
			Audit:         true,
			RetentionDays: 7,
			MaxFileMB:     100,
		},
	}
}

func (c *Config) Normalize() {
	if c.Version == 0 {
		c.Version = configVersion
	}
	if c.Admin.SessionDays <= 0 || c.Admin.SessionDays > 365 {
		c.Admin.SessionDays = 7
	}
	if c.Proxy.RedirectMode != "custom_port" {
		c.Proxy.RedirectMode = "same_port"
	}
	if c.Proxy.RedirectPort == 0 {
		c.Proxy.RedirectPort = c.Proxy.Port
	}
	if c.Proxy.Title == "" {
		c.Proxy.Title = "FnRelay"
	}
	if c.Logs.RetentionDays <= 0 {
		c.Logs.RetentionDays = 7
	}
	if c.Logs.RetentionDays > 7 {
		c.Logs.RetentionDays = 7
	}
	if c.Logs.MaxFileMB <= 0 {
		c.Logs.MaxFileMB = 100
	}
	if c.Logs.MaxFileMB > 1024 {
		c.Logs.MaxFileMB = 1024
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			r.ID = newRuleID()
		}
		r.Name = strings.TrimSpace(r.Name)
		r.Upstream = strings.TrimSpace(r.Upstream)
		r.CertHint = strings.TrimSpace(strings.ToLower(r.CertHint))
		r.Note = strings.TrimSpace(r.Note)
		r.Domains = normalizeDomains(r.Domains)
		if r.Name == "" && len(r.Domains) > 0 {
			r.Name = r.Domains[0]
		}
	}
	c.Proxy.DefaultUpstream = strings.TrimSpace(c.Proxy.DefaultUpstream)
	for i := range c.Certs.ExtraDirs {
		c.Certs.ExtraDirs[i] = strings.TrimSpace(c.Certs.ExtraDirs[i])
	}
	c.Certs.DefaultCert = strings.TrimSpace(strings.ToLower(c.Certs.DefaultCert))
}

func normalizeDomains(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == ';' || r == '，'
		}) {
			d := strings.ToLower(strings.TrimSpace(part))
			d = strings.TrimPrefix(d, "http://")
			d = strings.TrimPrefix(d, "https://")
			if i := strings.IndexAny(d, "/?#"); i >= 0 {
				d = d[:i]
			}
			d = strings.TrimSuffix(d, ".")
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func (c *Config) Validate(adminPort int) error {
	if c.Proxy.Port < 1 || c.Proxy.Port > 65535 {
		return errors.New("代理端口必须在 1-65535 之间")
	}
	if c.Proxy.Port == adminPort {
		return fmt.Errorf("代理端口不能与管理端口 %d 相同", adminPort)
	}
	if c.Proxy.RedirectMode != "same_port" && c.Proxy.RedirectMode != "custom_port" {
		return errors.New("跳转模式不合法")
	}
	if c.Proxy.RedirectPort < 1 || c.Proxy.RedirectPort > 65535 {
		return errors.New("跳转目标端口不合法")
	}
	if c.Proxy.ExtraHTTPPort != 0 {
		if c.Proxy.ExtraHTTPPort < 1 || c.Proxy.ExtraHTTPPort > 65535 {
			return errors.New("额外 HTTP 端口不合法")
		}
		if c.Proxy.ExtraHTTPPort == c.Proxy.Port || c.Proxy.ExtraHTTPPort == adminPort {
			return errors.New("额外 HTTP 端口不能与代理端口或管理端口相同")
		}
	}
	if c.Proxy.DefaultUpstream != "" {
		if _, err := parseUpstream(c.Proxy.DefaultUpstream); err != nil {
			return fmt.Errorf("默认上游不合法: %v", err)
		}
	}
	if c.Logs.RetentionDays < 1 || c.Logs.RetentionDays > 7 {
		return errors.New("日志保留天数必须在 1-7 之间")
	}
	if c.Logs.MaxFileMB < 1 || c.Logs.MaxFileMB > 1024 {
		return errors.New("单日日志上限必须在 1-1024 MB 之间")
	}
	seen := map[string]string{}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.Upstream == "" {
			return fmt.Errorf("规则「%s」缺少上游地址", ruleLabel(r))
		}
		if _, err := parseUpstream(r.Upstream); err != nil {
			return fmt.Errorf("规则「%s」上游不合法: %v", ruleLabel(r), err)
		}
		if len(r.Domains) == 0 {
			return fmt.Errorf("规则「%s」至少需要一个域名", ruleLabel(r))
		}
		for _, d := range r.Domains {
			if !validDomainPattern(d) {
				return fmt.Errorf("域名「%s」格式不正确", d)
			}
			if r.Enabled {
				if other, ok := seen[d]; ok && other != r.ID {
					return fmt.Errorf("域名「%s」已经被另一条规则使用", d)
				}
				seen[d] = r.ID
			}
		}
		if r.Headers != nil {
			for k, v := range r.Headers {
				if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
					return fmt.Errorf("规则「%s」的自定义请求头不合法", ruleLabel(r))
				}
			}
		}
	}
	for _, d := range c.Certs.ExtraDirs {
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) {
			return fmt.Errorf("证书目录必须是绝对路径: %s", d)
		}
	}
	return nil
}

func ruleLabel(r *Rule) string {
	if r.Name != "" {
		return r.Name
	}
	if len(r.Domains) > 0 {
		return r.Domains[0]
	}
	return r.ID
}

func validDomainPattern(d string) bool {
	if d == "" || len(d) > 253 {
		return false
	}
	if strings.HasPrefix(d, "*.") {
		d = d[2:]
		if d == "" || strings.Contains(d, "*") {
			return false
		}
	} else if strings.Contains(d, "*") {
		return false
	}
	if strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || strings.Contains(d, "..") {
		return false
	}
	for _, r := range d {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '.' || r == '_' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func parseUpstream(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("地址为空")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("只支持 http/https 上游")
	}
	if u.Host == "" {
		return nil, errors.New("缺少主机")
	}
	if u.Path == "/" {
		u.Path = ""
	}
	return u, nil
}

func normalizeHost(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	if strings.HasPrefix(h, "[") {
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		h = strings.Trim(h, "[]")
	} else if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(h, ".")
	return h
}

func newRuleID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("r%d", time.Now().UnixNano())
	}
	return "r" + hex.EncodeToString(b)
}

func (c *Config) Clone() *Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return &out
}

func LoadConfig(path string) (*Config, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), false, nil
		}
		return nil, false, err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, true, fmt.Errorf("配置文件解析失败: %w", err)
	}
	cfg.Normalize()
	return cfg, true, nil
}

func SaveConfig(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

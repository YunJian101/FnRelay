package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const sessionCookie = "fnrelay_session"

type stateResponse struct {
	NeedsSetup    bool           `json:"needs_setup"`
	Authenticated bool           `json:"authenticated"`
	Version       string         `json:"version"`
	BuildTime     string         `json:"build_time"`
	Hostname      string         `json:"hostname"`
	Config        *Config        `json:"config,omitempty"`
	Status        *RuntimeStatus `json:"status,omitempty"`
	Certs         []CertEntry    `json:"certs,omitempty"`
	Logs          []LogEntry     `json:"logs,omitempty"`
	LogStats      *LogStats      `json:"log_stats,omitempty"`
	LogDates      []LogDateInfo  `json:"log_dates,omitempty"`
	Warning       string         `json:"warning,omitempty"`
	Detected      detectedInfo   `json:"detected"`
	SuggestedPort int            `json:"suggested_port,omitempty"`
}

type detectedInfo struct {
	FnosCertRoot bool     `json:"fnos_cert_root"`
	FnosCertConf bool     `json:"fnos_cert_conf"`
	CertRoots    []string `json:"cert_roots,omitempty"`
}

func (a *App) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		certCount, _, certErr := a.certs.Stats()
		a.writeJSON(w, 200, map[string]any{
			"ok":       true,
			"version":  Version,
			"certs":    certCount,
			"certs_ok": certErr == "",
		})
	})
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("POST /api/domains/replace", a.mutating(a.authed(a.handleDomainReplace)))
	mux.HandleFunc("POST /api/rules", a.mutating(a.authed(a.handleRuleCreate)))
	mux.HandleFunc("PUT /api/rules/{id}", a.mutating(a.authed(a.handleRuleUpdate)))
	mux.HandleFunc("DELETE /api/rules/{id}", a.mutating(a.authed(a.handleRuleDelete)))
	mux.HandleFunc("POST /api/rules/{id}/toggle", a.mutating(a.authed(a.handleRuleToggle)))
	mux.HandleFunc("POST /api/settings", a.mutating(a.authed(a.handleSettings)))
	mux.HandleFunc("GET /api/certs", a.authed(a.handleCerts))
	mux.HandleFunc("POST /api/certs/reload", a.mutating(a.authed(a.handleCertReload)))
	mux.HandleFunc("POST /api/certs/import", a.mutating(a.authed(a.handleCertImport)))
	mux.HandleFunc("DELETE /api/certs/{id}", a.mutating(a.authed(a.handleCertDelete)))
	mux.HandleFunc("GET /api/stats", a.authed(a.handleStats))
	mux.HandleFunc("GET /api/backup", a.authed(a.handleBackup))
	mux.HandleFunc("POST /api/restore", a.mutating(a.authed(a.handleRestore)))
	mux.HandleFunc("GET /api/live", a.authed(a.handleLive))
	mux.HandleFunc("GET /api/logs", a.authed(a.handleLogs))
	mux.HandleFunc("GET /api/logs/dates", a.authed(a.handleLogDates))
	mux.HandleFunc("GET /api/logs/download", a.authed(a.handleLogDownload))
	mux.HandleFunc("POST /api/test", a.mutating(a.authed(a.handleTest)))

	sub, err := webSub()
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", cacheStatic(fileServer)))
	mux.HandleFunc("GET /", a.serveIndex)
	return securityHeaders(mux)
}

func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	if !a.isAuthed(r) {
		a.writeJSON(w, 403, map[string]any{"error": "请从飞牛桌面打开 FnRelay（需要飞牛管理员账号）"})
		return
	}
	cfg := a.Config()
	hostname, _ := os.Hostname()
	cp := cfg.Clone()
	cp.Admin.PasswordHash = ""
	resp := stateResponse{
		NeedsSetup:    false,
		Authenticated: true,
		Version:       Version,
		BuildTime:     BuildTime,
		Hostname:      hostname,
		Config:        cp,
		Detected:      detectEnvironment(),
	}
	st := a.Status()
	resp.Status = &st
	certs, _, _ := a.Certificates()
	resp.Certs = certs
	resp.Logs = a.logs.List(200, "", "")
	if a.fileLog != nil {
		st := a.fileLog.Stats()
		resp.LogStats = &st
		resp.LogDates = a.fileLog.Dates()
	}
	resp.Warning = a.autoFallback
	if resp.Warning == "" && cfg.Proxy.AutoFallback && cfg.Proxy.Port != 443 {
		resp.Warning = fmt.Sprintf("443 端口被飞牛系统占用，当前使用 %d 端口；可在「设置」里修改代理端口。", cfg.Proxy.Port)
	}
	a.writeJSON(w, 200, resp)
}

func detectEnvironment() detectedInfo {
	d := detectedInfo{}
	if st, err := os.Stat("/usr/trim/var/trim_connect/ssls"); err == nil && st.IsDir() {
		d.FnosCertRoot = true
		d.CertRoots = append(d.CertRoots, "/usr/trim/var/trim_connect/ssls")
	}
	if _, err := os.Stat("/usr/trim/etc/network_gateway_cert.conf"); err == nil {
		d.FnosCertConf = true
	}
	return d
}

type gatewayBaseKey struct{}

func gatewayBase(r *http.Request) string {
	if v, ok := r.Context().Value(gatewayBaseKey{}).(string); ok && v != "" {
		return v
	}
	return "/"
}

// WithGateway 把统一网关的 /app/{appname}/... 前缀剥掉后交给管理后台。
// 同一个 handler 也可以挂在 TCP 端口上，便于本地调试。
func (a *App) WithGateway(root http.Handler, prefix string, forceBase bool) http.Handler {
	prefix = "/" + strings.Trim(prefix, "/")
	if prefix == "/" {
		return root
	}
	base := prefix + "/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			stripped := strings.TrimPrefix(p, prefix)
			if stripped == "" {
				stripped = "/"
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = stripped
			r2.URL.RawPath = ""
			r2 = r2.WithContext(context.WithValue(r2.Context(), gatewayBaseKey{}, base))
			root.ServeHTTP(w, r2)
			return
		}
		if forceBase {
			root.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), gatewayBaseKey{}, base)))
			return
		}
		root.ServeHTTP(w, r)
	})
}

func (a *App) serveIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		a.writeJSON(w, 404, map[string]any{"error": "接口不存在"})
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "UI 资源缺失", 500)
		return
	}
	page := strings.Replace(string(b), "<!--BASE-->", `<base href="`+html.EscapeString(gatewayBase(r))+`">`, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(page))
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	cfg := a.Config()
	if !cfg.Admin.Configured() {
		a.writeJSON(w, 400, map[string]any{"error": "尚未设置管理密码", "needs_setup": true})
		return
	}
	ip := clientIP(r)
	if blocked, wait := a.limiter.Blocked(ip); blocked {
		a.writeJSON(w, 429, map[string]any{"error": fmt.Sprintf("尝试次数过多，请 %d 分钟后再试", int(wait.Minutes())+1)})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	if !verifyPassword(cfg.Admin.PasswordHash, req.Password) {
		a.limiter.Fail(ip)
		a.writeJSON(w, 401, map[string]any{"error": "密码不正确"})
		return
	}
	a.limiter.Reset(ip)
	token := a.sessions.Create()
	if token == "" {
		a.writeJSON(w, 500, map[string]any{"error": "创建会话失败"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   cfg.Admin.SessionDays * 86400,
	})
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.Drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	cfg := a.Config()
	if cfg.Admin.Configured() {
		a.writeJSON(w, 403, map[string]any{"error": "应用已经初始化"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	if len(req.Password) < 8 {
		a.writeJSON(w, 400, map[string]any{"error": "管理密码至少 8 位"})
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		a.writeJSON(w, 500, map[string]any{"error": "生成密码失败"})
		return
	}
	_, err = a.Update(func(c *Config) error {
		c.Admin.PasswordHash = hash
		return nil
	})
	if err != nil {
		a.writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	token := a.sessions.Create()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 86400,
	})
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleDomainReplace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldSuffix string `json:"old_suffix"`
		NewSuffix string `json:"new_suffix"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	oldSuffix, err := normalizeDomainSuffix(req.OldSuffix)
	if err != nil {
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	newSuffix, err := normalizeDomainSuffix(req.NewSuffix)
	if err != nil {
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if oldSuffix == newSuffix {
		a.writeJSON(w, 400, map[string]any{"error": "新旧域名后缀不能相同"})
		return
	}
	var changedRules, changedDomains int
	_, err = a.Update(func(c *Config) error {
		rules, cr, cd, replaceErr := replaceRuleDomains(c.Rules, oldSuffix, newSuffix)
		if replaceErr != nil {
			return replaceErr
		}
		c.Rules = rules
		changedRules, changedDomains = cr, cd
		return nil
	})
	if err != nil {
		a.audit(r, "domain.batch_replace", "domains", "error", 400, map[string]any{
			"old_suffix": oldSuffix,
			"new_suffix": newSuffix,
		}, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	a.audit(r, "domain.batch_replace", "domains", "ok", 200, map[string]any{
		"old_suffix":      oldSuffix,
		"new_suffix":      newSuffix,
		"changed_rules":   changedRules,
		"changed_domains": changedDomains,
	}, "")
	a.writeJSON(w, 200, map[string]any{
		"ok":              true,
		"old_suffix":      oldSuffix,
		"new_suffix":      newSuffix,
		"changed_rules":   changedRules,
		"changed_domains": changedDomains,
	})
}

func (a *App) handleRuleCreate(w http.ResponseWriter, r *http.Request) {
	var rule Rule
	if !decodeJSON(w, r, &rule, 1<<20) {
		return
	}
	rule.ID = newRuleID()
	_, err := a.Update(func(c *Config) error {
		c.Rules = append(c.Rules, rule)
		return nil
	})
	if err != nil {
		a.audit(r, "rule.create", rule.ID, "error", 400, nil, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	after := a.ruleSnapshot(rule.ID)
	if after == nil {
		after = &rule
	}
	a.audit(r, "rule.create", rule.ID, "ok", 200, map[string]any{"rule": ruleAuditMap(after)}, "")
	a.writeJSON(w, 200, map[string]any{"ok": true, "id": rule.ID})
}

func (a *App) handleRuleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var rule Rule
	if !decodeJSON(w, r, &rule, 1<<20) {
		return
	}
	rule.ID = id
	before := a.ruleSnapshot(id)
	_, err := a.Update(func(c *Config) error {
		for i := range c.Rules {
			if c.Rules[i].ID == id {
				c.Rules[i] = rule
				return nil
			}
		}
		return fmt.Errorf("规则不存在")
	})
	if err != nil {
		a.audit(r, "rule.update", id, "error", 400, nil, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	after := a.ruleSnapshot(id)
	if after == nil {
		after = &rule
	}
	a.audit(r, "rule.update", id, "ok", 200, map[string]any{
		"before": ruleAuditMap(before),
		"after":  ruleAuditMap(after),
	}, "")
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	before := a.ruleSnapshot(id)
	_, err := a.Update(func(c *Config) error {
		for i := range c.Rules {
			if c.Rules[i].ID == id {
				c.Rules = append(c.Rules[:i], c.Rules[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("规则不存在")
	})
	if err != nil {
		a.audit(r, "rule.delete", id, "error", 400, nil, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	a.audit(r, "rule.delete", id, "ok", 200, map[string]any{"before": ruleAuditMap(before)}, "")
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleRuleToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	before := a.ruleSnapshot(id)
	_, err := a.Update(func(c *Config) error {
		for i := range c.Rules {
			if c.Rules[i].ID == id {
				c.Rules[i].Enabled = req.Enabled
				return nil
			}
		}
		return fmt.Errorf("规则不存在")
	})
	if err != nil {
		a.audit(r, "rule.toggle", id, "error", 400, nil, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	detail := map[string]any{"enabled": req.Enabled}
	if before != nil {
		detail["name"] = before.Name
		detail["before_enabled"] = before.Enabled
	}
	a.audit(r, "rule.toggle", id, "ok", 200, detail, "")
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proxy ProxyConfig `json:"proxy"`
		Certs CertConfig  `json:"certs"`
		Logs  *LogConfig  `json:"logs"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	req.Proxy.AutoFallback = false
	before := a.Config()
	after, err := a.Update(func(c *Config) error {
		title := c.Proxy.Title
		c.Proxy = req.Proxy
		if c.Proxy.Title == "" {
			c.Proxy.Title = title
		}
		c.Certs = req.Certs
		if req.Logs != nil {
			c.Logs = *req.Logs
		}
		return nil
	})
	if err != nil {
		detail := map[string]any{"before": settingsAuditMap(before)}
		if after != nil {
			detail["after"] = settingsAuditMap(after)
		}
		a.audit(r, "settings.save", "config", "error", 400, detail, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	a.audit(r, "settings.save", "config", "ok", 200, map[string]any{
		"before": settingsAuditMap(before),
		"after":  settingsAuditMap(after),
	}, "")
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	cfg := a.Config()
	if !verifyPassword(cfg.Admin.PasswordHash, req.Current) {
		a.writeJSON(w, 400, map[string]any{"error": "当前密码不正确"})
		return
	}
	if len(req.New) < 8 {
		a.writeJSON(w, 400, map[string]any{"error": "新密码至少 8 位"})
		return
	}
	hash, err := hashPassword(req.New)
	if err != nil {
		a.writeJSON(w, 500, map[string]any{"error": "生成密码失败"})
		return
	}
	if _, err := a.Update(func(c *Config) error {
		c.Admin.PasswordHash = hash
		return nil
	}); err != nil {
		a.writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleCerts(w http.ResponseWriter, r *http.Request) {
	certs, loaded, errMsg := a.Certificates()
	a.writeJSON(w, 200, map[string]any{"certs": certs, "loaded_at": loaded, "error": errMsg})
}

func (a *App) handleCertReload(w http.ResponseWriter, r *http.Request) {
	err := a.certs.Reload(true)
	certs, loaded, errMsg := a.Certificates()
	if err != nil && len(certs) == 0 {
		a.audit(r, "cert.reload", "certificates", "error", 500, nil, err.Error())
		a.writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	a.audit(r, "cert.reload", "certificates", "ok", 200, map[string]any{
		"count": len(certs),
		"error": errMsg,
	}, "")
	a.writeJSON(w, 200, map[string]any{"ok": true, "certs": certs, "loaded_at": loaded, "warning": errMsg})
}

func (a *App) handleCertImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if !decodeJSON(w, r, &req, 8<<20) {
		return
	}
	if strings.TrimSpace(req.Cert) == "" || strings.TrimSpace(req.Key) == "" {
		a.writeJSON(w, 400, map[string]any{"error": "证书和私钥都不能为空"})
		return
	}
	entry, err := a.certs.Import(req.Name, req.Cert, req.Key)
	if err != nil {
		a.audit(r, "cert.import", req.Name, "error", 400, nil, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	a.logf("系统", "手动导入证书 %s（%s）", entry.Name, strings.Join(entry.Domains, ", "))
	a.audit(r, "cert.import", entry.ID, "ok", 200, map[string]any{
		"name":    entry.Name,
		"domains": entry.Domains,
		"serial":  entry.Serial,
	}, "")
	a.writeJSON(w, 200, map[string]any{"ok": true, "cert": entry})
}

func (a *App) handleCertDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var before *CertEntry
	certs, _, _ := a.Certificates()
	for _, cert := range certs {
		if cert.ID == id {
			cp := cert
			before = &cp
			break
		}
	}
	if err := a.certs.DeleteManual(id); err != nil {
		a.audit(r, "cert.delete", id, "error", 400, nil, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	detail := map[string]any{"id": id}
	if before != nil {
		detail["name"] = before.Name
		detail["domains"] = before.Domains
	}
	a.audit(r, "cert.delete", id, "ok", 200, detail, "")
	a.writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, 200, a.Status())
}

func (a *App) handleBackup(w http.ResponseWriter, r *http.Request) {
	b := a.MakeBackup()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fnrelay-backup-%s-%s.json"`, Version, time.Now().Format("20060102-150405")))
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(b)
}

func (a *App) handleRestore(w http.ResponseWriter, r *http.Request) {
	var b BackupFile
	if !decodeJSON(w, r, &b, 4<<20) {
		return
	}
	next, err := a.RestoreBackup(b)
	if err != nil {
		a.audit(r, "backup.restore", "config", "error", 400, map[string]any{
			"backup_version": b.Version,
		}, err.Error())
		a.writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	a.audit(r, "backup.restore", "config", "ok", 200, map[string]any{
		"backup_version": b.Version,
		"rules":          len(next.Rules),
	}, "")
	a.writeJSON(w, 200, map[string]any{"ok": true, "version": Version, "rules": len(next.Rules)})
}

func (a *App) handleLive(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, 200, a.live.Snapshot())
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 300
	if v := q.Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	if limit <= 0 || limit > 2000 {
		limit = 300
	}
	kind := q.Get("kind")
	text := q.Get("q")
	if text == "" {
		text = q.Get("host")
	}
	date := q.Get("date")
	if date != "" {
		if !validLogDate(date) {
			a.writeJSON(w, 400, map[string]any{"error": "日期格式不正确"})
			return
		}
		if a.fileLog != nil {
			entries, err := a.fileLog.Read(date, kind, limit)
			if err == nil {
				a.writeJSON(w, 200, map[string]any{
					"logs":   filterLogEntries(entries, text),
					"source": "file",
					"date":   date,
				})
				return
			}
			if date != time.Now().Format("2006-01-02") {
				a.writeJSON(w, 404, map[string]any{"error": "该日期没有日志文件"})
				return
			}
		}
	}
	a.writeJSON(w, 200, map[string]any{
		"logs":   a.logs.Search(limit, kind, text),
		"source": "memory",
	})
}

func (a *App) handleLogDates(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"dates": []LogDateInfo{}, "today": time.Now().Format("2006-01-02")}
	if a.fileLog != nil {
		resp["dates"] = a.fileLog.Dates()
		resp["stats"] = a.fileLog.Stats()
	}
	a.writeJSON(w, 200, resp)
}

func (a *App) handleLogDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	date := q.Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	kind := q.Get("kind")
	if kind == "" {
		kind = logKindAccess
	}
	if a.fileLog == nil {
		a.writeJSON(w, 404, map[string]any{"error": "日志文件未启用"})
		return
	}
	path, err := a.fileLog.DownloadPath(date, kind)
	if err != nil {
		a.writeJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fnrelay-%s-%s.jsonl"`, kind, date))
	http.ServeFile(w, r, path)
}

func filterLogEntries(entries []LogEntry, text string) []LogEntry {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return entries
	}
	out := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(strings.ToLower(entrySearchText(e)), text) {
			out = append(out, e)
		}
	}
	return out
}

func (a *App) handleTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	host := normalizeHost(req.Host)
	if host == "" {
		a.writeJSON(w, 400, map[string]any{"error": "请输入域名"})
		return
	}
	rule := a.Index().Match(host)
	resp := map[string]any{"host": host, "matched": rule != nil}
	if rule != nil {
		resp["rule"] = rule.Name
		resp["rule_id"] = rule.ID
		resp["upstream"] = rule.Upstream
	}
	var cert *CertEntry
	if rule != nil && rule.CertHint != "" {
		cert = a.certs.Match(rule.CertHint)
	}
	if cert == nil {
		cert = a.certs.Match(host)
	}
	if cert != nil {
		resp["cert"] = cert
	}
	cfg := a.Config()
	port := 443
	if cfg.Proxy.RedirectMode == "custom_port" {
		port = cfg.Proxy.RedirectPort
	}
	hostPart := host
	if port == 443 {
		if strings.Contains(host, ":") {
			hostPart = "[" + host + "]"
		}
	} else {
		hostPart = net.JoinHostPort(host, fmt.Sprintf("%d", port))
	}
	resp["https_url"] = "https://" + hostPart + "/"
	resp["redirect_mode"] = cfg.Proxy.RedirectMode
	a.writeJSON(w, 200, resp)
}

func (a *App) isAuthed(r *http.Request) bool {
	// 仅本地开发回退端口允许免登录；正式安装只走飞牛统一网关。
	if a.devMode {
		return true
	}
	uid := strings.TrimSpace(r.Header.Get("X-Trim-Userid"))
	if uid == "" {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Trim-Isadmin")))
	return v == "true" || v == "1"
}

func (a *App) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAuthed(r) {
			a.writeJSON(w, 403, map[string]any{"error": "仅飞牛管理员可访问，请从飞牛桌面打开"})
			return
		}
		next(w, r)
	}
}

func (a *App) mutating(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok := r.Header.Get("X-FnRelay") == "1"
		if !ok {
			// 兜底：网关转发时即使丢了自定义头，application/json 也会触发
			// 浏览器预检，跨站表单无法伪造，因此可接受。
			ok = r.Header.Get("X-Trim-Userid") != "" &&
				strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
		}
		if !ok {
			a.writeJSON(w, 403, map[string]any{"error": "缺少请求校验头"})
			return
		}
		id := newRequestID()
		w.Header().Set("X-FnRelay-Request-Id", id)
		r = withRequestID(r, id)
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			// 统一网关已经校验过 NAS 会话，并会带上 X-Trim-Userid；
			// 此时 Host 可能被网关改写，不能只按 Host 拒绝。
			if (err != nil || !strings.EqualFold(u.Host, r.Host)) && r.Header.Get("X-Trim-Userid") == "" {
				a.writeJSON(w, 403, map[string]any{"error": "跨站请求被拒绝"})
				return
			}
		}
		next(w, r)
	}
}

func (a *App) ruleSnapshot(id string) *Rule {
	for _, r := range a.Config().Rules {
		if r.ID == id {
			cp := r
			cp.Domains = append([]string{}, r.Domains...)
			return &cp
		}
	}
	return nil
}

func ruleAuditMap(r *Rule) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"id":                   r.ID,
		"name":                 r.Name,
		"enabled":              r.Enabled,
		"domains":              r.Domains,
		"upstream":             redactURL(r.Upstream),
		"preserve_host":        r.PreserveHost,
		"insecure_skip_verify": r.InsecureSkipVerify,
		"cert_hint":            r.CertHint,
		"headers_count":        len(r.Headers),
	}
}

func settingsAuditMap(c *Config) map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"proxy": proxyAuditMap(c.Proxy),
		"certs": certConfigAuditMap(c.Certs),
		"logs":  logConfigAuditMap(c.Logs),
	}
}

func proxyAuditMap(p ProxyConfig) map[string]any {
	return map[string]any{
		"port":             p.Port,
		"http_redirect":    p.HTTPRedirect,
		"redirect_mode":    p.RedirectMode,
		"redirect_port":    p.RedirectPort,
		"extra_http_port":  p.ExtraHTTPPort,
		"default_upstream": redactURL(p.DefaultUpstream),
		"hsts":             p.HSTS,
	}
}

func certConfigAuditMap(c CertConfig) map[string]any {
	return map[string]any{
		"auto_fnos":    c.AutoFnos,
		"extra_dirs":   c.ExtraDirs,
		"default_cert": c.DefaultCert,
	}
}

func logConfigAuditMap(l LogConfig) map[string]any {
	return map[string]any{
		"file_enabled":   l.FileEnabled,
		"access":         l.Access,
		"audit":          l.Audit,
		"retention_days": l.RetentionDays,
		"max_file_mb":    l.MaxFileMB,
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, max int64) bool {
	body := http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(body)
	if err := dec.Decode(dst); err != nil {
		if err == io.EOF {
			http.Error(w, `{"error":"请求体为空"}`, 400)
			return false
		}
		http.Error(w, `{"error":"请求格式不正确"}`, 400)
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

package main

import (
	"fmt"
	"time"
)

const (
	backupFormat = "fnrelay-backup"
	backupSchema = 1
)

type BackupFile struct {
	Format    string       `json:"format"`
	Schema    int          `json:"schema"`
	Version   string       `json:"version"`
	CreatedAt time.Time    `json:"created_at"`
	Config    BackupConfig `json:"config"`
}

type BackupConfig struct {
	Proxy ProxyConfig `json:"proxy"`
	Certs CertConfig  `json:"certs"`
	Logs  LogConfig   `json:"logs"`
	Rules []Rule      `json:"rules"`
}

func cloneRules(in []Rule) []Rule {
	out := make([]Rule, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Domains = append([]string{}, in[i].Domains...)
		if in[i].Headers != nil {
			out[i].Headers = map[string]string{}
			for k, v := range in[i].Headers {
				out[i].Headers[k] = v
			}
		}
	}
	return out
}

func cloneCertConfig(in CertConfig) CertConfig {
	out := in
	out.ExtraDirs = append([]string{}, in.ExtraDirs...)
	return out
}

func (a *App) MakeBackup() BackupFile {
	cfg := a.Config()
	return BackupFile{
		Format:    backupFormat,
		Schema:    backupSchema,
		Version:   Version,
		CreatedAt: time.Now(),
		Config: BackupConfig{
			Proxy: cfg.Proxy,
			Certs: cloneCertConfig(cfg.Certs),
			Logs:  cfg.Logs,
			Rules: cloneRules(cfg.Rules),
		},
	}
}

func (a *App) validateBackup(b BackupFile) error {
	if b.Format != backupFormat {
		return fmt.Errorf("不是 FnRelay 备份文件")
	}
	if b.Schema != backupSchema {
		return fmt.Errorf("备份格式版本不支持：%d", b.Schema)
	}
	if b.Version != Version {
		return fmt.Errorf("版本不一致：备份为 %s，当前为 %s", b.Version, Version)
	}
	next := a.Config().Clone()
	next.Proxy = b.Config.Proxy
	next.Certs = cloneCertConfig(b.Config.Certs)
	next.Logs = b.Config.Logs
	next.Rules = cloneRules(b.Config.Rules)
	next.Normalize()
	if err := next.Validate(a.adminPort); err != nil {
		return fmt.Errorf("备份配置不合法: %w", err)
	}
	return nil
}

func (a *App) RestoreBackup(b BackupFile) (*Config, error) {
	if err := a.validateBackup(b); err != nil {
		return nil, err
	}
	next, err := a.Update(func(c *Config) error {
		c.Proxy = b.Config.Proxy
		c.Certs = cloneCertConfig(b.Config.Certs)
		c.Logs = b.Config.Logs
		c.Rules = cloneRules(b.Config.Rules)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	certSourceFnos   = "fnos"
	certSourceManual = "manual"
	certSourceCustom = "custom"
	certSourceSelf   = "self-signed"
)

var errSkipCert = errors.New("skip auxiliary certificate")

type CertEntry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Domains   []string  `json:"domains"`
	Source    string    `json:"source"`
	Path      string    `json:"path"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Issuer    string    `json:"issuer"`
	Serial    string    `json:"serial"`
	Expired   bool      `json:"expired"`
	DaysLeft  int       `json:"days_left"`
	IsDefault bool      `json:"is_default"`
	Builtin   bool      `json:"builtin"`
	Manual    bool      `json:"manual"`
	Error     string    `json:"error,omitempty"`
}

type CertStore struct {
	mu          sync.RWMutex
	cfg         CertConfig
	dataDir     string
	entries     []*CertEntry
	pairs       []*tls.Certificate
	exact       map[string]int
	wild        []wildCert
	def         int
	self        *tls.Certificate
	lastLoad    time.Time
	lastErr     string
	selfErr     string
	fingerprint string
	loaded      int
}

type wildCert struct {
	suffix string
	idx    int
}

type certFile struct {
	path   string
	source string
}

type loadedCert struct {
	pair  *tls.Certificate
	leaf  *x509.Certificate
	entry *CertEntry
}

var certPathRe = regexp.MustCompile(`(/[A-Za-z0-9_@./:\-]+\.(?:crt|cer|pem|key))`)

func NewCertStore(dataDir string, cfg CertConfig) *CertStore {
	s := &CertStore{dataDir: dataDir, cfg: cfg, exact: map[string]int{}, def: -1}
	if err := s.ensureSelfSigned(); err != nil {
		s.selfErr = err.Error()
		appLog.Printf("内置自签证书生成失败: %v", err)
	}
	return s
}

func (s *CertStore) ensureSelfSigned() error {
	certPath := filepath.Join(s.dataDir, "selfsigned.crt")
	keyPath := filepath.Join(s.dataDir, "selfsigned.key")
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if keyPEM, err := os.ReadFile(keyPath); err == nil {
			if pair, err := tls.X509KeyPair(certPEM, keyPEM); err == nil {
				leaf, _ := x509.ParseCertificate(pair.Certificate[0])
				pair.Leaf = leaf
				s.self = &pair
				return nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成密钥失败: %w", err)
	}
	hostname, _ := os.Hostname()
	dns := []string{"fnrelay.local", "localhost"}
	if h := strings.ToLower(strings.TrimSpace(hostname)); isASCIIDNSName(h) {
		dns = append(dns, h)
	}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				ips = append(ips, ipnet.IP)
			}
		}
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "FnRelay Self-signed", Organization: []string{"FnRelay"}},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("签发失败: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("编码私钥失败: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("写入证书失败: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("写入私钥失败: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("加载自签证书失败: %w", err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	pair.Leaf = leaf
	s.self = &pair
	return nil
}

func isASCIIDNSName(s string) bool {
	if s == "" || len(s) > 253 || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func (s *CertStore) SetConfig(cfg CertConfig) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *CertStore) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: s.GetCertificate,
		NextProtos:     []string{"http/1.1"},
	}
}

func (s *CertStore) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := ""
	if hello != nil {
		name = normalizeHost(hello.ServerName)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if name != "" {
		if idx, ok := s.exact[name]; ok && idx < len(s.pairs) {
			return s.pairs[idx], nil
		}
		best := -1
		bestLen := -1
		for _, w := range s.wild {
			if name == w.suffix || !strings.HasSuffix(name, "."+w.suffix) {
				continue
			}
			if len(w.suffix) > bestLen {
				bestLen = len(w.suffix)
				best = w.idx
			}
		}
		if best >= 0 && best < len(s.pairs) {
			return s.pairs[best], nil
		}
	}
	if s.def >= 0 && s.def < len(s.pairs) {
		return s.pairs[s.def], nil
	}
	if s.self != nil {
		return s.self, nil
	}
	return nil, errors.New("没有可用证书")
}

func (s *CertStore) Lookup(hint string) *tls.Certificate {
	hint = strings.ToLower(strings.TrimSpace(hint))
	hint = strings.TrimSuffix(hint, ".")
	if hint == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i, ok := s.exact[hint]; ok && i < len(s.pairs) {
		return s.pairs[i]
	}
	for i, e := range s.entries {
		for _, d := range e.Domains {
			if d == hint && i < len(s.pairs) {
				return s.pairs[i]
			}
		}
		if e.Name == hint && i < len(s.pairs) {
			return s.pairs[i]
		}
	}
	return nil
}

func (s *CertStore) Match(host string) *CertEntry {
	host = normalizeHost(host)
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := -1
	if host != "" {
		if i, ok := s.exact[host]; ok {
			idx = i
		} else {
			bestLen := -1
			for _, w := range s.wild {
				if host == w.suffix || !strings.HasSuffix(host, "."+w.suffix) {
					continue
				}
				if len(w.suffix) > bestLen {
					bestLen = len(w.suffix)
					idx = w.idx
				}
			}
		}
	}
	if idx < 0 {
		idx = s.def
	}
	if idx >= 0 && idx < len(s.entries) {
		cp := *s.entries[idx]
		return &cp
	}
	if s.self != nil {
		if len(s.entries) > 0 {
			cp := *s.entries[0]
			return &cp
		}
		return &CertEntry{
			ID: "builtin-self", Name: "内置自签证书", Source: certSourceSelf,
			Domains: selfDomains(s.self), IsDefault: true, Builtin: true,
			NotAfter: selfNotAfter(s.self),
		}
	}
	return nil
}

func selfDomains(pair *tls.Certificate) []string {
	if pair == nil || pair.Leaf == nil {
		return nil
	}
	return append([]string{}, pair.Leaf.DNSNames...)
}

func selfNotAfter(pair *tls.Certificate) time.Time {
	if pair == nil || pair.Leaf == nil {
		return time.Time{}
	}
	return pair.Leaf.NotAfter
}

func (s *CertStore) Entries() ([]CertEntry, time.Time, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CertEntry, 0, len(s.entries)+1)
	for _, e := range s.entries {
		cp := *e
		cp.Domains = append([]string{}, e.Domains...)
		out = append(out, cp)
	}
	if s.self != nil {
		e := CertEntry{
			ID: "builtin-self", Name: "内置自签证书", Source: certSourceSelf,
			Domains: selfDomains(s.self), IsDefault: s.def < 0 || len(s.entries) == 0,
			Builtin: true, NotAfter: selfNotAfter(s.self),
		}
		if e.NotAfter.IsZero() {
			e.NotAfter = time.Now().AddDate(10, 0, 0)
		}
		e.DaysLeft = int(time.Until(e.NotAfter).Hours() / 24)
		e.Expired = time.Now().After(e.NotAfter)
		if s.self != nil && s.self.Leaf != nil {
			e.Issuer = s.self.Leaf.Issuer.CommonName
			e.Serial = s.self.Leaf.SerialNumber.Text(16)
			e.NotBefore = s.self.Leaf.NotBefore
			e.Domains = append([]string{}, s.self.Leaf.DNSNames...)
		}
		out = append(out, e)
	}
	return out, s.lastLoad, s.lastErr
}

func (s *CertStore) Stats() (int, time.Time, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries), s.lastLoad, s.lastErr
}

func certInfo(leaf *x509.Certificate, source, path string) *CertEntry {
	domains := append([]string{}, leaf.DNSNames...)
	seen := map[string]bool{}
	for _, d := range domains {
		seen[strings.ToLower(d)] = true
	}
	cn := strings.ToLower(strings.TrimSpace(leaf.Subject.CommonName))
	if cn != "" && !seen[cn] {
		domains = append(domains, cn)
	}
	for i := range domains {
		domains[i] = strings.ToLower(domains[i])
	}
	sort.Strings(domains)
	name := ""
	for _, d := range domains {
		if !strings.HasPrefix(d, "*.") {
			name = d
			break
		}
	}
	if name == "" && len(domains) > 0 {
		name = domains[0]
	}
	sum := sha256.Sum256(leaf.Raw)
	e := &CertEntry{
		ID:        hex.EncodeToString(sum[:])[:12],
		Name:      name,
		Domains:   domains,
		Source:    source,
		Path:      path,
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
		Issuer:    leaf.Issuer.CommonName,
		Serial:    leaf.SerialNumber.Text(16),
		Expired:   time.Now().After(leaf.NotAfter),
		DaysLeft:  int(time.Until(leaf.NotAfter).Hours() / 24),
		Manual:    source == certSourceManual,
	}
	if e.Issuer == "" {
		e.Issuer = leaf.Issuer.String()
	}
	return e
}

func (s *CertStore) Reload(force bool) error {
	s.mu.RLock()
	cfg := s.cfg
	oldFP := s.fingerprint
	oldCount := len(s.entries)
	s.mu.RUnlock()

	files := s.collectFiles(cfg)
	fp := fingerprintFiles(files)
	if !force && fp == oldFP {
		return nil
	}

	var loaded []*loadedCert
	var errs []string
	seen := map[string]int{}
	for _, f := range files {
		lc, err := loadCertFile(f.path, f.source)
		if err != nil {
			if errors.Is(err, errSkipCert) {
				continue
			}
			errs = append(errs, fmt.Sprintf("%s: %v", f.path, err))
			continue
		}
		key := lc.entry.ID
		if idx, ok := seen[key]; ok {
			if strings.Contains(strings.ToLower(filepath.Base(f.path)), "fullchain") {
				loaded[idx] = lc
			}
			continue
		}
		seen[key] = len(loaded)
		loaded = append(loaded, lc)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.fingerprint = fp
	s.lastLoad = time.Now()
	if len(loaded) == 0 && oldCount > 0 {
		s.lastErr = strings.Join(errs, "; ")
		return fmt.Errorf("证书重新加载失败，继续使用旧证书: %s", s.lastErr)
	}

	s.pairs = make([]*tls.Certificate, 0, len(loaded))
	s.entries = make([]*CertEntry, 0, len(loaded))
	s.exact = map[string]int{}
	s.wild = nil
	s.def = -1
	for _, lc := range loaded {
		idx := len(s.pairs)
		s.pairs = append(s.pairs, lc.pair)
		s.entries = append(s.entries, lc.entry)
		for _, d := range lc.entry.Domains {
			if strings.HasPrefix(d, "*.") {
				s.wild = append(s.wild, wildCert{suffix: strings.TrimPrefix(d, "*."), idx: idx})
			} else if _, ok := s.exact[d]; !ok {
				s.exact[d] = idx
			}
		}
	}
	if len(loaded) > 0 {
		want := strings.ToLower(strings.TrimSpace(cfg.DefaultCert))
		if want != "" {
			for i, e := range s.entries {
				for _, d := range e.Domains {
					if d == want || e.Name == want {
						s.def = i
						break
					}
				}
				if s.def >= 0 {
					break
				}
			}
		}
		if s.def < 0 {
			s.def = 0
			for i, e := range s.entries {
				if !e.Expired {
					s.def = i
					break
				}
			}
		}
		for i := range s.entries {
			s.entries[i].IsDefault = i == s.def
		}
	}
	s.lastErr = ""
	if len(errs) > 0 {
		s.lastErr = strings.Join(errs, "; ")
	}
	s.loaded = len(s.pairs)
	return nil
}

func (s *CertStore) collectFiles(cfg CertConfig) []certFile {
	var files []certFile
	add := func(path, source string) {
		if strings.HasSuffix(strings.ToLower(path), ".key") {
			return
		}
		files = append(files, certFile{path: path, source: source})
	}

	manualDir := filepath.Join(s.dataDir, "certs")
	walkCertDir(manualDir, certSourceManual, 4, add)

	if cfg.AutoFnos {
		for _, root := range []string{
			"/usr/trim/var/trim_connect/ssls",
			"/usr/trim/etc/ssls",
			"/usr/local/apps/@appdata/trim_connect/ssls",
		} {
			walkCertDir(root, certSourceFnos, 6, add)
		}
		for _, conf := range []string{
			"/usr/trim/etc/network_gateway_cert.conf",
			"/usr/trim/etc/network_cert_all.conf",
			"/etc/network_gateway_cert.conf",
		} {
			if cand, err := confCertPaths(conf); err == nil {
				for _, p := range cand {
					if _, err := os.Stat(p); err == nil {
						add(p, certSourceFnos)
					}
				}
			}
		}
	}
	for _, dir := range cfg.ExtraDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && !st.IsDir() {
			add(dir, certSourceCustom)
			continue
		}
		walkCertDir(dir, certSourceCustom, 4, add)
	}

	sort.SliceStable(files, func(i, j int) bool {
		fi := strings.Contains(strings.ToLower(files[i].path), "fullchain")
		fj := strings.Contains(strings.ToLower(files[j].path), "fullchain")
		if fi != fj {
			return !fi
		}
		return files[i].path < files[j].path
	})

	out := make([]certFile, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		clean := filepath.Clean(f.path)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, f)
	}
	return out
}

func walkCertDir(root, source string, maxDepth int, add func(path, source string)) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return
	}
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".crt", ".cer", ".pem":
			add(path, source)
		}
		return nil
	})
}

func confCertPaths(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	matches := certPathRe.FindAllString(string(b), -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out, nil
}

func fingerprintFiles(files []certFile) string {
	h := sha256.New()
	for _, f := range files {
		st, err := os.Stat(f.path)
		if err != nil {
			fmt.Fprintf(h, "%s|err\n", f.path)
			continue
		}
		fmt.Fprintf(h, "%s|%d|%d\n", f.path, st.Size(), st.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fnOS 会在证书目录里同时放叶子证书、fullchain 和 issuer_certificate.crt
// （中间/根证书）。辅助证书没有对应的服务器私钥，不能当站点证书加载。
func isAuxiliaryCertPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, "fullchain") {
		return false
	}
	if strings.Contains(base, "issuer") || strings.Contains(base, "intermediate") {
		return true
	}
	switch base {
	case "ca.crt", "ca.pem", "ca.cer", "root.crt", "root.pem", "chain.crt", "chain.pem", "trust.crt", "trust.pem":
		return true
	}
	return false
}

func isAuxiliaryLeaf(leaf *x509.Certificate) bool {
	if leaf == nil {
		return false
	}
	// 中间 CA 的特征：CA、无站点 SAN、可签发，且 CN 与 issuer 不同。
	// 自签服务器证书通常 CN == issuer，不会被误判。
	if leaf.IsCA && len(leaf.DNSNames) == 0 && leaf.KeyUsage&x509.KeyUsageCertSign != 0 &&
		!strings.EqualFold(strings.TrimSpace(leaf.Subject.CommonName), strings.TrimSpace(leaf.Issuer.CommonName)) {
		return true
	}
	return false
}

func loadCertFile(certPath, source string) (*loadedCert, error) {
	if isAuxiliaryCertPath(certPath) {
		return nil, errSkipCert
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(certPEM); block != nil {
		if leaf, parseErr := x509.ParseCertificate(block.Bytes); parseErr == nil && isAuxiliaryLeaf(leaf) {
			return nil, errSkipCert
		}
	}
	keyPath := findKeyPath(certPath)
	var keyPEM []byte
	if keyPath != "" {
		keyPEM, err = os.ReadFile(keyPath)
		if err != nil {
			keyPath = ""
		}
	}
	if keyPEM == nil {
		if strings.Contains(string(certPEM), "PRIVATE KEY") {
			keyPEM = certPEM
		} else {
			return nil, errors.New("找不到匹配的私钥文件")
		}
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		keyPEM = []byte(normalizePEM(string(keyPEM)))
		certPEM = []byte(normalizePEM(string(certPEM)))
		pair, err = tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("证书与私钥不匹配: %w", err)
		}
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	pair.Leaf = leaf
	return &loadedCert{pair: &pair, leaf: leaf, entry: certInfo(leaf, source, certPath)}, nil
}

func normalizePEM(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func findKeyPath(certPath string) string {
	dir := filepath.Dir(certPath)
	base := strings.TrimSuffix(certPath, filepath.Ext(certPath))
	baseName := strings.ToLower(filepath.Base(base))

	candidates := []string{base + ".key"}
	// 只有常见的整链/服务证书文件名才允许退回目录里的通用 key，
	// 避免把 issuer_certificate.crt 之类辅助证书硬配给站点私钥。
	switch baseName {
	case "fullchain", "cert", "server", "wildcard":
		candidates = append(candidates,
			filepath.Join(dir, "privkey.key"),
			filepath.Join(dir, "key.pem"),
			filepath.Join(dir, "private.key"),
		)
	}
	for _, c := range candidates {
		if c == certPath {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if len(candidates) > 1 {
		keys, _ := filepath.Glob(filepath.Join(dir, "*.key"))
		if len(keys) == 1 {
			return keys[0]
		}
	}
	return ""
}

func (s *CertStore) Import(name, certPEM, keyPEM string) (*CertEntry, error) {
	certPEM = normalizePEM(certPEM)
	keyPEM = normalizePEM(keyPEM)
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("证书或私钥格式不正确: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	safe := sanitizeFileName(name)
	if safe == "" {
		safe = sanitizeFileName(strings.TrimPrefix(leaf.Subject.CommonName, "*."))
	}
	if safe == "" {
		safe = "manual-" + time.Now().Format("20060102-150405")
	}
	dir := filepath.Join(s.dataDir, "certs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, safe+".crt")
	keyPath := filepath.Join(dir, safe+".key")
	if err := os.WriteFile(certPath, []byte(certPEM), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		return nil, err
	}
	if err := s.Reload(true); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(leaf.Raw)
	id := hex.EncodeToString(sum[:])[:12]
	entries, _, _ := s.Entries()
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i], nil
		}
	}
	return certInfo(leaf, certSourceManual, certPath), nil
}

func sanitizeFileName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ".-_")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func (s *CertStore) DeleteManual(id string) error {
	s.mu.RLock()
	var target *CertEntry
	for _, e := range s.entries {
		if e.ID == id {
			target = e
			break
		}
	}
	s.mu.RUnlock()
	if target == nil {
		return errors.New("证书不存在")
	}
	if target.Source != certSourceManual || !strings.HasPrefix(filepath.Clean(target.Path), filepath.Clean(filepath.Join(s.dataDir, "certs"))) {
		return errors.New("只有手动导入的证书可以删除")
	}
	certPath := target.Path
	keyPath := findKeyPath(certPath)
	_ = os.Remove(certPath)
	if keyPath != "" {
		_ = os.Remove(keyPath)
	}
	return s.Reload(true)
}

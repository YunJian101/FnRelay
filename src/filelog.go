package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	logKindAccess = "access"
	logKindAudit  = "audit"
	logKindSystem = "system"
)

var (
	logFileNameRe = regexp.MustCompile(`^(access|audit|system)-(\d{4}-\d{2}-\d{2})\.jsonl$`)
	logDateRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

type LogStats struct {
	Dir           string `json:"dir"`
	Today         string `json:"today"`
	Enabled       bool   `json:"enabled"`
	Access        bool   `json:"access"`
	Audit         bool   `json:"audit"`
	RetentionDays int    `json:"retention_days"`
	MaxFileMB     int    `json:"max_file_mb"`
	TodayAccess   int64  `json:"today_access_bytes"`
	TodayAudit    int64  `json:"today_audit_bytes"`
	TodaySystem   int64  `json:"today_system_bytes"`
	Dropped       uint64 `json:"dropped"`
	LastError     string `json:"last_error,omitempty"`
}

type LogDateInfo struct {
	Date  string   `json:"date"`
	Kinds []string `json:"kinds"`
	Size  int64    `json:"size"`
}

type FileLogger struct {
	mu        sync.Mutex
	dir       string
	cfg       LogConfig
	files     map[string]*os.File
	dates     map[string]string
	sizes     map[string]int64
	lastError string

	dropped atomic.Uint64

	accessCh chan LogEntry
	done     chan struct{}
	wg       sync.WaitGroup
	closeOne sync.Once
}

func NewFileLogger(dir string, cfg LogConfig) *FileLogger {
	f := &FileLogger{
		dir:      dir,
		cfg:      cfg,
		files:    map[string]*os.File{},
		dates:    map[string]string{},
		sizes:    map[string]int64{},
		accessCh: make(chan LogEntry, 4096),
		done:     make(chan struct{}),
	}
	_ = os.MkdirAll(dir, 0o700)
	f.mu.Lock()
	f.cleanupLocked()
	f.mu.Unlock()
	f.wg.Add(1)
	go f.writer()
	return f
}

func (f *FileLogger) Configure(cfg LogConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	f.lastError = ""
	if !cfg.FileEnabled {
		f.closeFilesLocked()
		return
	}
	_ = os.MkdirAll(f.dir, 0o700)
	f.cleanupLocked()
}

func (f *FileLogger) Write(e LogEntry) {
	f.mu.Lock()
	enabled := f.cfg.FileEnabled
	access := f.cfg.Access
	audit := f.cfg.Audit
	f.mu.Unlock()
	if !enabled {
		return
	}
	switch e.Kind {
	case logKindAccess:
		if !access {
			return
		}
		select {
		case f.accessCh <- e:
		default:
			f.dropped.Add(1)
		}
	case logKindAudit:
		if !audit {
			return
		}
		f.writeEntry(e)
	case logKindSystem:
		f.writeEntry(e)
	}
}

func (f *FileLogger) writer() {
	defer f.wg.Done()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case e := <-f.accessCh:
			f.writeEntry(e)
		case <-ticker.C:
			f.mu.Lock()
			f.cleanupLocked()
			f.mu.Unlock()
		case <-f.done:
			for {
				select {
				case e := <-f.accessCh:
					f.writeEntry(e)
				default:
					return
				}
			}
		}
	}
}

func (f *FileLogger) writeEntry(e LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.cfg.FileEnabled {
		return
	}
	kind := e.Kind
	if kind == logKindAccess && !f.cfg.Access {
		return
	}
	if kind == logKindAudit && !f.cfg.Audit {
		return
	}
	if kind != logKindAccess && kind != logKindAudit && kind != logKindSystem {
		return
	}
	when := e.Time
	if when.IsZero() {
		when = time.Now()
	}
	date := when.Local().Format("2006-01-02")
	file, err := f.fileLocked(kind, date)
	if err != nil {
		f.lastError = err.Error()
		return
	}
	line, err := json.Marshal(e)
	if err != nil {
		f.lastError = err.Error()
		return
	}
	maxBytes := int64(f.cfg.MaxFileMB) * 1024 * 1024
	if maxBytes <= 0 {
		maxBytes = 100 * 1024 * 1024
	}
	if f.sizes[kind]+int64(len(line))+1 > maxBytes {
		f.dropped.Add(1)
		f.lastError = fmt.Sprintf("%s 日志达到单日 %d MB 上限，今天的后续日志已丢弃", kind, f.cfg.MaxFileMB)
		return
	}
	n, err := file.Write(append(line, '\n'))
	if err != nil {
		f.lastError = err.Error()
		return
	}
	f.sizes[kind] += int64(n)
	if kind == logKindAudit {
		_ = file.Sync()
	}
}

func (f *FileLogger) fileLocked(kind, date string) (*os.File, error) {
	if f.dates[kind] == date {
		if file, ok := f.files[kind]; ok {
			return file, nil
		}
	}
	if file, ok := f.files[kind]; ok {
		_ = file.Close()
		delete(f.files, kind)
	}
	path := filepath.Join(f.dir, kind+"-"+date+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	size := int64(0)
	if st, statErr := file.Stat(); statErr == nil {
		size = st.Size()
	}
	f.files[kind] = file
	f.dates[kind] = date
	f.sizes[kind] = size
	f.lastError = ""
	return file, nil
}

func (f *FileLogger) closeFilesLocked() {
	for kind, file := range f.files {
		_ = file.Close()
		delete(f.files, kind)
		delete(f.dates, kind)
		delete(f.sizes, kind)
	}
}

func (f *FileLogger) cleanupLocked() {
	days := f.cfg.RetentionDays
	if days < 1 || days > 7 {
		days = 7
	}
	cutoff := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := logFileNameRe.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		if m[2] < cutoff {
			_ = os.Remove(filepath.Join(f.dir, entry.Name()))
		}
	}
}

func (f *FileLogger) Close() {
	f.closeOne.Do(func() {
		close(f.done)
		f.wg.Wait()
		f.mu.Lock()
		f.cfg.FileEnabled = false
		f.closeFilesLocked()
		f.mu.Unlock()
	})
}

func (f *FileLogger) Stats() LogStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	stats := LogStats{
		Dir:           f.dir,
		Today:         now.Format("2006-01-02"),
		Enabled:       f.cfg.FileEnabled,
		Access:        f.cfg.Access,
		Audit:         f.cfg.Audit,
		RetentionDays: f.cfg.RetentionDays,
		MaxFileMB:     f.cfg.MaxFileMB,
		Dropped:       f.dropped.Load(),
		LastError:     f.lastError,
	}
	today := now.Format("2006-01-02")
	for kind, target := range map[string]*int64{
		logKindAccess: &stats.TodayAccess,
		logKindAudit:  &stats.TodayAudit,
		logKindSystem: &stats.TodaySystem,
	} {
		if st, err := os.Stat(filepath.Join(f.dir, kind+"-"+today+".jsonl")); err == nil {
			*target = st.Size()
		}
	}
	return stats
}

func (f *FileLogger) Dates() []LogDateInfo {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil
	}
	type info struct {
		kinds map[string]bool
		size  int64
	}
	byDate := map[string]*info{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := logFileNameRe.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		item, ok := byDate[m[2]]
		if !ok {
			item = &info{kinds: map[string]bool{}}
			byDate[m[2]] = item
		}
		item.kinds[m[1]] = true
		if st, statErr := entry.Info(); statErr == nil {
			item.size += st.Size()
		}
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	out := make([]LogDateInfo, 0, len(dates))
	for _, date := range dates {
		kinds := make([]string, 0, 3)
		for _, kind := range []string{logKindAccess, logKindAudit, logKindSystem} {
			if byDate[date].kinds[kind] {
				kinds = append(kinds, kind)
			}
		}
		out = append(out, LogDateInfo{Date: date, Kinds: kinds, Size: byDate[date].size})
	}
	return out
}

func (f *FileLogger) DownloadPath(date, kind string) (string, error) {
	if !validLogDate(date) {
		return "", fmt.Errorf("日期格式不正确")
	}
	if kind != logKindAccess && kind != logKindAudit && kind != logKindSystem {
		return "", fmt.Errorf("日志类型不正确")
	}
	path := filepath.Join(f.dir, kind+"-"+date+".jsonl")
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", fmt.Errorf("日志文件不存在")
	}
	return path, nil
}

func (f *FileLogger) Read(date, kind string, limit int) ([]LogEntry, error) {
	if !validLogDate(date) {
		return nil, fmt.Errorf("日期格式不正确")
	}
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	kinds := []string{kind}
	if kind == "" {
		kinds = []string{logKindAccess, logKindAudit, logKindSystem}
	}
	var all []LogEntry
	readAny := false
	for _, k := range kinds {
		if k != logKindAccess && k != logKindAudit && k != logKindSystem {
			return nil, fmt.Errorf("日志类型不正确")
		}
		entries, err := f.readFile(date, k, limit)
		if err != nil {
			continue
		}
		readAny = true
		all = append(all, entries...)
	}
	if !readAny {
		return nil, fmt.Errorf("该日期没有日志文件")
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.After(all[j].Time) })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (f *FileLogger) readFile(date, kind string, limit int) ([]LogEntry, error) {
	path := filepath.Join(f.dir, kind+"-"+date+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if st, statErr := file.Stat(); statErr == nil && st.Size() > 128*1024*1024 {
		return nil, fmt.Errorf("日志文件过大")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	ring := make([]LogEntry, 0, limit)
	for scanner.Scan() {
		var e LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if len(ring) >= limit {
			copy(ring, ring[1:])
			ring[len(ring)-1] = e
		} else {
			ring = append(ring, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}

func validLogDate(date string) bool {
	if !logDateRe.MatchString(date) {
		return false
	}
	_, err := time.Parse("2006-01-02", date)
	return err == nil
}

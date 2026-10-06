package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

type Entry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type Page struct {
	Entries     []Entry   `json:"entries"`
	NextBefore  string    `json:"next_before,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

type Store struct {
	path          string
	retentionDays int
	writer        *lumberjack.Logger
	output        *dailyWriter
}

func New(logPath string, retentionDays int) (*slog.Logger, *Store, error) {
	stdout := slog.NewJSONHandler(os.Stdout, nil)
	if strings.TrimSpace(logPath) == "" {
		return slog.New(stdout), &Store{}, nil
	}
	if retentionDays <= 0 {
		retentionDays = 7
	}
	logPath = filepath.Clean(logPath)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return nil, nil, err
	}
	writer := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    20,
		MaxBackups: 14,
		MaxAge:     retentionDays,
		LocalTime:  false,
		Compress:   false,
	}
	day := time.Now().UTC().Format("2006-01-02")
	if info, err := os.Stat(logPath); err == nil {
		day = info.ModTime().UTC().Format("2006-01-02")
	}
	output := &dailyWriter{sink: writer, day: day, now: time.Now}
	store := &Store{path: logPath, retentionDays: retentionDays, writer: writer, output: output}
	if err := store.cleanup(time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	file := slog.NewJSONHandler(output, nil)
	return slog.New(teeHandler{handlers: []slog.Handler{stdout, file}}), store, nil
}

func (s *Store) Close() error {
	if s == nil || s.writer == nil {
		return nil
	}
	return s.writer.Close()
}

func (s *Store) Query(limit int, before time.Time, minimumLevel string) (Page, error) {
	page := Page{Entries: []Entry{}, GeneratedAt: time.Now().UTC()}
	if s == nil || s.path == "" {
		return page, errors.New("application log file is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	threshold, err := levelValue(minimumLevel)
	if err != nil {
		return page, err
	}
	_ = s.cleanup(page.GeneratedAt)
	files, err := s.files()
	if err != nil {
		return page, err
	}
	cutoff := page.GeneratedAt.Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	for _, path := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		lines := bytes.Split(data, []byte{'\n'})
		for i := len(lines) - 1; i >= 0; i-- {
			line := bytes.TrimSpace(lines[i])
			if len(line) == 0 {
				continue
			}
			entry, parseErr := parseEntry(line)
			if parseErr != nil || entry.Time.Before(cutoff) || (!before.IsZero() && !entry.Time.Before(before)) {
				continue
			}
			value, _ := levelValue(entry.Level)
			if value < threshold {
				continue
			}
			page.Entries = append(page.Entries, entry)
			if len(page.Entries) == limit {
				page.NextBefore = entry.Time.Format(time.RFC3339Nano)
				return page, nil
			}
		}
	}
	return page, nil
}

func (s *Store) files() ([]string, error) {
	dir := filepath.Dir(s.path)
	base := filepath.Base(s.path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type fileInfo struct {
		path string
		mod  time.Time
	}
	var found []fileInfo
	for _, item := range entries {
		if item.IsDir() || !isLogFile(item.Name(), base, stem, ext) {
			continue
		}
		info, infoErr := item.Info()
		if infoErr == nil {
			found = append(found, fileInfo{path: filepath.Join(dir, item.Name()), mod: info.ModTime()})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	out := make([]string, 0, len(found))
	for _, item := range found {
		out = append(out, item.path)
	}
	return out, nil
}

func (s *Store) cleanup(now time.Time) error {
	if s.path == "" || s.retentionDays <= 0 {
		return nil
	}
	dir := filepath.Dir(s.path)
	base := filepath.Base(s.path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	for _, item := range entries {
		if item.IsDir() || item.Name() == base || !isLogFile(item.Name(), base, stem, ext) {
			continue
		}
		info, infoErr := item.Info()
		if infoErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, item.Name()))
		}
	}
	return nil
}

func isLogFile(name, base, stem, ext string) bool {
	return name == base || (strings.HasPrefix(name, stem+"-") && strings.HasSuffix(name, ext))
}

func parseEntry(line []byte) (Entry, error) {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return Entry{}, err
	}
	timeText, _ := raw["time"].(string)
	t, err := time.Parse(time.RFC3339Nano, timeText)
	if err != nil {
		return Entry{}, err
	}
	level, _ := raw["level"].(string)
	message, _ := raw["msg"].(string)
	delete(raw, "time")
	delete(raw, "level")
	delete(raw, "msg")
	return Entry{Time: t, Level: strings.ToUpper(level), Message: message, Fields: raw}, nil
}

func levelValue(level string) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "", "DEBUG":
		return 0, nil
	case "INFO":
		return 1, nil
	case "WARN", "WARNING":
		return 2, nil
	case "ERROR":
		return 3, nil
	default:
		return 0, errors.New("level must be DEBUG, INFO, WARN, or ERROR")
	}
}

type teeHandler struct {
	handlers []slog.Handler
}

type dailyWriter struct {
	mu   sync.Mutex
	sink *lumberjack.Logger
	day  string
	now  func() time.Time
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	day := w.now().UTC().Format("2006-01-02")
	if day != w.day {
		if err := w.sink.Rotate(); err != nil {
			return 0, err
		}
		w.day = day
	}
	return w.sink.Write(p)
}

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	var first error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			if err := handler.Handle(ctx, record.Clone()); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithAttrs(attrs))
	}
	return teeHandler{handlers: handlers}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithGroup(name))
	}
	return teeHandler{handlers: handlers}
}

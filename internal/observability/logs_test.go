package observability

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogQueryAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	logger, store, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	logger.Info("backfill complete", "exchange", "binance", "candles", 42)
	logger.Warn("training skipped", "samples", 10)
	page, err := store.Query(1, time.Time{}, "WARN")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "training skipped" || page.NextBefore == "" {
		t.Fatalf("page=%+v", page)
	}
	old := filepath.Join(filepath.Dir(path), "application-2026-01-01T00-00-00.000.log")
	if err = os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	if err = os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Query(10, time.Time{}, "INFO"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired log still exists: %v", err)
	}
}

func TestTeeHandlerPreservesFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	_, store, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	logger := slog.New(teeHandler{handlers: []slog.Handler{slog.NewJSONHandler(store.writer, nil)}})
	logger.LogAttrs(context.Background(), slog.LevelError, "request failed", slog.String("route", "/market"))
	page, err := store.Query(10, time.Time{}, "ERROR")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Fields["route"] != "/market" {
		t.Fatalf("entries=%+v", page.Entries)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "password") {
		t.Fatal("unexpected password field")
	}
}

func TestDailyRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	_, store, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 6, 23, 59, 0, 0, time.UTC)
	store.output.now = func() time.Time { return now }
	if _, err = store.output.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err = store.output.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	files, err := store.files()
	if err != nil || len(files) < 2 {
		t.Fatalf("daily rotation files=%v error=%v", files, err)
	}
}

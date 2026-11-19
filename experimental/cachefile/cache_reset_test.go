package cachefile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
)

func startTestCache(t *testing.T) *CacheFile {
	t.Helper()
	cache := New(context.Background(), option.CacheFileOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache.db"), StoreFakeIP: true})
	if err := cache.Start(adapter.StartStateInitialize); err != nil {
		t.Fatal(err)
	}
	return cache
}

// Codex review round 2 P1: callers that saw the same broken handle reset it
// once; a later caller must not delete the replacement.
func TestResetDBIfCurrentResetsOncePerBrokenHandle(t *testing.T) {
	cache := startTestCache(t)
	defer cache.Close()
	broken := cache.currentDB()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = cache.resetDBIfCurrent(broken) }()
	}
	wg.Wait()
	replacement := cache.currentDB()
	if replacement == broken {
		t.Fatal("the broken handle was not replaced")
	}
	if err := cache.FakeIPSaveMetadata(&adapter.FakeIPMetadata{}); err != nil {
		t.Fatalf("write to the replacement: %v", err)
	}
	if err := cache.resetDBIfCurrent(broken); err != nil {
		t.Fatal(err)
	}
	if cache.currentDB() != replacement {
		t.Fatal("a stale caller replaced the replacement")
	}
}

func TestResetDBIfCurrentNeverReopensAClosedCache(t *testing.T) {
	cache := startTestCache(t)
	db := cache.currentDB()
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cache.resetDBIfCurrent(db); err != nil {
		t.Fatal(err)
	}
	if cache.currentDB() != db {
		t.Fatal("a closed cache was reopened")
	}
}

// Codex round 3 #3: a transaction that never releases the writer lock (bbolt
// keeps it when a transaction panics during its start) must not hang the
// reset: the handle is abandoned and the session continues on a side file.
func TestResetDBIfCurrentDoesNotHangOnAHeldWriterLock(t *testing.T) {
	old := cacheCloseDeadline
	cacheCloseDeadline = 200 * time.Millisecond
	defer func() { cacheCloseDeadline = old }()
	cache := startTestCache(t)
	broken := cache.currentDB()
	held, err := broken.Begin(true) // holds the writer lock like the leaked one
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cache.resetDBIfCurrent(broken) }()
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reset hung on a held writer lock")
	}
	if err == nil || !strings.Contains(err.Error(), "abandoned") {
		t.Fatalf("want an 'abandoned' error, got %v", err)
	}
	if cache.currentDB() == broken || !strings.HasSuffix(cache.dbPath, recoveredSuffix) {
		t.Fatalf("expected a new handle on the side file, path %q", cache.dbPath)
	}
	if err = cache.FakeIPSaveMetadata(&adapter.FakeIPMetadata{}); err != nil {
		t.Fatalf("write after the reset: %v", err)
	}
	_ = held.Rollback() // lets the abandoned Close finish
	if err = cache.Close(); err != nil {
		t.Fatal(err)
	}
}

// Codex round 3 #4: a failed reopen must not leave a closed handle until
// restart; the next access after the retry interval reopens.
func TestFailedReopenIsRetried(t *testing.T) {
	oldInterval := cacheReopenInterval
	cacheReopenInterval = 0
	defer func() { cacheReopenInterval = oldInterval }()
	cache := startTestCache(t)
	defer cache.Close()
	broken := cache.currentDB()
	// Make the reopen fail: a non-empty directory where the file was.
	if err := os.Remove(cache.path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cache.path, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cache.resetDBIfCurrent(broken); err == nil {
		t.Fatal("the reopen should have failed")
	}
	if !cache.reopenPending {
		t.Fatal("a failed reopen must be retried later")
	}
	if err := os.RemoveAll(cache.path); err != nil {
		t.Fatal(err)
	}
	if err := cache.FakeIPSaveMetadata(&adapter.FakeIPMetadata{}); err != nil {
		t.Fatalf("write after the cause went away: %v", err)
	}
	if cache.reopenPending || cache.currentDB() == broken {
		t.Fatal("the cache did not reopen")
	}
}

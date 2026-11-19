package cachefile

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

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

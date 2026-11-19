package cachefile

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
)

// A cache.db whose freelist page is torn (the state an interrupted write leaves)
// must not kill the process: the old bbolt faulted inside the mmap ("fatal error:
// fault", unrecoverable), which on Android aborted libbox right after the tunnel
// came up (ticket 20278). Start must instead drop the file and open a fresh one.
func TestCacheFileStartSurvivesCorruptedFreelist(t *testing.T) {
	if os.Getenv("CACHEFILE_CORRUPT_CHILD") == "1" {
		runCorruptedStart(t, os.Getenv("CACHEFILE_CORRUPT_PATH"))
		return
	}
	path := filepath.Join(t.TempDir(), "cache.db")
	writeTornFreelist(t, path)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCacheFileStartSurvivesCorruptedFreelist$")
	cmd.Env = append(os.Environ(), "CACHEFILE_CORRUPT_CHILD=1", "CACHEFILE_CORRUPT_PATH="+path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cache file start on a corrupted cache.db killed the process: %v\n%s", err, head(output, 1500))
	}
}

func runCorruptedStart(t *testing.T, path string) {
	cache := New(context.Background(), option.CacheFileOptions{Enabled: true, Path: path, StoreFakeIP: true, StoreRDRC: true})
	if err := cache.Start(adapter.StartStateInitialize); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cache.Close()
	if err := cache.FakeIPSaveMetadata(&adapter.FakeIPMetadata{}); err != nil {
		t.Fatalf("write after recovery: %v", err)
	}
}

func writeTornFreelist(t *testing.T, path string) {
	db, err := bbolt.Open(path, 0o666, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Update(func(tx *bbolt.Tx) error {
		bucket, _ := tx.CreateBucketIfNotExists([]byte("fakeip_address"))
		for i := 0; i < 2000; i++ {
			_ = bucket.Put([]byte(fmt.Sprintf("k%05d", i)), make([]byte, 64))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("fakeip_address"))
		for i := 0; i < 1500; i++ {
			_ = bucket.Delete([]byte(fmt.Sprintf("k%05d", i)))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pageSize := int64(db.Info().PageSize)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	meta := make([]byte, 64)
	for page := int64(0); page < 2; page++ {
		if _, err = file.ReadAt(meta, page*pageSize+16); err != nil {
			t.Fatal(err)
		}
		freelistPage := int64(binary.LittleEndian.Uint64(meta[32:40]))
		header := make([]byte, 16)
		if _, err = file.ReadAt(header, freelistPage*pageSize); err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint16(header[10:12], 0xFFFF)
		if _, err = file.WriteAt(header, freelistPage*pageSize); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 16)
		binary.LittleEndian.PutUint64(body[0:8], 1<<28)
		binary.LittleEndian.PutUint64(body[8:16], 1<<40)
		if _, err = file.WriteAt(body, freelistPage*pageSize+16); err != nil {
			t.Fatal(err)
		}
	}
}

func head(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

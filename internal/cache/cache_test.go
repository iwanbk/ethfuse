package cache

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCache_MemoryGetSet(t *testing.T) {
	c, err := NewCache("")
	if err != nil {
		t.Fatalf("NewCache failed: %v", err)
	}
	defer func() { _ = c.Close() }()

	key := "testkey"
	value := []byte("testvalue")

	// Set with short TTL
	c.Set(key, value, 50*time.Millisecond)

	// Get immediately
	got, ok := c.Get(key)
	if !ok {
		t.Error("Get failed: key not found")
	}
	if string(got) != string(value) {
		t.Errorf("Get returned %q, want %q", got, value)
	}

	// Wait for expiration
	time.Sleep(60 * time.Millisecond)
	_, ok = c.Get(key)
	if ok {
		t.Error("Get succeeded after TTL, want not found")
	}
}

func TestCache_PersistentGetSet(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	c, err := NewCache(dbPath)
	if err != nil {
		t.Fatalf("NewCache failed: %v", err)
	}
	defer func() { _ = c.Close() }()

	bucket := "testbucket"
	key := "testkey"
	value := []byte("testvalue")

	// Set persistent
	err = c.SetPersistent(bucket, key, value)
	if err != nil {
		t.Fatalf("SetPersistent failed: %v", err)
	}

	// Get persistent
	got, ok := c.GetPersistent(bucket, key)
	if !ok {
		t.Error("GetPersistent failed: key not found")
	}
	if string(got) != string(value) {
		t.Errorf("GetPersistent returned %q, want %q", got, value)
	}

	// Test non-existent bucket
	_, ok = c.GetPersistent("nonexistent", key)
	if ok {
		t.Error("GetPersistent succeeded for non-existent bucket")
	}

	// Test non-existent key
	_, ok = c.GetPersistent(bucket, "nonexistent")
	if ok {
		t.Error("GetPersistent succeeded for non-existent key")
	}
}

func TestCache_PersistentSurvivesRestart(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create cache, write, close
	c1, err := NewCache(dbPath)
	if err != nil {
		t.Fatalf("NewCache failed: %v", err)
	}

	bucket := "survive"
	key := "key"
	value := []byte("value")

	err = c1.SetPersistent(bucket, key, value)
	if err != nil {
		t.Fatalf("SetPersistent failed: %v", err)
	}
	_ = c1.Close()

	// Reopen cache, read back
	c2, err := NewCache(dbPath)
	if err != nil {
		t.Fatalf("NewCache failed on reopen: %v", err)
	}
	defer func() { _ = c2.Close() }()

	got, ok := c2.GetPersistent(bucket, key)
	if !ok {
		t.Error("GetPersistent failed after restart: key not found")
	}
	if string(got) != string(value) {
		t.Errorf("GetPersistent after restart returned %q, want %q", got, value)
	}
}

func TestCache_NegativeSetGet(t *testing.T) {
	c, err := NewCache("")
	if err != nil {
		t.Fatalf("NewCache failed: %v", err)
	}
	defer func() { _ = c.Close() }()

	path := "/some/invalid/path"

	// Initially not negative
	if c.IsNegative(path) {
		t.Error("IsNegative true before SetNegative")
	}

	// Set negative
	c.SetNegative(path)

	// Should be negative now
	if !c.IsNegative(path) {
		t.Error("IsNegative false after SetNegative")
	}

	// Verify the underlying cache entry
	val, ok := c.Get("negative:" + path)
	if !ok {
		t.Error("negative entry not found in cache")
	}
	if len(val) != 1 || val[0] != 1 {
		t.Errorf("negative entry value mismatch: got %v", val)
	}
}

func TestCache_NegativeExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow negative cache expiry test in short mode")
	}

	c, err := NewCache("")
	if err != nil {
		t.Fatalf("NewCache failed: %v", err)
	}
	defer func() { _ = c.Close() }()

	path := "/some/invalid/path"
	c.SetNegative(path)

	// Wait for TTL to expire
	time.Sleep(TTLNegative + 10*time.Millisecond)

	// Should no longer be negative
	if c.IsNegative(path) {
		t.Error("IsNegative true after TTL expiration")
	}
}

func TestCache_NoPersistentStorage(t *testing.T) {
	c, err := NewCache("")
	if err != nil {
		t.Fatalf("NewCache failed: %v", err)
	}
	defer func() { _ = c.Close() }()

	// GetPersistent should fail
	_, ok := c.GetPersistent("bucket", "key")
	if ok {
		t.Error("GetPersistent succeeded when persistent storage disabled")
	}

	// SetPersistent should error
	err = c.SetPersistent("bucket", "key", []byte("value"))
	if err == nil {
		t.Error("SetPersistent expected error when persistent storage disabled")
	}
}

func TestCache_Constants(t *testing.T) {
	// Ensure constants are positive durations
	if TTLChainLatestBlock <= 0 {
		t.Error("TTLChainLatestBlock must be positive")
	}
	if TTLBlockLatest <= 0 {
		t.Error("TTLBlockLatest must be positive")
	}
	if TTLChainGas <= 0 {
		t.Error("TTLChainGas must be positive")
	}
	if TTLAccount <= 0 {
		t.Error("TTLAccount must be positive")
	}
	if TTLNegative <= 0 {
		t.Error("TTLNegative must be positive")
	}
	if TTLENS <= 0 {
		t.Error("TTLENS must be positive")
	}
}

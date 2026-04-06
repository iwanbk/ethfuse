package cache

import (
	"errors"
	"fmt"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"go.etcd.io/bbolt"
)

// TTL constants for volatile data
const (
	TTLChainLatestBlock = 10 * time.Second
	TTLBlockLatest      = 12 * time.Second
	TTLChainGas         = 5 * time.Second
	TTLAccount          = 30 * time.Second
	TTLNegative         = 30 * time.Second
	TTLENS              = 300 * time.Second
)

// Cache implements a two-tier caching layer with ttlcache for volatile data
// and bbolt for finalized/immutable data that survives restarts.
type Cache struct {
	mem *ttlcache.Cache[string, []byte]
	db  *bbolt.DB
}

// NewCache creates a new Cache instance.
// dbPath is the file path for the bbolt database.
// If dbPath is empty, the persistent layer is disabled.
func NewCache(dbPath string) (*Cache, error) {
	c := &Cache{}

	// Initialize memory cache with default settings
	c.mem = ttlcache.New(
		ttlcache.WithTTL[string, []byte](time.Minute), // default TTL, overridden per key
	)

	// Start automatic cleanup goroutine
	go c.mem.Start()

	// Initialize persistent storage if path provided
	if dbPath != "" {
		db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 1 * time.Second})
		if err != nil {
			c.mem.Stop()
			return nil, fmt.Errorf("failed to open bbolt database: %w", err)
		}
		c.db = db
	}

	return c, nil
}

// Close releases resources used by the cache.
func (c *Cache) Close() error {
	c.mem.Stop()
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// Get retrieves a value from the memory cache.
func (c *Cache) Get(key string) ([]byte, bool) {
	item := c.mem.Get(key)
	if item == nil {
		return nil, false
	}
	return item.Value(), true
}

// Set stores a value in the memory cache with the given TTL.
func (c *Cache) Set(key string, val []byte, ttl time.Duration) {
	c.mem.Set(key, val, ttl)
}

// GetPersistent retrieves a value from the persistent storage.
// bucket is the bbolt bucket name, key is the key within that bucket.
func (c *Cache) GetPersistent(bucket, key string) ([]byte, bool) {
	if c.db == nil {
		return nil, false
	}

	var value []byte
	err := c.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return errors.New("bucket not found")
		}
		v := b.Get([]byte(key))
		if v == nil {
			return errors.New("key not found")
		}
		value = make([]byte, len(v))
		copy(value, v)
		return nil
	})
	if err != nil {
		return nil, false
	}
	return value, true
}

// SetPersistent stores a value in the persistent storage.
func (c *Cache) SetPersistent(bucket, key string, val []byte) error {
	if c.db == nil {
		return errors.New("persistent storage not enabled")
	}

	return c.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), val)
	})
}

// SetNegative marks a path as invalid (negative caching) with TTLNegative.
func (c *Cache) SetNegative(path string) {
	c.Set("negative:"+path, []byte{1}, TTLNegative)
}

// IsNegative checks if a path is marked as invalid.
func (c *Cache) IsNegative(path string) bool {
	_, ok := c.Get("negative:" + path)
	return ok
}

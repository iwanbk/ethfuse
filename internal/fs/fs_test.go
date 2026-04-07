package fs

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

// mockRPCClient is a mock implementation of RPCClient for testing.
type mockRPCClient struct{}

var _ rpc.RPCClient = (*mockRPCClient)(nil)

func (m *mockRPCClient) Close() {}

func (m *mockRPCClient) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	return nil, nil
}

func (m *mockRPCClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error) {
	return nil, nil
}

func (m *mockRPCClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	return nil, nil
}

func (m *mockRPCClient) BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
	return big.NewInt(0), nil
}

func (m *mockRPCClient) NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error) {
	return 0, nil
}

func (m *mockRPCClient) CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error) {
	return nil, nil
}

func (m *mockRPCClient) ChainID(ctx context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}

func (m *mockRPCClient) FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error) {
	return nil, nil
}

func (m *mockRPCClient) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	return nil, nil
}

// newTestCache creates a cache with empty db path (memory only) for testing.
func newTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.NewCache("")
	if err != nil {
		t.Fatalf("failed to create test cache: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Logf("failed to close test cache: %v", err)
		}
	})
	return c
}

func TestRootNode_Getattr(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, newTestCache(t))

	var out fuse.AttrOut
	errno := root.Getattr(context.TODO(), nil, &out)
	if errno != 0 {
		t.Errorf("Getattr() errno = %v, want 0", errno)
	}

	if out.Mode&syscall.S_IFDIR == 0 {
		t.Errorf("Getattr() Mode = %o, should be a directory", out.Mode)
	}

	wantPerm := uint32(0o755)
	if out.Mode&wantPerm != wantPerm {
		t.Errorf("Getattr() Mode permissions = %o, want %o", out.Mode&0o777, wantPerm)
	}
}

func TestRootNode_Readdir(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, newTestCache(t))

	stream, errno := root.Readdir(context.TODO())
	if errno != 0 {
		t.Errorf("Readdir() errno = %v, want 0", errno)
	}

	if stream == nil {
		t.Fatal("Readdir() stream = nil, want non-nil")
	}

	// Expect exactly five entries: "blocks", "txs", "accounts", "chain", "ens"
	var entries []fuse.DirEntry
	for stream.HasNext() {
		entry, errno := stream.Next()
		if errno != 0 {
			t.Fatalf("Next errno = %v", errno)
		}
		entries = append(entries, entry)
	}
	if len(entries) != 5 {
		t.Errorf("Readdir() returned %d entries, want 5", len(entries))
	}
	// Check all four entries are directories and have expected names
	expectedNames := map[string]bool{
		"blocks":   false,
		"txs":      false,
		"accounts": false,
		"chain":    false,
		"ens":      false,
	}
	for _, entry := range entries {
		if _, ok := expectedNames[entry.Name]; !ok {
			t.Errorf("Readdir() unexpected entry name %q", entry.Name)
		} else {
			expectedNames[entry.Name] = true
		}
		if entry.Mode&syscall.S_IFDIR == 0 {
			t.Errorf("Readdir() entry %q mode = %o, should be a directory", entry.Name, entry.Mode)
		}
	}
	for name, found := range expectedNames {
		if !found {
			t.Errorf("Readdir() missing entry %q", name)
		}
	}

	stream.Close()
}

func TestRootNode_Open(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, newTestCache(t))

	fh, fuseFlags, errno := root.Open(context.TODO(), 0)
	if errno != syscall.EISDIR {
		t.Errorf("Open() errno = %v, want %v", errno, syscall.EISDIR)
	}
	if fh != nil {
		t.Errorf("Open() fh = %v, want nil", fh)
	}
	if fuseFlags != 0 {
		t.Errorf("Open() fuseFlags = %d, want 0", fuseFlags)
	}
}

func TestRootNode_Getxattr(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, newTestCache(t))

	size, errno := root.Getxattr(context.TODO(), "user.test", nil)
	if errno != syscall.ENODATA {
		t.Errorf("Getxattr() errno = %v, want %v", errno, syscall.ENODATA)
	}
	if size != 0 {
		t.Errorf("Getxattr() size = %d, want 0", size)
	}
}

func TestRootNode_Listxattr(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, newTestCache(t))

	size, errno := root.Listxattr(context.TODO(), nil)
	if errno != syscall.ENODATA {
		t.Errorf("Listxattr() errno = %v, want %v", errno, syscall.ENODATA)
	}
	if size != 0 {
		t.Errorf("Listxattr() size = %d, want 0", size)
	}
}

func TestRootNode_Setxattr(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, newTestCache(t))

	errno := root.Setxattr(context.TODO(), "user.test", []byte("data"), 0)
	if errno != syscall.EROFS {
		t.Errorf("Setxattr() errno = %v, want %v", errno, syscall.EROFS)
	}
}

// TestMountUnmount tests that mounting and immediately unmounting does not crash.
func TestMountUnmount(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("FUSE mount test requires Linux")
	}
	// Create a temporary directory for the mount point
	tmpDir := t.TempDir()
	mountpoint := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}

	root := NewRootNode(&mockRPCClient{}, newTestCache(t))

	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "ethfuse",
			Name:   "ethfuse",
		},
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		t.Fatalf("Failed to mount: %v", err)
	}

	// Give the mount a moment to initialize
	time.Sleep(100 * time.Millisecond)

	// Immediately unmount
	if err := server.Unmount(); err != nil {
		t.Fatalf("Failed to unmount: %v", err)
	}

	// Wait should return without error/crash
	// Note: After Unmount, Wait may return an error, but should not crash
	server.Wait()
}

package fs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// mockRPCClient is a mock implementation of RPCClient for testing.
type mockRPCClient struct{}

func (m *mockRPCClient) Close() {}

// mockCacher is a mock implementation of Cacher for testing.
type mockCacher struct{}

func (m *mockCacher) Close() error { return nil }

func TestRootNode_Getattr(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, &mockCacher{})

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
	root := NewRootNode(&mockRPCClient{}, &mockCacher{})

	stream, errno := root.Readdir(context.TODO())
	if errno != 0 {
		t.Errorf("Readdir() errno = %v, want 0", errno)
	}

	if stream == nil {
		t.Fatal("Readdir() stream = nil, want non-nil")
	}

	// Stream should be empty
	if stream.HasNext() {
		t.Error("Readdir() stream.HasNext() = true, want false (empty directory)")
	}

	stream.Close()
}

func TestRootNode_Open(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, &mockCacher{})

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
	root := NewRootNode(&mockRPCClient{}, &mockCacher{})

	size, errno := root.Getxattr(context.TODO(), "user.test", nil)
	if errno != syscall.ENODATA {
		t.Errorf("Getxattr() errno = %v, want %v", errno, syscall.ENODATA)
	}
	if size != 0 {
		t.Errorf("Getxattr() size = %d, want 0", size)
	}
}

func TestRootNode_Listxattr(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, &mockCacher{})

	size, errno := root.Listxattr(context.TODO(), nil)
	if errno != syscall.ENODATA {
		t.Errorf("Listxattr() errno = %v, want %v", errno, syscall.ENODATA)
	}
	if size != 0 {
		t.Errorf("Listxattr() size = %d, want 0", size)
	}
}

func TestRootNode_Setxattr(t *testing.T) {
	root := NewRootNode(&mockRPCClient{}, &mockCacher{})

	errno := root.Setxattr(context.TODO(), "user.test", []byte("data"), 0)
	if errno != syscall.EROFS {
		t.Errorf("Setxattr() errno = %v, want %v", errno, syscall.EROFS)
	}
}

// TestMountUnmount tests that mounting and immediately unmounting does not crash.
func TestMountUnmount(t *testing.T) {
	// Create a temporary directory for the mount point
	tmpDir := t.TempDir()
	mountpoint := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}

	root := NewRootNode(&mockRPCClient{}, &mockCacher{})

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

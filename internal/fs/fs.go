package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// RootNode is the root directory of the ethfuse filesystem.
// It's an empty directory for now (skeleton implementation).
type RootNode struct {
	fs.Inode
	rpc   RPCClient
	cache Cacher
}

// RPCClient defines the interface for RPC operations.
type RPCClient interface {
	Close()
}

// Cacher defines the interface for cache operations.
type Cacher interface {
	Close() error
}

var _ fs.NodeOpener = (*RootNode)(nil)
var _ fs.NodeReaddirer = (*RootNode)(nil)
var _ fs.NodeGetattrer = (*RootNode)(nil)
var _ fs.NodeGetxattrer = (*RootNode)(nil)
var _ fs.NodeListxattrer = (*RootNode)(nil)
var _ fs.NodeSetxattrer = (*RootNode)(nil)

// NewRootNode creates a new RootNode.
func NewRootNode(rpc RPCClient, cache Cacher) *RootNode {
	return &RootNode{
		rpc:   rpc,
		cache: cache,
	}
}

// Open handles FUSE_OPEN requests.
func (r *RootNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	return nil, 0, syscall.EISDIR
}

// Readdir handles FUSE_READDIR requests.
// Returns an empty directory listing for now.
func (r *RootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream([]fuse.DirEntry{}), 0
}

// Getattr handles FUSE_GETATTR requests.
func (r *RootNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Getxattr handles FUSE_GETXATTR requests.
func (r *RootNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	return 0, syscall.ENODATA
}

// Listxattr handles FUSE_LISTXATTR requests.
func (r *RootNode) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	return 0, syscall.ENODATA
}

// Setxattr handles FUSE_SETXATTR requests.
func (r *RootNode) Setxattr(ctx context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	return syscall.EROFS
}

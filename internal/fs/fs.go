package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

// RootNode is the root directory of the ethfuse filesystem.
// It's an empty directory for now (skeleton implementation).
// Future nodes (BlocksDirNode, AccountsDirNode, etc.) will be attached here.
type RootNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeLookuper = (*RootNode)(nil)
var _ fs.NodeOpener = (*RootNode)(nil)
var _ fs.NodeReaddirer = (*RootNode)(nil)
var _ fs.NodeGetattrer = (*RootNode)(nil)
var _ fs.NodeGetxattrer = (*RootNode)(nil)
var _ fs.NodeListxattrer = (*RootNode)(nil)
var _ fs.NodeSetxattrer = (*RootNode)(nil)

// NewRootNode creates a new RootNode.
func NewRootNode(rpc rpc.RPCClient, cache *cache.Cache) *RootNode {
	return &RootNode{
		rpc:   rpc,
		cache: cache,
	}
}

// StartBackgroundRefresh starts background goroutines for refreshing volatile data.
// The goroutines stop when the context is cancelled (e.g., on shutdown).
// This should be called once at startup after creating the RootNode.
func StartBackgroundRefresh(ctx context.Context, rpc rpc.RPCClient, c *cache.Cache) {
	// Start background refresh for latest_block (10s interval)
	startBackgroundRefresh(ctx, c, rpc, cache.TTLChainLatestBlock)
}

// Lookup implements NodeLookuper.
func (r *RootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "blocks":
		child := NewBlocksDirNode(r.rpc, r.cache)
		ch := r.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks"),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	case "txs":
		child := NewRootTxsDirNode(r.rpc, r.cache)
		ch := r.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/txs"),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	case "accounts":
		child := NewAccountsDirNode(r.rpc, r.cache)
		ch := r.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/accounts"),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	case "chain":
		child := NewChainNode(r.rpc, r.cache)
		ch := r.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/chain"),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	}
	return nil, syscall.ENOENT
}

// Open handles FUSE_OPEN requests.
func (r *RootNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	return nil, 0, syscall.EISDIR
}

// Readdir handles FUSE_READDIR requests.
// Returns directory entries for root: blocks, txs, accounts, chain.
func (r *RootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: "blocks",
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/blocks"),
		},
		{
			Name: "txs",
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/txs"),
		},
		{
			Name: "accounts",
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/accounts"),
		},
		{
			Name: "chain",
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/chain"),
		},
	}
	return fs.NewListDirStream(entries), 0
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

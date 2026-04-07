package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

const (
	cacheKeyENSPrefix = "ens:"
	ensRegistryAddr   = "0x00000000000C2E074eC69A0dFb2997BA6C7d2e1e"
)

var (
	// registryABI contains only the resolver(bytes32) method.
	registryABI, _ = abi.JSON(strings.NewReader(`[{"constant":true,"inputs":[{"name":"node","type":"bytes32"}],"name":"resolver","outputs":[{"name":"","type":"address"}],"type":"function"}]`))
	// resolverABI contains only the addr(bytes32) method.
	resolverABI, _ = abi.JSON(strings.NewReader(`[{"constant":true,"inputs":[{"name":"node","type":"bytes32"}],"name":"addr","outputs":[{"name":"","type":"address"}],"type":"function"}]`))
)

// namehash computes the ENS namehash as defined in EIP-137.
func namehash(name string) common.Hash {
	var node [32]byte
	if name != "" {
		labels := strings.Split(name, ".")
		for i := len(labels) - 1; i >= 0; i-- {
			labelHash := crypto.Keccak256([]byte(labels[i]))
			hash := crypto.Keccak256(node[:], labelHash)
			copy(node[:], hash)
		}
	}
	return common.BytesToHash(node[:])
}

// ENSDirNode represents the /ens directory (ghost).
// Readdir returns empty, but Lookup resolves ENS names ending with .eth.
type ENSDirNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeLookuper = (*ENSDirNode)(nil)
var _ fs.NodeReaddirer = (*ENSDirNode)(nil)
var _ fs.NodeGetattrer = (*ENSDirNode)(nil)

// NewENSDirNode creates a new ENSDirNode.
func NewENSDirNode(rpc rpc.RPCClient, cache *cache.Cache) *ENSDirNode {
	return &ENSDirNode{
		rpc:   rpc,
		cache: cache,
	}
}

// Getattr implements NodeGetattrer for ENSDirNode.
func (e *ENSDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for ENSDirNode.
// Ghost directory: returns empty list.
func (e *ENSDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream([]fuse.DirEntry{}), 0
}

// Lookup implements NodeLookuper for ENSDirNode.
// Validates name ends with .eth, resolves via ENS registry.
// Returns ENSNode (symlink) if resolution succeeds.
func (e *ENSDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate .eth suffix
	if !strings.HasSuffix(name, ".eth") {
		return nil, syscall.EINVAL
	}
	// Resolve name to address
	addr, err := ResolveENS(ctx, name, e.rpc, e.cache)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		return nil, syscall.EIO
	}
	child := NewENSNode(name, addr, e.rpc, e.cache)
	ch := e.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/ens/" + name),
		Mode: fuse.S_IFLNK,
	})
	out.Mode = fuse.S_IFLNK | 0o777
	return ch, 0
}

// ENSNode represents a symlink from /ens/[name.eth] to /accounts/[address].
type ENSNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
	name  string
	addr  common.Address
}

var _ fs.NodeReadlinker = (*ENSNode)(nil)
var _ fs.NodeGetattrer = (*ENSNode)(nil)

// NewENSNode creates a new ENSNode.
func NewENSNode(name string, addr common.Address, rpc rpc.RPCClient, cache *cache.Cache) *ENSNode {
	return &ENSNode{
		name:  name,
		addr:  addr,
		rpc:   rpc,
		cache: cache,
	}
}

// Getattr implements NodeGetattrer for ENSNode.
func (en *ENSNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFLNK | 0o777
	return 0
}

// Readlink implements NodeReadlinker for ENSNode.
// Returns symlink target path "../../accounts/0xaddr/".
func (en *ENSNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	target := "../../accounts/" + strings.ToLower(en.addr.Hex()) + "/"
	return []byte(target), 0
}

// ResolveENS resolves an ENS name to an Ethereum address.
// Uses cache with TTLENS.
func ResolveENS(ctx context.Context, name string, rpc rpc.RPCClient, c *cache.Cache) (common.Address, error) {
	key := cacheKeyENSPrefix + strings.ToLower(name)
	// Check negative cache first
	if c.IsNegative(key) {
		return common.Address{}, ethereum.NotFound
	}
	// Try memory cache
	if data, ok := c.Get(key); ok && len(data) == 20 {
		return common.BytesToAddress(data), nil
	}
	// Compute namehash
	node := namehash(name)
	// Call ENS registry: resolver(bytes32)
	registry := common.HexToAddress(ensRegistryAddr)
	callData, err := registryABI.Pack("resolver", node)
	if err != nil {
		return common.Address{}, err
	}
	result, err := rpc.CallContract(ctx, ethereum.CallMsg{
		To:   &registry,
		Data: callData,
	}, nil)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			c.SetNegative(key)
		}
		return common.Address{}, err
	}
	var resolverAddr common.Address
	if err = registryABI.UnpackIntoInterface(&resolverAddr, "resolver", result); err != nil {
		return common.Address{}, err
	}
	if resolverAddr == (common.Address{}) {
		// No resolver set
		c.SetNegative(key)
		return common.Address{}, ethereum.NotFound
	}
	// Call resolver: addr(bytes32)
	callData, err = resolverABI.Pack("addr", node)
	if err != nil {
		return common.Address{}, err
	}
	result, err = rpc.CallContract(ctx, ethereum.CallMsg{
		To:   &resolverAddr,
		Data: callData,
	}, nil)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			c.SetNegative(key)
		}
		return common.Address{}, err
	}
	var addr common.Address
	if err = resolverABI.UnpackIntoInterface(&addr, "addr", result); err != nil {
		return common.Address{}, err
	}
	if addr == (common.Address{}) {
		// Resolver returns zero address
		c.SetNegative(key)
		return common.Address{}, ethereum.NotFound
	}
	// Cache positive result
	c.Set(key, addr.Bytes(), cache.TTLENS)
	return addr, nil
}

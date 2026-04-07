package fs

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"syscall"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

const (
	cacheKeyAccountBalancePrefix = "acc:bal:"
	cacheKeyAccountNoncePrefix   = "acc:nonce:"
	cacheKeyAccountCodePrefix    = "acc:code:"
	fileNameBalance              = "balance"
	fileNameNonce                = "nonce"
	fileNameCode                 = "code"
)

// AccountsDirNode represents the /accounts directory (ghost).
// Readdir returns empty, but Lookup resolves account addresses.
type AccountsDirNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeLookuper = (*AccountsDirNode)(nil)
var _ fs.NodeReaddirer = (*AccountsDirNode)(nil)
var _ fs.NodeGetattrer = (*AccountsDirNode)(nil)

// NewAccountsDirNode creates a new AccountsDirNode.
func NewAccountsDirNode(rpc rpc.RPCClient, cache *cache.Cache) *AccountsDirNode {
	return &AccountsDirNode{
		rpc:   rpc,
		cache: cache,
	}
}

// Getattr implements NodeGetattrer for AccountsDirNode.
func (adn *AccountsDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for AccountsDirNode.
// Ghost directory: returns empty list.
func (adn *AccountsDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream([]fuse.DirEntry{}), 0
}

// Lookup implements NodeLookuper for AccountsDirNode.
// Validates address format, returns AccountNode if valid, EINVAL if malformed.
func (adn *AccountsDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate address format using common.IsHexAddress
	if !common.IsHexAddress(name) {
		// Invalid address format per architecture error mapping
		return nil, syscall.EINVAL
	}
	addr := common.HexToAddress(name)
	child := NewAccountNode(addr, adn.rpc, adn.cache)
	ch := adn.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/accounts/" + name),
		Mode: fuse.S_IFDIR,
	})
	out.Mode = fuse.S_IFDIR | 0o755
	return ch, 0
}

// AccountNode represents an account directory at /accounts/[address].
type AccountNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
	addr  common.Address
}

var _ fs.NodeLookuper = (*AccountNode)(nil)
var _ fs.NodeReaddirer = (*AccountNode)(nil)
var _ fs.NodeGetattrer = (*AccountNode)(nil)

// NewAccountNode creates a new AccountNode.
func NewAccountNode(addr common.Address, rpc rpc.RPCClient, cache *cache.Cache) *AccountNode {
	return &AccountNode{
		addr:  addr,
		rpc:   rpc,
		cache: cache,
	}
}

// Getattr implements NodeGetattrer for AccountNode.
func (an *AccountNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for AccountNode.
// Returns directory entries for the account: balance, nonce, code.
func (an *AccountNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: fileNameBalance,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + an.addr.Hex() + "/" + fileNameBalance),
		},
		{
			Name: fileNameNonce,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + an.addr.Hex() + "/" + fileNameNonce),
		},
		{
			Name: fileNameCode,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + an.addr.Hex() + "/" + fileNameCode),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Lookup implements NodeLookuper for AccountNode.
// Returns child file nodes: balance, nonce, code.
func (an *AccountNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var fileType string
	switch name {
	case fileNameBalance:
		fileType = fileNameBalance
	case fileNameNonce:
		fileType = fileNameNonce
	case fileNameCode:
		fileType = fileNameCode
	default:
		return nil, syscall.ENOENT
	}
	child := NewAccountFileNode(an.addr, fileType, an.rpc, an.cache)
	ch := an.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/accounts/" + an.addr.Hex() + "/" + name),
		Mode: fuse.S_IFREG,
	})
	out.Mode = fuse.S_IFREG | 0o644
	out.Size = 0 // unknown size
	return ch, 0
}

// AccountFileNode represents a file inside an account directory.
type AccountFileNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	fileType string // one of fileNameBalance, fileNameNonce, fileNameCode
	addr     common.Address
}

var _ fs.NodeReader = (*AccountFileNode)(nil)
var _ fs.NodeGetattrer = (*AccountFileNode)(nil)

// NewAccountFileNode creates a new AccountFileNode.
func NewAccountFileNode(addr common.Address, fileType string, rpc rpc.RPCClient, cache *cache.Cache) *AccountFileNode {
	return &AccountFileNode{
		addr:     addr,
		fileType: fileType,
		rpc:      rpc,
		cache:    cache,
	}
}

// Getattr implements NodeGetattrer for AccountFileNode.
func (afn *AccountFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFREG | 0o644
	out.Size = 0
	return 0
}

// Read implements NodeReader for AccountFileNode.
func (afn *AccountFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content, err := afn.fileContent(ctx)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		return nil, syscall.EIO
	}
	end := int64(len(content))
	if off >= end {
		return fuse.ReadResultData(nil), 0
	}
	data := content[off:]
	if int64(len(data)) > int64(len(dest)) {
		data = data[:len(dest)]
	}
	return fuse.ReadResultData(data), 0
}

// fileContent returns the file content as bytes based on fileType.
func (afn *AccountFileNode) fileContent(ctx context.Context) ([]byte, error) {
	switch afn.fileType {
	case fileNameBalance:
		return afn.balanceContent(ctx)
	case fileNameNonce:
		return afn.nonceContent(ctx)
	case fileNameCode:
		return afn.codeContent(ctx)
	default:
		return nil, errors.New("unknown file type")
	}
}

// balanceContent returns the balance as a decimal string terminated with \n.
func (afn *AccountFileNode) balanceContent(ctx context.Context) ([]byte, error) {
	key := cacheKeyAccountBalancePrefix + strings.ToLower(afn.addr.Hex())

	// Try cache first
	if data, ok := afn.cache.Get(key); ok {
		return data, nil
	}

	// Fetch from RPC (use nil block for latest)
	balance, err := afn.rpc.BalanceAt(ctx, afn.addr, nil)
	if err != nil {
		return nil, err
	}

	// Format as decimal string with newline
	content := []byte(balance.String() + "\n")

	// Store in cache with TTL
	afn.cache.Set(key, content, cache.TTLAccount)

	return content, nil
}

// nonceContent returns the nonce as a decimal string terminated with \n.
func (afn *AccountFileNode) nonceContent(ctx context.Context) ([]byte, error) {
	key := cacheKeyAccountNoncePrefix + strings.ToLower(afn.addr.Hex())

	// Try cache first
	if data, ok := afn.cache.Get(key); ok {
		return data, nil
	}

	// Fetch from RPC (use nil block for latest)
	nonce, err := afn.rpc.NonceAt(ctx, afn.addr, nil)
	if err != nil {
		return nil, err
	}

	// Format as decimal string with newline
	content := []byte(strconv.FormatUint(nonce, 10) + "\n")

	// Store in cache with TTL
	afn.cache.Set(key, content, cache.TTLAccount)

	return content, nil
}

// codeContent returns the code as hex string terminated with \n.
// For EOA, returns "0x\n"; for contracts, returns hex bytecode.
func (afn *AccountFileNode) codeContent(ctx context.Context) ([]byte, error) {
	key := cacheKeyAccountCodePrefix + strings.ToLower(afn.addr.Hex())

	// Try cache first
	if data, ok := afn.cache.Get(key); ok {
		return data, nil
	}

	// Fetch from RPC (use nil block for latest)
	code, err := afn.rpc.CodeAt(ctx, afn.addr, nil)
	if err != nil {
		return nil, err
	}

	// Format as hex string with newline
	// common.Bytes2Hex returns hex without 0x prefix, so we add it
	var content []byte
	if len(code) == 0 {
		content = []byte("0x\n")
	} else {
		content = []byte("0x" + common.Bytes2Hex(code) + "\n")
	}

	// Store in cache with TTL
	afn.cache.Set(key, content, cache.TTLAccount)

	return content, nil
}

// fetchAccountBalance fetches account balance with caching.
// Exported for testing purposes.
func fetchAccountBalance(ctx context.Context, addr common.Address, rpc rpc.RPCClient, c *cache.Cache) (*big.Int, error) {
	key := cacheKeyAccountBalancePrefix + strings.ToLower(addr.Hex())

	// Try cache first
	if data, ok := c.Get(key); ok {
		// Parse the cached value (decimal string without newline)
		val := strings.TrimSuffix(string(data), "\n")
		balance, ok := new(big.Int).SetString(val, 10)
		if ok {
			return balance, nil
		}
	}

	// Fetch from RPC
	balance, err := rpc.BalanceAt(ctx, addr, nil)
	if err != nil {
		return nil, err
	}

	// Cache the result
	content := []byte(balance.String() + "\n")
	c.Set(key, content, cache.TTLAccount)

	return balance, nil
}

// fetchAccountNonce fetches account nonce with caching.
// Exported for testing purposes.
func fetchAccountNonce(ctx context.Context, addr common.Address, rpc rpc.RPCClient, c *cache.Cache) (uint64, error) {
	key := cacheKeyAccountNoncePrefix + strings.ToLower(addr.Hex())

	// Try cache first
	if data, ok := c.Get(key); ok {
		// Parse the cached value (decimal string without newline)
		val := strings.TrimSuffix(string(data), "\n")
		nonce, err := strconv.ParseUint(val, 10, 64)
		if err == nil {
			return nonce, nil
		}
	}

	// Fetch from RPC
	nonce, err := rpc.NonceAt(ctx, addr, nil)
	if err != nil {
		return 0, err
	}

	// Cache the result
	content := []byte(strconv.FormatUint(nonce, 10) + "\n")
	c.Set(key, content, cache.TTLAccount)

	return nonce, nil
}

// fetchAccountCode fetches account code with caching.
// Exported for testing purposes.
func fetchAccountCode(ctx context.Context, addr common.Address, rpc rpc.RPCClient, c *cache.Cache) ([]byte, error) {
	key := cacheKeyAccountCodePrefix + strings.ToLower(addr.Hex())

	// Try cache first
	if data, ok := c.Get(key); ok {
		// Return the code bytes (strip "0x" prefix and newline)
		val := strings.TrimSuffix(string(data), "\n")
		if val == "0x" {
			return []byte{}, nil
		}
		return common.FromHex(val), nil
	}

	// Fetch from RPC
	code, err := rpc.CodeAt(ctx, addr, nil)
	if err != nil {
		return nil, err
	}

	// Cache the result
	var content []byte
	if len(code) == 0 {
		content = []byte("0x\n")
	} else {
		content = []byte("0x" + common.Bytes2Hex(code) + "\n")
	}
	c.Set(key, content, cache.TTLAccount)

	return code, nil
}

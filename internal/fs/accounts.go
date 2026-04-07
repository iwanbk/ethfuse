package fs

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"syscall"

	"github.com/ethereum/go-ethereum/common"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

const (
	cacheKeyAccountBalancePrefix = "account:balance:"
	cacheKeyAccountNoncePrefix   = "account:nonce:"
	cacheKeyAccountCodePrefix    = "account:code:"
	fileNameBalance              = "balance"
	fileNameNonce                = "nonce"
	fileNameCode                 = "code"
)

// AccountsDirNode represents the /accounts directory (ghost).
// Readdir returns empty, but Lookup resolves valid Ethereum addresses.
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
func (acdn *AccountsDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for AccountsDirNode.
// Ghost directory: returns empty list.
func (acdn *AccountsDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream([]fuse.DirEntry{}), 0
}

// Lookup implements NodeLookuper for AccountsDirNode.
// Validates address format via common.IsHexAddress, returns EINVAL if malformed.
// Returns AccountNode for valid addresses.
func (acdn *AccountsDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate address format
	if !common.IsHexAddress(name) {
		return nil, syscall.EINVAL
	}
	addr := common.HexToAddress(name)

	// Create AccountNode
	child := NewAccountNode(addr, acdn.rpc, acdn.cache)
	ch := acdn.NewPersistentInode(ctx, child, fs.StableAttr{
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
// Returns directory entries for balance, nonce, code.
func (an *AccountNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	addrHex := an.addr.Hex()
	entries := []fuse.DirEntry{
		{
			Name: fileNameBalance,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + addrHex + "/" + fileNameBalance),
		},
		{
			Name: fileNameNonce,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + addrHex + "/" + fileNameNonce),
		},
		{
			Name: fileNameCode,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + addrHex + "/" + fileNameCode),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Lookup implements NodeLookuper for AccountNode.
// Returns AccountFileNode for balance, nonce, or code.
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

// fileContent returns the file content based on fileType.
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

// balanceContent returns the account balance as a decimal string terminated with newline.
func (afn *AccountFileNode) balanceContent(ctx context.Context) ([]byte, error) {
	key := cacheKeyAccountBalancePrefix + afn.addr.Hex()
	// Check cache first
	if data, ok := afn.cache.Get(key); ok {
		return data, nil
	}
	// Fetch from RPC
	balance, err := afn.rpc.BalanceAt(ctx, afn.addr, nil)
	if err != nil {
		return nil, err
	}
	// Convert to decimal string with newline
	content := []byte(balance.String() + "\n")
	// Cache with TTLAccount (30s)
	afn.cache.Set(key, content, cache.TTLAccount)
	return content, nil
}

// nonceContent returns the account nonce as a decimal string terminated with newline.
func (afn *AccountFileNode) nonceContent(ctx context.Context) ([]byte, error) {
	key := cacheKeyAccountNoncePrefix + afn.addr.Hex()
	// Check cache first
	if data, ok := afn.cache.Get(key); ok {
		return data, nil
	}
	// Fetch from RPC
	nonce, err := afn.rpc.NonceAt(ctx, afn.addr, nil)
	if err != nil {
		return nil, err
	}
	// Convert to decimal string with newline
	content := []byte(strconv.FormatUint(nonce, 10) + "\n")
	// Cache with TTLAccount (30s)
	afn.cache.Set(key, content, cache.TTLAccount)
	return content, nil
}

// codeContent returns the account bytecode as hex string terminated with newline.
// Returns "0x\n" for EOA (not empty string).
func (afn *AccountFileNode) codeContent(ctx context.Context) ([]byte, error) {
	key := cacheKeyAccountCodePrefix + afn.addr.Hex()
	// Check cache first
	if data, ok := afn.cache.Get(key); ok {
		return data, nil
	}
	// Fetch from RPC
	code, err := afn.rpc.CodeAt(ctx, afn.addr, nil)
	if err != nil {
		return nil, err
	}
	// Convert to hex string with newline
	// For EOA: return "0x\n" (not empty string)
	var content []byte
	if len(code) == 0 {
		content = []byte("0x\n")
	} else {
		content = []byte("0x" + hex.EncodeToString(code) + "\n")
	}
	// Cache with TTLAccount (30s)
	afn.cache.Set(key, content, cache.TTLAccount)
	return content, nil
}

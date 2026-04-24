package fs

import (
	"context"
	"encoding/binary"
	"encoding/hex"
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
	cacheKeyAccountBalancePrefix = "account:balance:"
	cacheKeyAccountNoncePrefix   = "account:nonce:"
	cacheKeyAccountCodePrefix    = "account:code:"
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
func (a *AccountsDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for AccountsDirNode.
// Ghost directory: returns empty list.
func (a *AccountsDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream([]fuse.DirEntry{}), 0
}

// Lookup implements NodeLookuper for AccountsDirNode.
// Validates address format via common.IsHexAddress, returns AccountNode if valid,
// EINVAL for malformed address.
func (a *AccountsDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate address format
	if !common.IsHexAddress(name) {
		return nil, syscall.EINVAL
	}
	addr := common.HexToAddress(name)
	child := NewAccountNode(addr, a.rpc, a.cache)
	ch := a.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/accounts/" + strings.ToLower(name)),
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
// Returns directory entries for the account directory: balance, nonce, code.
func (an *AccountNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: fileNameBalance,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + strings.ToLower(an.addr.Hex()) + "/" + fileNameBalance),
		},
		{
			Name: fileNameNonce,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + strings.ToLower(an.addr.Hex()) + "/" + fileNameNonce),
		},
		{
			Name: fileNameCode,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/accounts/" + strings.ToLower(an.addr.Hex()) + "/" + fileNameCode),
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
		Ino:  hashIno("/accounts/" + strings.ToLower(an.addr.Hex()) + "/" + name),
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

var _ fs.NodeOpener = (*AccountFileNode)(nil)
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

// Open implements NodeOpener for AccountFileNode.
func (afn *AccountFileNode) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EROFS
	}
	return nil, fuse.FOPEN_DIRECT_IO, 0
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

// balanceContent returns the account balance as decimal string with newline.
func (afn *AccountFileNode) balanceContent(ctx context.Context) ([]byte, error) {
	balance, err := fetchAccountBalance(ctx, afn.addr, afn.rpc, afn.cache)
	if err != nil {
		return nil, err
	}
	return []byte(balance.String() + "\n"), nil
}

// nonceContent returns the account nonce as decimal string with newline.
func (afn *AccountFileNode) nonceContent(ctx context.Context) ([]byte, error) {
	nonce, err := fetchAccountNonce(ctx, afn.addr, afn.rpc, afn.cache)
	if err != nil {
		return nil, err
	}
	return []byte(strconv.FormatUint(nonce, 10) + "\n"), nil
}

// codeContent returns the account code as hex string with newline.
func (afn *AccountFileNode) codeContent(ctx context.Context) ([]byte, error) {
	code, err := fetchAccountCode(ctx, afn.addr, afn.rpc, afn.cache)
	if err != nil {
		return nil, err
	}
	// Ensure code is hex prefixed with 0x, even for empty result.
	// eth_getCode returns "0x" for EOA, which is what we want.
	hexStr := hex.EncodeToString(code)
	if hexStr == "" {
		hexStr = "0x"
	} else {
		hexStr = "0x" + hexStr
	}
	return []byte(hexStr + "\n"), nil
}

// fetchAccountBalance retrieves the balance of an account, using cache with TTLAccount.
func fetchAccountBalance(ctx context.Context, addr common.Address, rpc rpc.RPCClient, c *cache.Cache) (*big.Int, error) {
	key := cacheKeyAccountBalancePrefix + strings.ToLower(addr.Hex())
	// Check negative cache first
	if c.IsNegative(key) {
		return nil, ethereum.NotFound
	}
	// Try memory cache
	if data, ok := c.Get(key); ok {
		// Decode big.Int from bytes
		balance := new(big.Int).SetBytes(data)
		return balance, nil
	}
	// Fetch from RPC (latest block)
	balance, err := rpc.BalanceAt(ctx, addr, nil)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			// Negative caching
			c.SetNegative(key)
		}
		return nil, err
	}
	// Store in cache with TTLAccount
	c.Set(key, balance.Bytes(), cache.TTLAccount)
	return balance, nil
}

// fetchAccountNonce retrieves the nonce of an account, using cache with TTLAccount.
func fetchAccountNonce(ctx context.Context, addr common.Address, rpc rpc.RPCClient, c *cache.Cache) (uint64, error) {
	key := cacheKeyAccountNoncePrefix + strings.ToLower(addr.Hex())
	// Check negative cache first
	if c.IsNegative(key) {
		return 0, ethereum.NotFound
	}
	// Try memory cache
	if data, ok := c.Get(key); ok && len(data) == 8 {
		return binary.BigEndian.Uint64(data), nil
	}
	// Fetch from RPC (latest block)
	nonce, err := rpc.NonceAt(ctx, addr, nil)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			c.SetNegative(key)
		}
		return 0, err
	}
	// Store in cache with TTLAccount
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], nonce)
	c.Set(key, buf[:], cache.TTLAccount)
	return nonce, nil
}

// fetchAccountCode retrieves the code of an account, using cache with TTLAccount.
func fetchAccountCode(ctx context.Context, addr common.Address, rpc rpc.RPCClient, c *cache.Cache) ([]byte, error) {
	key := cacheKeyAccountCodePrefix + strings.ToLower(addr.Hex())
	// Check negative cache first
	if c.IsNegative(key) {
		return nil, ethereum.NotFound
	}
	// Try memory cache
	if data, ok := c.Get(key); ok {
		return data, nil
	}
	// Fetch from RPC (latest block)
	code, err := rpc.CodeAt(ctx, addr, nil)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			c.SetNegative(key)
		}
		return nil, err
	}
	// Store in cache with TTLAccount
	c.Set(key, code, cache.TTLAccount)
	return code, nil
}

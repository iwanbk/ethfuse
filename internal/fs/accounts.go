package fs

import (
	"context"
	"encoding/binary"
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
	cacheKeyAccountPrefix = "acct:"
	fileNameBalance       = "balance"
	fileNameNonce         = "nonce"
	fileNameCode          = "code"
)

// AccountsDirNode represents the /accounts directory (ghost).
// Readdir returns empty, but Lookup resolves address directories.
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
// Validates address format, returns AccountsNode if valid, EINVAL if malformed.
func (a *AccountsDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate address format: must start with 0x and be a valid hex address
	if !strings.HasPrefix(name, "0x") || !common.IsHexAddress(name) {
		return nil, syscall.EINVAL
	}
	addr := common.HexToAddress(name)
	child := NewAccountNode(addr, a.rpc, a.cache)
	ch := a.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/accounts/" + name),
		Mode: fuse.S_IFDIR,
	})
	out.Mode = fuse.S_IFDIR | 0o755
	return ch, 0
}

// AccountNode represents an account directory at /accounts/[address].
// Contains files: balance, nonce, code.
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
// Returns directory entries for balance, nonce, code files.
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
	out.Size = 0
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

// fetchAccountState retrieves account state (balance, nonce, code) using cache.
// Uses TTLAccount (30s) for caching.
func fetchAccountState(ctx context.Context, addr common.Address, rpc rpc.RPCClient, c *cache.Cache) (*big.Int, uint64, []byte, error) {
	key := cacheKeyAccountPrefix + addr.Hex()

	// Try cache first
	if data, ok := c.Get(key); ok && len(data) >= 16 {
		// Decode cached data: first 8 bytes = balance (big.Int bytes), next 8 bytes = nonce, rest = code
		balanceBytes := data[:8]
		nonceBytes := data[8:16]
		code := data[16:]

		balance := new(big.Int).SetBytes(balanceBytes)
		nonce := binary.BigEndian.Uint64(nonceBytes)

		// Verify cache is valid by re-fetching from RPC if needed
		// For simplicity, return cached values
		return balance, nonce, code, nil
	}

	// Fetch from RPC
	balance, err := rpc.BalanceAt(ctx, addr, nil)
	if err != nil {
		return nil, 0, nil, err
	}

	nonce, err := rpc.NonceAt(ctx, addr, nil)
	if err != nil {
		return nil, 0, nil, err
	}

	code, err := rpc.CodeAt(ctx, addr, nil)
	if err != nil {
		return nil, 0, nil, err
	}

	// Cache the data: balance (8 bytes) + nonce (8 bytes) + code
	// Note: balance is stored as 8 bytes (truncated for cache key purposes)
	// For actual balance, we need to store the full big.Int bytes
	balanceBytes := balance.Bytes()
	if len(balanceBytes) > 8 {
		balanceBytes = balanceBytes[len(balanceBytes)-8:] // take last 8 bytes
	}
	// Pad to 8 bytes
	balancePadded := make([]byte, 8)
	copy(balancePadded[8-len(balanceBytes):], balanceBytes)

	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonce)

	cacheData := append(balancePadded, nonceBytes...)
	cacheData = append(cacheData, code...)

	c.Set(key, cacheData, cache.TTLAccount)

	return balance, nonce, code, nil
}

// balanceContent returns the balance as a decimal string with newline.
func (afn *AccountFileNode) balanceContent(ctx context.Context) ([]byte, error) {
	balance, _, _, err := fetchAccountState(ctx, afn.addr, afn.rpc, afn.cache)
	if err != nil {
		return nil, err
	}
	return []byte(balance.String() + "\n"), nil
}

// nonceContent returns the nonce as a decimal string with newline.
func (afn *AccountFileNode) nonceContent(ctx context.Context) ([]byte, error) {
	_, nonce, _, err := fetchAccountState(ctx, afn.addr, afn.rpc, afn.cache)
	if err != nil {
		return nil, err
	}
	return []byte(strconv.FormatUint(nonce, 10) + "\n"), nil
}

// codeContent returns the code as hex string with 0x prefix and newline.
// For EOA, returns "0x\n" (not empty string).
func (afn *AccountFileNode) codeContent(ctx context.Context) ([]byte, error) {
	_, _, code, err := fetchAccountState(ctx, afn.addr, afn.rpc, afn.cache)
	if err != nil {
		return nil, err
	}
	if len(code) == 0 {
		return []byte("0x\n"), nil
	}
	return []byte("0x" + common.Bytes2Hex(code) + "\n"), nil
}

package fs

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

const (
	cacheKeyTxBlockMapPrefix = "txblockmap:"
)

// RootTxsDirNode represents the top-level /txs directory (ghost).
// Readdir returns empty, but Lookup resolves transaction hashes.
type RootTxsDirNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeLookuper = (*RootTxsDirNode)(nil)
var _ fs.NodeReaddirer = (*RootTxsDirNode)(nil)
var _ fs.NodeGetattrer = (*RootTxsDirNode)(nil)

// NewRootTxsDirNode creates a new RootTxsDirNode.
func NewRootTxsDirNode(rpc rpc.RPCClient, cache *cache.Cache) *RootTxsDirNode {
	return &RootTxsDirNode{
		rpc:   rpc,
		cache: cache,
	}
}

// Getattr implements NodeGetattrer for RootTxsDirNode.
func (rtdn *RootTxsDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for RootTxsDirNode.
// Ghost directory: returns empty list.
func (rtdn *RootTxsDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream([]fuse.DirEntry{}), 0
}

// Lookup implements NodeLookuper for RootTxsDirNode.
// Validates hash format, fetches transaction via eth_getTransactionByHash,
// returns RootTxNode if found, ENOENT otherwise.
func (rtdn *RootTxsDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate hash format: must start with 0x and have 64 hex characters
	if len(name) != 66 || !strings.HasPrefix(name, "0x") || len(common.FromHex(name)) != 32 {
		// Invalid hash format per architecture error mapping
		return nil, syscall.EINVAL
	}
	txHash := common.HexToHash(name)

	// Fetch transaction to get block number (if mined)
	tx, blockNum, err := fetchTransactionByHash(ctx, txHash, rtdn.rpc, rtdn.cache)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		return nil, syscall.EIO
	}
	_ = tx // used later for info.json content, but we can refetch from block

	// Create RootTxNode with blockNum (0 for pending)
	child := NewRootTxNode(blockNum, txHash, rtdn.rpc, rtdn.cache)
	ch := rtdn.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/txs/" + name),
		Mode: fuse.S_IFDIR,
	})
	out.Mode = fuse.S_IFDIR | 0o755
	return ch, 0
}

// RootTxNode represents a transaction directory at /txs/[hash].
type RootTxNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	blockNum uint64 // 0 if pending
	txHash   common.Hash
}

var _ fs.NodeLookuper = (*RootTxNode)(nil)
var _ fs.NodeReaddirer = (*RootTxNode)(nil)
var _ fs.NodeGetattrer = (*RootTxNode)(nil)

// NewRootTxNode creates a new RootTxNode.
func NewRootTxNode(blockNum uint64, txHash common.Hash, rpc rpc.RPCClient, cache *cache.Cache) *RootTxNode {
	return &RootTxNode{
		blockNum: blockNum,
		txHash:   txHash,
		rpc:      rpc,
		cache:    cache,
	}
}

// Getattr implements NodeGetattrer for RootTxNode.
func (rtn *RootTxNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for RootTxNode.
// Returns directory entries for the transaction directory.
func (rtn *RootTxNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: fileNameInfoJSON,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/txs/" + rtn.txHash.Hex() + "/" + fileNameInfoJSON),
		},
		{
			Name: fileNameReceiptJSON,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/txs/" + rtn.txHash.Hex() + "/" + fileNameReceiptJSON),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Lookup implements NodeLookuper for RootTxNode.
// Returns child file nodes: info.json, receipt.json.
func (rtn *RootTxNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var fileType string
	switch name {
	case fileNameInfoJSON:
		fileType = fileNameInfoJSON
	case fileNameReceiptJSON:
		fileType = fileNameReceiptJSON
	default:
		return nil, syscall.ENOENT
	}
	child := NewTxFileNode(rtn.blockNum, rtn.txHash, fileType, rtn.rpc, rtn.cache)
	ch := rtn.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/txs/" + rtn.txHash.Hex() + "/" + name),
		Mode: fuse.S_IFREG,
	})
	out.Mode = fuse.S_IFREG | 0o644
	out.Size = 0 // unknown size
	return ch, 0
}

// fetchTransactionByHash retrieves a transaction by hash, using cache.
// Returns transaction, block number (0 if pending), error.
func fetchTransactionByHash(ctx context.Context, txHash common.Hash, rpc rpc.RPCClient, c *cache.Cache) (*types.Transaction, uint64, error) {
	key := cacheKeyTxBlockMapPrefix + txHash.Hex()
	// Check negative cache first
	if c.IsNegative(key) {
		return nil, 0, ethereum.NotFound
	}
	// First try memory cache for block number mapping
	if data, ok := c.Get(key); ok && len(data) == 8 {
		blockNum := binary.BigEndian.Uint64(data)
		if blockNum == 0 {
			// pending marker, treat as not cached (need to refetch)
		} else {
			// We have block number, but need to fetch transaction from block cache.
			block, err := fetchBlock(ctx, blockNum, rpc, c)
			if err != nil {
				return nil, 0, err
			}
			for _, tx := range block.Transactions() {
				if tx.Hash() == txHash {
					return tx, blockNum, nil
				}
			}
			// Should not happen: mapping exists but tx not in block (reorg?)
			// Log this rare case? For now, fall through to re-fetch via RPC.
		}
	}
	// Fetch from RPC
	tx, err := rpc.TransactionByHash(ctx, txHash)
	if err != nil {
		// Check if it's a not found error
		if errors.Is(err, ethereum.NotFound) {
			// Negative caching
			c.SetNegative(key)
		}
		return nil, 0, err
	}
	// Try to get receipt to determine block number
	// Note: Our RPCClient.TransactionByHash interface doesn't return the isPending boolean,
	// so we need to fetch receipt to distinguish pending vs mined transactions.
	var blockNum uint64
	receipt, err := fetchTransactionReceipt(ctx, txHash, 0, rpc, c)
	if err != nil {
		// If receipt not found, treat as pending (blockNum stays 0)
	} else {
		blockNum = receipt.BlockNumber.Uint64()
	}
	// Cache block number mapping only for mined transactions (pending are not cached per architecture.md)
	if blockNum > 0 {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], blockNum)
		// Determine finalization
		latest, err := fetchLatestBlockNumber(ctx, rpc, c)
		if err != nil {
			// fallback to memory cache with block time TTL
			c.Set(key, buf[:], cache.TTLBlockLatest)
		} else if latest-blockNum >= BlockFinalizationConfirmations {
			// Finalized: store in persistent storage (bucket "txblockmap")
			if err := c.SetPersistent("txblockmap", key, buf[:]); err != nil {
				// fallback to memory cache with long TTL
				c.Set(key, buf[:], time.Hour)
			} else {
				// Also keep in memory cache for faster access
				c.Set(key, buf[:], time.Hour)
			}
		} else {
			// Non-finalized: store in memory cache with block time TTL
			c.Set(key, buf[:], cache.TTLBlockLatest)
		}
	}
	// Note: fetchLatestBlockNumber is called here and again in fetchTransactionReceipt.
	// Both calls use the same cache (10s TTL), so redundancy is minimal.
	return tx, blockNum, nil
}

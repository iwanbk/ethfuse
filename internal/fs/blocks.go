package fs

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math/big"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

// hashIno generates a deterministic inode number from a string path.
func hashIno(path string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(path))
	return h.Sum64()
}

const (
	cacheKeyBlockPrefix     = "block:"
	cacheKeyTxReceiptPrefix = "txr:"
	cacheKeyLatestBlock     = "chain/latest_block"
	fileNameInfoJSON        = "info.json"
	fileNameHash            = "hash"
	fileNameParentHash      = "parent_hash"
	fileNameTimestamp       = "timestamp"
	fileNameReceiptJSON     = "receipt.json"
	dirNameTxs              = "txs"
	// BlockFinalizationConfirmations is the number of confirmations required for a block to be considered finalized.
	// Finalized blocks are stored in persistent storage (bbolt), non-finalized blocks use TTL-based memory cache.
	BlockFinalizationConfirmations = 12
)

// encodeBlock serializes a block to bytes using RLP.
func encodeBlock(block *types.Block) ([]byte, error) {
	return rlp.EncodeToBytes(block)
}

// decodeBlock deserializes bytes into a block.
func decodeBlock(data []byte) (*types.Block, error) {
	var block types.Block
	if err := rlp.DecodeBytes(data, &block); err != nil {
		return nil, err
	}
	return &block, nil
}

// fetchLatestBlockNumber returns the latest block number, using cache.
// Uses TTLChainLatestBlock (10s) for chain/latest_block file.
func fetchLatestBlockNumber(ctx context.Context, rpc rpc.RPCClient, c *cache.Cache) (uint64, error) {
	return fetchLatestBlockNumberWithTTL(ctx, rpc, c, cache.TTLChainLatestBlock)
}

// fetchLatestBlockNumberWithTTL returns the latest block number with custom TTL.
// This is used by LatestDirNode which requires 12s TTL (one block time).
func fetchLatestBlockNumberWithTTL(ctx context.Context, rpc rpc.RPCClient, c *cache.Cache, ttl time.Duration) (uint64, error) {
	// Try cache first
	if data, ok := c.Get(cacheKeyLatestBlock); ok {
		if len(data) == 8 {
			return binary.BigEndian.Uint64(data), nil
		}
	}
	// Fetch from RPC
	block, err := rpc.BlockByNumber(ctx, nil)
	if err != nil {
		return 0, err
	}
	blockNum := block.Number().Uint64()
	// Store as big-endian bytes
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], blockNum)
	c.Set(cacheKeyLatestBlock, buf[:], ttl)
	return blockNum, nil
}

// fetchBlock retrieves a block by number, using cache and finalization logic.
func fetchBlock(ctx context.Context, blockNum uint64, rpc rpc.RPCClient, c *cache.Cache) (*types.Block, error) {
	key := cacheKeyBlockPrefix + strconv.FormatUint(blockNum, 10)
	// First try memory cache
	if data, ok := c.Get(key); ok {
		block, err := decodeBlock(data)
		if err == nil {
			return block, nil
		}
		// If decode fails, fall through to fetch
	}
	// Then try persistent storage
	if data, err := c.GetPersistent("blocks", key); err == nil {
		block, err := decodeBlock(data)
		if err == nil {
			// Also populate memory cache with no TTL? but block is finalized, can store with long TTL.
			// For simplicity, store in memory cache with long TTL (e.g., 1 hour).
			c.Set(key, data, time.Hour)
			return block, nil
		}
	}
	// Fetch from RPC
	block, err := rpc.BlockByNumber(ctx, big.NewInt(int64(blockNum)))
	if err != nil {
		// Check if it's a not found error
		if errors.Is(err, ethereum.NotFound) {
			// Negative caching
			c.SetNegative(key)
		}
		return nil, err
	}
	// Encode block
	data, err := encodeBlock(block)
	if err != nil {
		// If encoding fails, still return block (but can't cache)
		return block, nil
	}
	// Determine finalization
	latest, err := fetchLatestBlockNumber(ctx, rpc, c)
	if err != nil {
		// If we can't determine, treat as non-finalized
		c.Set(key, data, cache.TTLBlockLatest)
		return block, nil
	}
	if latest-blockNum >= BlockFinalizationConfirmations {
		// Finalized: store in persistent storage
		if err := c.SetPersistent("blocks", key, data); err != nil {
			// Log? fallback to memory cache
			c.Set(key, data, time.Hour)
		} else {
			// Also keep in memory cache for faster access
			c.Set(key, data, time.Hour)
		}
	} else {
		// Non-finalized: store in memory cache with block time TTL
		c.Set(key, data, cache.TTLBlockLatest)
	}
	return block, nil
}

// encodeReceipt serializes a receipt to bytes using RLP.
func encodeReceipt(receipt *types.Receipt) ([]byte, error) {
	return rlp.EncodeToBytes(receipt)
}

// decodeReceipt deserializes bytes into a receipt.
func decodeReceipt(data []byte) (*types.Receipt, error) {
	var receipt types.Receipt
	if err := rlp.DecodeBytes(data, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// fetchTransactionReceipt retrieves a transaction receipt by hash, using cache and finalization logic.
// blockNum is the block number containing the transaction (optional, zero means unknown).
// If blockNum is zero, it will be extracted from the receipt itself.
func fetchTransactionReceipt(ctx context.Context, txHash common.Hash, blockNum uint64, rpc rpc.RPCClient, c *cache.Cache) (*types.Receipt, error) {
	key := cacheKeyTxReceiptPrefix + txHash.Hex()
	// Check negative cache first
	if c.IsNegative(key) {
		return nil, ethereum.NotFound
	}
	// First try memory cache
	if data, ok := c.Get(key); ok {
		receipt, err := decodeReceipt(data)
		if err == nil {
			return receipt, nil
		}
		// If decode fails, fall through to fetch
	}
	// Then try persistent storage
	if data, err := c.GetPersistent("receipts", key); err == nil {
		receipt, err := decodeReceipt(data)
		if err == nil {
			// Also populate memory cache with long TTL (1 hour) since receipt is finalized
			c.Set(key, data, time.Hour)
			return receipt, nil
		}
	}
	// Fetch from RPC
	receipt, err := rpc.TransactionReceipt(ctx, txHash)
	if err != nil {
		// Check if it's a not found error
		if errors.Is(err, ethereum.NotFound) {
			// Negative caching
			c.SetNegative(key)
		}
		return nil, err
	}
	// Determine block number from receipt if not provided
	if blockNum == 0 {
		blockNum = receipt.BlockNumber.Uint64()
	}
	// Encode receipt
	data, err := encodeReceipt(receipt)
	if err != nil {
		// If encoding fails, still return receipt (but can't cache)
		return receipt, nil
	}
	// Determine finalization using latest block number
	// Note: fetchLatestBlockNumber has its own 10-second cache, so in practice
	// this doesn't cause excessive RPC calls. Could be optimized by passing
	// latest block number as a parameter if caller already has it.
	latest, err := fetchLatestBlockNumber(ctx, rpc, c)
	if err != nil {
		// If we can't determine, treat as non-finalized
		c.Set(key, data, cache.TTLTxReceipt)
		return receipt, nil
	}
	if latest-blockNum >= BlockFinalizationConfirmations {
		// Finalized: store in persistent storage
		if err := c.SetPersistent("receipts", key, data); err != nil {
			// Log? fallback to memory cache
			c.Set(key, data, time.Hour)
		} else {
			// Also keep in memory cache for faster access
			c.Set(key, data, time.Hour)
		}
	} else {
		// Non-finalized: store in memory cache with receipt TTL
		c.Set(key, data, cache.TTLTxReceipt)
	}
	return receipt, nil
}

// BlocksDirNode represents the /blocks directory.
// It contains:
//   - recent/ subdirectory (last 100 blocks visible in ls)
//   - ghost entries for any block number via direct lookup
type BlocksDirNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeLookuper = (*BlocksDirNode)(nil)
var _ fs.NodeReaddirer = (*BlocksDirNode)(nil)
var _ fs.NodeGetattrer = (*BlocksDirNode)(nil)

// NewBlocksDirNode creates a new BlocksDirNode.
func NewBlocksDirNode(rpc rpc.RPCClient, cache *cache.Cache) *BlocksDirNode {
	return &BlocksDirNode{
		rpc:   rpc,
		cache: cache,
	}
}

// RecentDirNode represents the /blocks/recent directory.
type RecentDirNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeLookuper = (*RecentDirNode)(nil)
var _ fs.NodeReaddirer = (*RecentDirNode)(nil)
var _ fs.NodeGetattrer = (*RecentDirNode)(nil)

// NewRecentDirNode creates a new RecentDirNode.
func NewRecentDirNode(rpc rpc.RPCClient, cache *cache.Cache) *RecentDirNode {
	return &RecentDirNode{
		rpc:   rpc,
		cache: cache,
	}
}

// Readdir implements NodeReaddirer for RecentDirNode.
// Returns last 100 block numbers as directory entries.
func (r *RecentDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	latest, err := fetchLatestBlockNumber(ctx, r.rpc, r.cache)
	if err != nil {
		// Map RPC errors to appropriate errno
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		return nil, syscall.EIO
	}
	var entries []fuse.DirEntry
	// Start from latest, go backwards up to 100 blocks, but not below 0
	start := latest
	if latest > 99 {
		start = latest - 99
	}
	for num := latest; num >= start; num-- {
		name := strconv.FormatUint(num, 10)
		entries = append(entries, fuse.DirEntry{
			Name: name,
			Mode: fuse.S_IFDIR,
			// Use /blocks/[number] as path for consistent inode across access methods
			Ino: hashIno("/blocks/" + name),
		})
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements NodeGetattrer for RecentDirNode.
func (r *RecentDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Lookup implements NodeLookuper for RecentDirNode.
// Called when the kernel looks up a block number under /blocks/recent.
func (r *RecentDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Parse block number
	blockNum, err := strconv.ParseUint(name, 10, 64)
	if err != nil {
		// Not a number, treat as non-existent
		return nil, syscall.ENOENT
	}
	child := NewBlockNode(blockNum, r.rpc, r.cache)
	ch := r.NewPersistentInode(ctx, child, fs.StableAttr{
		// Use /blocks/[number] as path for consistent inode across access methods
		// (both /blocks/[number] and /blocks/recent/[number] refer to same block directory)
		Ino:  hashIno("/blocks/" + name),
		Mode: fuse.S_IFDIR,
	})
	out.Mode = fuse.S_IFDIR | 0o755
	return ch, 0
}

// Lookup implements NodeLookuper.
// Called when the kernel looks up a child entry (e.g., ls /blocks/21000000).
// name can be "recent", "latest", or a block number as decimal string.
// Returns a child node (directory or file) or ENOENT.
func (b *BlocksDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "recent":
		child := NewRecentDirNode(b.rpc, b.cache)
		ch := b.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/recent"),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	case "latest":
		child := NewLatestDirNode(b.rpc, b.cache)
		ch := b.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/latest"),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	}
	// Parse block number
	blockNum, err := strconv.ParseUint(name, 10, 64)
	if err != nil {
		// Not a number, treat as non-existent
		return nil, syscall.ENOENT
	}
	child := NewBlockNode(blockNum, b.rpc, b.cache)
	ch := b.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/blocks/" + name),
		Mode: fuse.S_IFDIR,
	})
	out.Mode = fuse.S_IFDIR | 0o755
	return ch, 0
}

// Readdir implements NodeReaddirer.
// Returns directory entries for /blocks (recent and latest subdirectories).
// The kernel will call Lookup on each name we return.
func (b *BlocksDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: "recent",
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/blocks/recent"),
		},
		{
			Name: "latest",
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/blocks/latest"),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements NodeGetattrer for BlocksDirNode.
func (b *BlocksDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// LatestDirNode represents the /blocks/latest directory.
// It always points to the current chain head, refreshed every 12s.
type LatestDirNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeLookuper = (*LatestDirNode)(nil)
var _ fs.NodeReaddirer = (*LatestDirNode)(nil)
var _ fs.NodeGetattrer = (*LatestDirNode)(nil)

// NewLatestDirNode creates a new LatestDirNode.
func NewLatestDirNode(rpc rpc.RPCClient, cache *cache.Cache) *LatestDirNode {
	return &LatestDirNode{
		rpc:   rpc,
		cache: cache,
	}
}

// Lookup implements NodeLookuper for LatestDirNode.
// Returns child file nodes: info.json, hash, parent_hash, timestamp, txs directory.
func (ln *LatestDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Get latest block number with 12s TTL (one block time)
	latest, err := fetchLatestBlockNumberWithTTL(ctx, ln.rpc, ln.cache, cache.TTLBlockLatest)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		return nil, syscall.EIO
	}
	// Delegate to BlockFileNode or TxsDirNode based on name
	switch name {
	case fileNameInfoJSON:
		child := NewBlockFileNode(latest, fileNameInfoJSON, ln.rpc, ln.cache)
		ch := ln.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/latest/" + name),
			Mode: fuse.S_IFREG,
		})
		out.Mode = fuse.S_IFREG | 0o644
		out.Size = 0
		return ch, 0
	case fileNameHash:
		child := NewBlockFileNode(latest, fileNameHash, ln.rpc, ln.cache)
		ch := ln.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/latest/" + name),
			Mode: fuse.S_IFREG,
		})
		out.Mode = fuse.S_IFREG | 0o644
		out.Size = 0
		return ch, 0
	case fileNameParentHash:
		child := NewBlockFileNode(latest, fileNameParentHash, ln.rpc, ln.cache)
		ch := ln.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/latest/" + name),
			Mode: fuse.S_IFREG,
		})
		out.Mode = fuse.S_IFREG | 0o644
		out.Size = 0
		return ch, 0
	case fileNameTimestamp:
		child := NewBlockFileNode(latest, fileNameTimestamp, ln.rpc, ln.cache)
		ch := ln.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/latest/" + name),
			Mode: fuse.S_IFREG,
		})
		out.Mode = fuse.S_IFREG | 0o644
		out.Size = 0
		return ch, 0
	case dirNameTxs:
		child := NewTxsDirNode(latest, ln.rpc, ln.cache)
		ch := ln.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/latest/" + dirNameTxs),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	default:
		return nil, syscall.ENOENT
	}
}

// Readdir implements NodeReaddirer for LatestDirNode.
// Returns directory entries for the latest block directory.
func (ln *LatestDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: fileNameInfoJSON,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/latest/" + fileNameInfoJSON),
		},
		{
			Name: fileNameHash,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/latest/" + fileNameHash),
		},
		{
			Name: fileNameParentHash,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/latest/" + fileNameParentHash),
		},
		{
			Name: fileNameTimestamp,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/latest/" + fileNameTimestamp),
		},
		{
			Name: dirNameTxs,
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/blocks/latest/" + dirNameTxs),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements NodeGetattrer for LatestDirNode.
func (ln *LatestDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// BlockNode represents a block directory at /blocks/[number].
type BlockNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	blockNum uint64
}

var _ fs.NodeLookuper = (*BlockNode)(nil)
var _ fs.NodeReaddirer = (*BlockNode)(nil)
var _ fs.NodeGetattrer = (*BlockNode)(nil)

// TxsDirNode represents the txs/ subdirectory inside a block directory.
type TxsDirNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	blockNum uint64
}

var _ fs.NodeLookuper = (*TxsDirNode)(nil)
var _ fs.NodeReaddirer = (*TxsDirNode)(nil)
var _ fs.NodeGetattrer = (*TxsDirNode)(nil)

// NewTxsDirNode creates a new TxsDirNode.
func NewTxsDirNode(blockNum uint64, rpc rpc.RPCClient, cache *cache.Cache) *TxsDirNode {
	return &TxsDirNode{
		blockNum: blockNum,
		rpc:      rpc,
		cache:    cache,
	}
}

// TxNode represents a transaction directory at /blocks/[number]/txs/[hash].
type TxNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	blockNum uint64
	txHash   common.Hash
}

var _ fs.NodeLookuper = (*TxNode)(nil)
var _ fs.NodeReaddirer = (*TxNode)(nil)
var _ fs.NodeGetattrer = (*TxNode)(nil)

// NewTxNode creates a new TxNode.
func NewTxNode(blockNum uint64, txHash common.Hash, rpc rpc.RPCClient, cache *cache.Cache) *TxNode {
	return &TxNode{
		blockNum: blockNum,
		txHash:   txHash,
		rpc:      rpc,
		cache:    cache,
	}
}

// TxFileNode represents a file inside a transaction directory.
type TxFileNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	fileType string // one of fileNameInfoJSON, fileNameReceiptJSON
	blockNum uint64
	txHash   common.Hash
}

var _ fs.NodeReader = (*TxFileNode)(nil)
var _ fs.NodeGetattrer = (*TxFileNode)(nil)

// NewTxFileNode creates a new TxFileNode.
func NewTxFileNode(blockNum uint64, txHash common.Hash, fileType string, rpc rpc.RPCClient, cache *cache.Cache) *TxFileNode {
	return &TxFileNode{
		blockNum: blockNum,
		txHash:   txHash,
		fileType: fileType,
		rpc:      rpc,
		cache:    cache,
	}
}

// Getattr implements NodeGetattrer for TxsDirNode.
func (tdn *TxsDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for TxsDirNode.
// Returns transaction hashes from the cached block object (no extra RPC call).
func (tdn *TxsDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	block, err := fetchBlock(ctx, tdn.blockNum, tdn.rpc, tdn.cache)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		return nil, syscall.EIO
	}
	var entries []fuse.DirEntry
	for _, tx := range block.Transactions() {
		txHash := tx.Hash().Hex()
		entries = append(entries, fuse.DirEntry{
			Name: txHash,
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(tdn.blockNum, 10) + "/txs/" + txHash),
		})
	}
	return fs.NewListDirStream(entries), 0
}

// Lookup implements NodeLookuper for TxsDirNode.
// Returns TxNode for a transaction hash, or ENOENT if not found in the block.
func (tdn *TxsDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate hash format: must start with 0x and have 64 hex characters
	if len(name) != 66 || !strings.HasPrefix(name, "0x") || common.FromHex(name) == nil {
		// Invalid hash format per architecture error mapping
		return nil, syscall.EINVAL
	}
	txHash := common.HexToHash(name)
	// Fetch block to verify transaction exists
	block, err := fetchBlock(ctx, tdn.blockNum, tdn.rpc, tdn.cache)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		return nil, syscall.EIO
	}
	// Check if transaction exists in block
	for _, tx := range block.Transactions() {
		if tx.Hash() == txHash {
			child := NewTxNode(tdn.blockNum, txHash, tdn.rpc, tdn.cache)
			ch := tdn.NewPersistentInode(ctx, child, fs.StableAttr{
				Ino:  hashIno("/blocks/" + strconv.FormatUint(tdn.blockNum, 10) + "/txs/" + name),
				Mode: fuse.S_IFDIR,
			})
			out.Mode = fuse.S_IFDIR | 0o755
			return ch, 0
		}
	}
	return nil, syscall.ENOENT
}

// Getattr implements NodeGetattrer for TxNode.
func (tn *TxNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Readdir implements NodeReaddirer for TxNode.
// Returns directory entries for the transaction directory.
func (tn *TxNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: fileNameInfoJSON,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(tn.blockNum, 10) + "/txs/" + tn.txHash.Hex() + "/" + fileNameInfoJSON),
		},
		{
			Name: fileNameReceiptJSON,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(tn.blockNum, 10) + "/txs/" + tn.txHash.Hex() + "/" + fileNameReceiptJSON),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Lookup implements NodeLookuper for TxNode.
// Returns child file nodes: info.json, receipt.json.
func (tn *TxNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var fileType string
	switch name {
	case fileNameInfoJSON:
		fileType = fileNameInfoJSON
	case fileNameReceiptJSON:
		fileType = fileNameReceiptJSON
	default:
		return nil, syscall.ENOENT
	}
	child := NewTxFileNode(tn.blockNum, tn.txHash, fileType, tn.rpc, tn.cache)
	ch := tn.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/blocks/" + strconv.FormatUint(tn.blockNum, 10) + "/txs/" + tn.txHash.Hex() + "/" + name),
		Mode: fuse.S_IFREG,
	})
	out.Mode = fuse.S_IFREG | 0o644
	out.Size = 0 // unknown size
	return ch, 0
}

// Getattr implements NodeGetattrer for TxFileNode.
func (tfn *TxFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFREG | 0o644
	out.Size = 0
	return 0
}

// Read implements NodeReader for TxFileNode.
func (tfn *TxFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content, err := tfn.fileContent(ctx)
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
func (tfn *TxFileNode) fileContent(ctx context.Context) ([]byte, error) {
	switch tfn.fileType {
	case fileNameInfoJSON:
		return tfn.infoJSONContent(ctx)
	case fileNameReceiptJSON:
		return tfn.receiptJSONContent(ctx)
	default:
		return nil, errors.New("unknown file type")
	}
}

// infoJSONContent returns JSON representation of transaction info from cached block.
func (tfn *TxFileNode) infoJSONContent(ctx context.Context) ([]byte, error) {
	block, err := fetchBlock(ctx, tfn.blockNum, tfn.rpc, tfn.cache)
	if err != nil {
		return nil, err
	}
	// Find transaction by hash
	var targetTx *types.Transaction
	for _, tx := range block.Transactions() {
		if tx.Hash() == tfn.txHash {
			targetTx = tx
			break
		}
	}
	if targetTx == nil {
		return nil, ethereum.NotFound
	}
	// Build info struct
	info := struct {
		Hash                 string `json:"hash"`
		From                 string `json:"from"`
		To                   string `json:"to"`
		Value                string `json:"value"`
		GasPrice             string `json:"gasPrice"`
		MaxFeePerGas         string `json:"maxFeePerGas,omitempty"`
		MaxPriorityFeePerGas string `json:"maxPriorityFeePerGas,omitempty"`
		Input                string `json:"input"`
		Gas                  uint64 `json:"gas"`
		Nonce                uint64 `json:"nonce"`
		BlockNumber          uint64 `json:"blockNumber"`
		TransactionIndex     uint   `json:"transactionIndex"`
	}{
		Hash:        tfn.txHash.Hex(),
		Value:       targetTx.Value().String(), // decimal string
		Gas:         targetTx.Gas(),
		Nonce:       targetTx.Nonce(),
		Input:       common.Bytes2Hex(targetTx.Data()),
		BlockNumber: tfn.blockNum,
		// From, To, and TransactionIndex will be populated below
		// GasPrice, MaxFeePerGas, MaxPriorityFeePerGas depend on transaction type
	}
	// Recover sender (from)
	var signer types.Signer
	if chainID := targetTx.ChainId(); chainID != nil {
		signer = types.LatestSignerForChainID(chainID)
	} else {
		signer = types.HomesteadSigner{}
	}
	from, err := types.Sender(signer, targetTx)
	if err != nil {
		// Sender recovery may fail for pre-homestead transactions or malformed signatures
		// Leave field empty rather than failing the entire request
		info.From = ""
	} else {
		info.From = from.Hex()
	}
	// To (nil for contract creation)
	if targetTx.To() != nil {
		info.To = targetTx.To().Hex()
	} else {
		info.To = ""
	}
	// GasPrice depends on tx type
	if targetTx.Type() == types.LegacyTxType {
		info.GasPrice = targetTx.GasPrice().String()
	} else {
		// EIP-1559: gasPrice field not applicable, use maxFeePerGas and maxPriorityFeePerGas
		if targetTx.GasTipCap() != nil {
			info.MaxPriorityFeePerGas = targetTx.GasTipCap().String()
		}
		if targetTx.GasFeeCap() != nil {
			info.MaxFeePerGas = targetTx.GasFeeCap().String()
		}
		// gasPrice is intentionally left empty for EIP-1559 transactions
		// Effective gas price is available in receipt.json
	}
	// Find transaction index
	for i, tx := range block.Transactions() {
		if tx.Hash() == tfn.txHash {
			info.TransactionIndex = uint(i)
			break
		}
	}
	// Marshal JSON with indentation
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, err
	}
	// Append newline for consistency
	return append(data, '\n'), nil
}

// receiptJSONContent returns JSON representation of transaction receipt (lazy fetch).
func (tfn *TxFileNode) receiptJSONContent(ctx context.Context) ([]byte, error) {
	receipt, err := fetchTransactionReceipt(ctx, tfn.txHash, tfn.blockNum, tfn.rpc, tfn.cache)
	if err != nil {
		return nil, err
	}
	// Build receipt struct matching issue specification
	//nolint:govet
	info := struct {
		Logs              []*types.Log `json:"logs"`
		EffectiveGasPrice string       `json:"effectiveGasPrice"`
		LogsBloom         string       `json:"logsBloom"`
		ContractAddress   string       `json:"contractAddress"`
		Status            uint64       `json:"status"`
		GasUsed           uint64       `json:"gasUsed"`
	}{
		Logs:              receipt.Logs,
		EffectiveGasPrice: receipt.EffectiveGasPrice.String(),
		LogsBloom:         hex.EncodeToString(receipt.Bloom[:]),
		ContractAddress:   "",
		Status:            receipt.Status,
		GasUsed:           receipt.GasUsed,
	}
	if receipt.ContractAddress != (common.Address{}) {
		info.ContractAddress = receipt.ContractAddress.Hex()
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// BlockFileNode represents a file inside a block directory.
type BlockFileNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	fileType string // one of fileNameInfoJSON, fileNameHash, fileNameParentHash, fileNameTimestamp
	blockNum uint64
}

var _ fs.NodeReader = (*BlockFileNode)(nil)
var _ fs.NodeGetattrer = (*BlockFileNode)(nil)

// NewBlockFileNode creates a new BlockFileNode.
func NewBlockFileNode(blockNum uint64, fileType string, rpc rpc.RPCClient, cache *cache.Cache) *BlockFileNode {
	return &BlockFileNode{
		blockNum: blockNum,
		fileType: fileType,
		rpc:      rpc,
		cache:    cache,
	}
}

// Getattr implements NodeGetattrer for BlockFileNode.
func (bfn *BlockFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFREG | 0o644
	// Size unknown; we could compute but not required
	out.Size = 0
	return 0
}

// Read implements NodeReader for BlockFileNode.
func (bfn *BlockFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	block, err := fetchBlock(ctx, bfn.blockNum, bfn.rpc, bfn.cache)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, syscall.ENOENT
		}
		// TODO: map other errors appropriately
		return nil, syscall.EIO
	}
	content, err := bfn.fileContent(block)
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

// fileContent returns the file content as bytes based on fileType.
func (bfn *BlockFileNode) fileContent(block *types.Block) ([]byte, error) {
	switch bfn.fileType {
	case fileNameInfoJSON:
		data, err := bfn.infoJSONContent(block)
		if err != nil {
			return nil, err
		}
		// Append newline for consistency with scalar files
		return append(data, '\n'), nil
	case fileNameHash:
		return []byte(block.Hash().Hex() + "\n"), nil
	case fileNameParentHash:
		return []byte(block.ParentHash().Hex() + "\n"), nil
	case fileNameTimestamp:
		return []byte(strconv.FormatUint(block.Time(), 10) + "\n"), nil
	default:
		return nil, errors.New("unknown file type")
	}
}

// infoJSONContent returns the JSON representation of block info.
func (bfn *BlockFileNode) infoJSONContent(block *types.Block) ([]byte, error) {
	info := struct {
		Hash          string `json:"hash"`
		ParentHash    string `json:"parentHash"`
		Miner         string `json:"miner"`
		BaseFeePerGas string `json:"baseFeePerGas,omitempty"`
		Difficulty    string `json:"difficulty"`
		Number        uint64 `json:"number"`
		Timestamp     uint64 `json:"timestamp"`
		GasUsed       uint64 `json:"gasUsed"`
		GasLimit      uint64 `json:"gasLimit"`
		Size          uint64 `json:"size"`
		TxCount       int    `json:"txCount"`
	}{
		Number:     block.Number().Uint64(),
		Hash:       block.Hash().Hex(),
		ParentHash: block.ParentHash().Hex(),
		Timestamp:  block.Time(),
		Miner:      block.Coinbase().Hex(),
		GasUsed:    block.GasUsed(),
		GasLimit:   block.GasLimit(),
		Difficulty: block.Difficulty().String(),
		Size:       uint64(block.Size()),
		TxCount:    len(block.Transactions()),
	}
	// BaseFeePerGas exists only after London fork
	if block.BaseFee() != nil {
		info.BaseFeePerGas = block.BaseFee().String()
	}
	return json.MarshalIndent(info, "", "  ")
}

// NewBlockNode creates a new BlockNode.
func NewBlockNode(blockNum uint64, rpc rpc.RPCClient, cache *cache.Cache) *BlockNode {
	return &BlockNode{
		blockNum: blockNum,
		rpc:      rpc,
		cache:    cache,
	}
}

// Lookup implements NodeLookuper for BlockNode.
// Returns child file nodes: info.json, hash, parent_hash, timestamp, txs directory.
func (bn *BlockNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var fileType string
	switch name {
	case fileNameInfoJSON:
		fileType = fileNameInfoJSON
	case fileNameHash:
		fileType = fileNameHash
	case fileNameParentHash:
		fileType = fileNameParentHash
	case fileNameTimestamp:
		fileType = fileNameTimestamp
	case dirNameTxs:
		// txs subdirectory
		child := NewTxsDirNode(bn.blockNum, bn.rpc, bn.cache)
		ch := bn.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/" + strconv.FormatUint(bn.blockNum, 10) + "/" + dirNameTxs),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	default:
		// Unknown file
		return nil, syscall.ENOENT
	}
	child := NewBlockFileNode(bn.blockNum, fileType, bn.rpc, bn.cache)
	ch := bn.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/blocks/" + strconv.FormatUint(bn.blockNum, 10) + "/" + name),
		Mode: fuse.S_IFREG,
	})
	out.Mode = fuse.S_IFREG | 0o644
	out.Size = 0 // unknown size; we could compute but not required
	return ch, 0
}

// Readdir implements NodeReaddirer for BlockNode.
// Returns directory entries for the block directory.
func (bn *BlockNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: fileNameInfoJSON,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(bn.blockNum, 10) + "/" + fileNameInfoJSON),
		},
		{
			Name: fileNameHash,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(bn.blockNum, 10) + "/" + fileNameHash),
		},
		{
			Name: fileNameParentHash,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(bn.blockNum, 10) + "/" + fileNameParentHash),
		},
		{
			Name: fileNameTimestamp,
			Mode: fuse.S_IFREG,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(bn.blockNum, 10) + "/" + fileNameTimestamp),
		},
		{
			Name: dirNameTxs,
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/blocks/" + strconv.FormatUint(bn.blockNum, 10) + "/" + dirNameTxs),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements NodeGetattrer for BlockNode.
func (bn *BlockNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

package fs

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math/big"
	"strconv"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
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
	cacheKeyBlockPrefix = "block:"
	cacheKeyLatestBlock = "chain/latest_block"
	fileNameInfoJSON    = "info.json"
	fileNameHash        = "hash"
	fileNameParentHash  = "parent_hash"
	fileNameTimestamp   = "timestamp"
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
func fetchLatestBlockNumber(ctx context.Context, rpc rpc.RPCClient, c *cache.Cache) (uint64, error) {
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
	c.Set(cacheKeyLatestBlock, buf[:], cache.TTLChainLatestBlock)
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
	if latest-blockNum >= 12 {
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
			Ino:  hashIno("/blocks/" + name),
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
		Ino:  hashIno("/blocks/" + name),
		Mode: fuse.S_IFDIR,
	})
	out.Mode = fuse.S_IFDIR | 0o755
	return ch, 0
}

// Lookup implements NodeLookuper.
// Called when the kernel looks up a child entry (e.g., ls /blocks/21000000).
// name can be either "recent" or a block number as decimal string.
// Returns a child node (directory or file) or ENOENT.
func (b *BlocksDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "recent" {
		child := NewRecentDirNode(b.rpc, b.cache)
		ch := b.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/blocks/recent"),
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
// Returns directory entries for /blocks (only "recent" subdirectory).
// The kernel will call Lookup on each name we return.
func (b *BlocksDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{
			Name: "recent",
			Mode: fuse.S_IFDIR,
			Ino:  hashIno("/blocks/recent"),
		},
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements NodeGetattrer for BlocksDirNode.
func (b *BlocksDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
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

// BlockFileNode represents a file inside a block directory.
type BlockFileNode struct {
	fs.Inode
	rpc      rpc.RPCClient
	cache    *cache.Cache
	blockNum uint64
	fileType string // one of fileNameInfoJSON, fileNameHash, fileNameParentHash, fileNameTimestamp
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
	// Ensure newline at end
	if !bytes.HasSuffix(content, []byte{'\n'}) {
		content = append(content, '\n')
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
// Returns child file nodes: info.json, hash, parent_hash, timestamp.
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
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements NodeGetattrer for BlockNode.
func (bn *BlockNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

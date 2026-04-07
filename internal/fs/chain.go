package fs

import (
	"context"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

const (
	cacheKeyChainID   = "chain/id"
	cacheKeyChainName = "chain/name"
	cacheKeyGasSlow   = "chain/gas/slow"
	cacheKeyGasStd    = "chain/gas/standard"
	cacheKeyGasFast   = "chain/gas/fast"
)

// ChainNode represents the /chain directory.
// Files: id, name, latest_block
type ChainNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeReaddirer = (*ChainNode)(nil)
var _ fs.NodeGetattrer = (*ChainNode)(nil)
var _ fs.NodeLookuper = (*ChainNode)(nil)

// NewChainNode creates a new ChainNode.
func NewChainNode(rpc rpc.RPCClient, c *cache.Cache) *ChainNode {
	n := &ChainNode{
		rpc:   rpc,
		cache: c,
	}
	// Fetch static data at startup and cache it
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fetch chain ID and cache with long TTL (changes rarely)
	if id, err := rpc.ChainID(ctx); err == nil {
		c.Set(cacheKeyChainID, []byte(id.String()), 24*time.Hour)
	}

	// Fetch chain name from chain ID mapping and cache
	chainID := ""
	if id, err := rpc.ChainID(ctx); err == nil {
		chainID = id.String()
	}
	chainName := chainNameFromID(chainID)
	c.Set(cacheKeyChainName, []byte(chainName), 24*time.Hour)

	return n
}

// chainNameFromID returns a human-readable chain name from chain ID.
func chainNameFromID(id string) string {
	switch id {
	case "1":
		return "Ethereum Mainnet"
	case "17000":
		return "Holesky Testnet"
	case "11155111":
		return "Sepolia Testnet"
	case "56":
		return "BNB Smart Chain"
	case "137":
		return "Polygon"
	case "42161":
		return "Arbitrum One"
	case "10":
		return "Optimism"
	default:
		return "Unknown Chain"
	}
}

// Readdir implements fs.NodeReaddirer.
func (n *ChainNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "id", Mode: fuse.S_IFREG, Ino: hashIno("/chain/id")},
		{Name: "name", Mode: fuse.S_IFREG, Ino: hashIno("/chain/name")},
		{Name: "latest_block", Mode: fuse.S_IFREG, Ino: hashIno("/chain/latest_block")},
		{Name: "gas", Mode: fuse.S_IFDIR, Ino: hashIno("/chain/gas")},
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements fs.NodeGetattrer.
func (n *ChainNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Lookup implements fs.NodeLookuper.
func (n *ChainNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "id":
		return n.newFileInode(ctx, "id", fuse.S_IFREG, func() ([]byte, error) {
			if data, ok := n.cache.Get(cacheKeyChainID); ok {
				return append(data, '\n'), nil
			}
			// Fallback: fetch from RPC if cache miss
			if id, err := n.rpc.ChainID(ctx); err == nil {
				data := []byte(id.String() + "\n")
				n.cache.Set(cacheKeyChainID, data[:len(data)-1], 24*time.Hour)
				return data, nil
			}
			return []byte("\n"), nil
		}), 0
	case "name":
		return n.newFileInode(ctx, "name", fuse.S_IFREG, func() ([]byte, error) {
			if data, ok := n.cache.Get(cacheKeyChainName); ok {
				return append(data, '\n'), nil
			}
			// Fallback: derive from chain ID if cache miss
			chainID := ""
			if id, err := n.rpc.ChainID(ctx); err == nil {
				chainID = id.String()
			}
			chainName := chainNameFromID(chainID)
			data := []byte(chainName + "\n")
			n.cache.Set(cacheKeyChainName, data[:len(data)-1], 24*time.Hour)
			return data, nil
		}), 0
	case "latest_block":
		return n.newFileInode(ctx, "latest_block", fuse.S_IFREG, func() ([]byte, error) {
			num, err := fetchLatestBlockNumber(ctx, n.rpc, n.cache)
			if err != nil {
				return nil, err
			}
			return []byte(strconv.FormatUint(num, 10) + "\n"), nil
		}), 0
	case "gas":
		child := NewGasDirNode(n.rpc, n.cache)
		ch := n.NewPersistentInode(ctx, child, fs.StableAttr{
			Ino:  hashIno("/chain/gas"),
			Mode: fuse.S_IFDIR,
		})
		out.Mode = fuse.S_IFDIR | 0o755
		return ch, 0
	}
	return nil, syscall.ENOENT
}

// newFileInode creates an inode for a file node.
func (n *ChainNode) newFileInode(ctx context.Context, name string, mode uint32, readFn func() ([]byte, error)) *fs.Inode {
	child := &chainFileNode{
		readFn: readFn,
	}
	return n.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/chain/" + name),
		Mode: mode,
	})
}

// chainFileNode is a simple file node that reads from a function.
// It holds no state - all caching is done by the centralized cache layer.
type chainFileNode struct {
	fs.Inode
	readFn func() ([]byte, error)
}

var _ fs.NodeOpener = (*chainFileNode)(nil)
var _ fs.NodeGetattrer = (*chainFileNode)(nil)
var _ fs.NodeReader = (*chainFileNode)(nil)

// Open implements fs.NodeOpener.
func (n *chainFileNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	return nil, 0, 0
}

// Getattr implements fs.NodeGetattrer.
func (n *chainFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFREG | 0o644
	return 0
}

// Read implements fs.NodeReader.
// Calls readFn on every read - caching is handled by the centralized cache layer.
func (n *chainFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, err := n.readFn()
	if err != nil {
		return nil, errnoFromErr(err)
	}

	if off >= int64(len(data)) {
		return fuse.ReadResultData(nil), 0
	}
	return fuse.ReadResultData(data[off:]), 0
}

// GasDirNode represents the /chain/gas directory.
// Files: slow, standard, fast (gwei as decimal string)
type GasDirNode struct {
	fs.Inode
	rpc   rpc.RPCClient
	cache *cache.Cache
}

var _ fs.NodeReaddirer = (*GasDirNode)(nil)
var _ fs.NodeGetattrer = (*GasDirNode)(nil)
var _ fs.NodeLookuper = (*GasDirNode)(nil)

// NewGasDirNode creates a new GasDirNode.
func NewGasDirNode(rpc rpc.RPCClient, c *cache.Cache) *GasDirNode {
	return &GasDirNode{
		rpc:   rpc,
		cache: c,
	}
}

// Readdir implements fs.NodeReaddirer.
func (n *GasDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "slow", Mode: fuse.S_IFREG, Ino: hashIno("/chain/gas/slow")},
		{Name: "standard", Mode: fuse.S_IFREG, Ino: hashIno("/chain/gas/standard")},
		{Name: "fast", Mode: fuse.S_IFREG, Ino: hashIno("/chain/gas/fast")},
	}
	return fs.NewListDirStream(entries), 0
}

// Getattr implements fs.NodeGetattrer.
func (n *GasDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	return 0
}

// Lookup implements fs.NodeLookuper.
func (n *GasDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var percentile float64
	var cacheKey string

	switch name {
	case "slow":
		percentile = 25
		cacheKey = cacheKeyGasSlow
	case "standard":
		percentile = 50
		cacheKey = cacheKeyGasStd
	case "fast":
		percentile = 75
		cacheKey = cacheKeyGasFast
	default:
		return nil, syscall.ENOENT
	}

	return n.newFileInode(ctx, name, fuse.S_IFREG, func() ([]byte, error) {
		gwei, err := fetchGasPrice(ctx, n.rpc, n.cache, percentile, cacheKey)
		if err != nil {
			return nil, err
		}
		return []byte(gwei + "\n"), nil
	}), 0
}

// newFileInode creates an inode for a gas file node.
func (n *GasDirNode) newFileInode(ctx context.Context, name string, mode uint32, readFn func() ([]byte, error)) *fs.Inode {
	child := &chainFileNode{
		readFn: readFn,
	}
	return n.NewPersistentInode(ctx, child, fs.StableAttr{
		Ino:  hashIno("/chain/gas/" + name),
		Mode: mode,
	})
}

// fetchGasPrice fetches gas price for a given percentile using eth_feeHistory.
// Returns gwei as a decimal string.
func fetchGasPrice(ctx context.Context, rpc rpc.RPCClient, c *cache.Cache, percentile float64, cacheKey string) (string, error) {
	// Try cache first
	if data, ok := c.Get(cacheKey); ok {
		return string(data), nil
	}

	// Fetch fee history
	// blockCount=5, lastBlock="latest", rewardPercentiles=[25, 50, 75]
	feeHistory, err := rpc.FeeHistory(ctx, 5, nil, []float64{25, 50, 75})
	if err != nil {
		return "", err
	}

	// Calculate percentile from reward percentiles
	// feeHistory.Reward is [][]*big.Int, indexed by [block][percentile]
	// We take the median (middle block) of the requested percentile
	if len(feeHistory.Reward) == 0 || len(feeHistory.Reward[0]) < 3 {
		// Fallback: use base fee
		if len(feeHistory.BaseFee) > 0 {
			baseFee := feeHistory.BaseFee[len(feeHistory.BaseFee)-1]
			gwei := weiToGweiString(baseFee)
			c.Set(cacheKey, []byte(gwei), cache.TTLChainGas)
			return gwei, nil
		}
		return "0", nil
	}

	// Collect all rewards for the requested percentile across blocks
	rewards := make([]*big.Int, 0, len(feeHistory.Reward))
	percentileIdx := percentileIndex(percentile)

	for _, blockRewards := range feeHistory.Reward {
		if percentileIdx < len(blockRewards) && blockRewards[percentileIdx] != nil {
			rewards = append(rewards, blockRewards[percentileIdx])
		}
	}

	if len(rewards) == 0 {
		// Fallback to base fee
		if len(feeHistory.BaseFee) > 0 {
			baseFee := feeHistory.BaseFee[len(feeHistory.BaseFee)-1]
			gwei := weiToGweiString(baseFee)
			c.Set(cacheKey, []byte(gwei), cache.TTLChainGas)
			return gwei, nil
		}
		return "0", nil
	}

	// Sort and get the median (or the requested percentile)
	sort.Slice(rewards, func(i, j int) bool {
		return rewards[i].Cmp(rewards[j]) < 0
	})

	// Take median
	median := rewards[len(rewards)/2]
	gwei := weiToGweiString(median)

	// Cache with TTL
	c.Set(cacheKey, []byte(gwei), cache.TTLChainGas)
	return gwei, nil
}

// percentileIndex converts percentile to array index.
func percentileIndex(p float64) int {
	switch {
	case p <= 25:
		return 0
	case p <= 50:
		return 1
	default:
		return 2
	}
}

// weiToGweiString converts wei to gwei and returns as decimal string.
func weiToGweiString(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	gwei := new(big.Float).SetInt(wei)
	gwei.Quo(gwei, big.NewFloat(1e9))
	return gwei.Text('f', 2)
}

// errnoFromErr converts an error to syscall.Errno.
func errnoFromErr(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	if strings.Contains(err.Error(), "not found") {
		return syscall.ENOENT
	}
	if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "too many requests") {
		return syscall.EAGAIN
	}
	return syscall.EIO
}

// startBackgroundRefresh starts a background goroutine to refresh latest_block.
// The goroutine stops when the context is cancelled (e.g., on shutdown).
func startBackgroundRefresh(ctx context.Context, c *cache.Cache, rpc rpc.RPCClient, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Fetch and cache latest block number
				_, _ = fetchLatestBlockNumberWithTTL(ctx, rpc, c, interval)
			}
		}
	}()
}

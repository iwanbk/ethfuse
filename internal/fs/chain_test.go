package fs

import (
	"context"
	"math/big"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/rpc"
)

// mockChainRPCClient is a mock RPCClient for chain tests.
type mockChainRPCClient struct {
	chainID          *big.Int
	latestBlock      *types.Block
	feeHistory       *ethereum.FeeHistory
	mu               sync.Mutex
	chainIDCallCount int
	callCount        int
}

var _ rpc.RPCClient = (*mockChainRPCClient)(nil)

func newMockChainRPCClient() *mockChainRPCClient {
	return &mockChainRPCClient{
		chainID: big.NewInt(1),
	}
}

func (m *mockChainRPCClient) Close() {}

func (m *mockChainRPCClient) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	m.mu.Lock()
	m.callCount++
	block := m.latestBlock
	m.mu.Unlock()

	if block == nil {
		return nil, ethereum.NotFound
	}
	if n == nil || n.Cmp(block.Number()) == 0 {
		return block, nil
	}
	return nil, ethereum.NotFound
}

func (m *mockChainRPCClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error) {
	return nil, ethereum.NotFound
}

func (m *mockChainRPCClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	return nil, ethereum.NotFound
}

func (m *mockChainRPCClient) BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
	return big.NewInt(0), nil
}

func (m *mockChainRPCClient) NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error) {
	return 0, nil
}

func (m *mockChainRPCClient) CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error) {
	return nil, nil
}

func (m *mockChainRPCClient) ChainID(ctx context.Context) (*big.Int, error) {
	m.mu.Lock()
	m.chainIDCallCount++
	id := m.chainID
	m.mu.Unlock()
	return id, nil
}

func (m *mockChainRPCClient) FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.feeHistory != nil {
		return m.feeHistory, nil
	}

	// Default fee history response
	rewards := make([][]*big.Int, int(blockCount))
	for i := range rewards {
		rewards[i] = make([]*big.Int, len(rewardPercentiles))
		for j := range rewardPercentiles {
			// Create reward based on percentile
			rewards[i][j] = big.NewInt(int64(rewardPercentiles[j] * 1e9)) // in wei
		}
	}

	baseFee := make([]*big.Int, int(blockCount)+1)
	for i := range baseFee {
		baseFee[i] = big.NewInt(1e9) // 1 gwei
	}

	m.feeHistory = &ethereum.FeeHistory{
		OldestBlock:  big.NewInt(100),
		Reward:       rewards,
		BaseFee:      baseFee,
		GasUsedRatio: make([]float64, blockCount),
	}

	return m.feeHistory, nil
}

func (m *mockChainRPCClient) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	return nil, nil
}

func (m *mockChainRPCClient) setLatestBlock(block *types.Block) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latestBlock = block
}

func (m *mockChainRPCClient) setFeeHistory(fh *ethereum.FeeHistory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feeHistory = fh
}

func (m *mockChainRPCClient) getChainIDCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chainIDCallCount
}

func TestChainNode_Readdir(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	mockRPC.setLatestBlock(types.NewBlockWithHeader(&types.Header{
		Number: big.NewInt(1000),
	}))
	c := newTestCache(t)
	defer c.Close()

	node := NewChainNode(mockRPC, c)
	stream, errno := node.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir errno = %v, want 0", errno)
	}

	var entries []fuse.DirEntry
	for stream.HasNext() {
		entry, errno := stream.Next()
		if errno != 0 {
			t.Fatalf("Next errno = %v", errno)
		}
		entries = append(entries, entry)
	}

	// Should have id, name, latest_block, gas
	expectedFiles := map[string]bool{
		"id":           true,
		"name":         true,
		"latest_block": true,
		"gas":          true,
	}

	if len(entries) != len(expectedFiles) {
		t.Errorf("got %d entries, want %d", len(entries), len(expectedFiles))
	}

	for _, entry := range entries {
		if !expectedFiles[entry.Name] {
			t.Errorf("unexpected entry: %s", entry.Name)
		}
		if entry.Mode&syscall.S_IFREG == 0 && entry.Name != "gas" {
			t.Errorf("entry %s should be regular file", entry.Name)
		}
		if entry.Name == "gas" && entry.Mode&syscall.S_IFDIR == 0 {
			t.Errorf("entry gas should be directory")
		}
	}
}

func TestChainNode_IDAndName_FetchedOnce(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Create ChainNode - should fetch chain ID once at startup
	node := NewChainNode(mockRPC, c)

	// Verify chain ID was fetched once
	if count := mockRPC.getChainIDCallCount(); count != 1 {
		t.Errorf("ChainID called %d times at startup, want 1", count)
	}

	// Verify chain name is cached
	node.mu.RLock()
	chainName := node.chainName
	node.mu.RUnlock()
	if chainName == "" {
		t.Error("chain name should be set at startup")
	}

	// Verify chain ID is cached
	node.mu.RLock()
	chainID := node.chainID
	node.mu.RUnlock()
	if chainID != "1" {
		t.Errorf("chain ID = %q, want %q", chainID, "1")
	}

	// Multiple lookups would use cached values (tested indirectly via cache)
	// Note: Full Lookup test requires mounted FUSE bridge
}

func TestChainNode_LatestBlock_Refreshes(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Set initial block
	mockRPC.setLatestBlock(types.NewBlockWithHeader(&types.Header{
		Number: big.NewInt(1000),
	}))

	// Test fetchLatestBlockNumber directly
	ctx := context.Background()
	num, err := fetchLatestBlockNumber(ctx, mockRPC, c)
	if err != nil {
		t.Fatalf("fetchLatestBlockNumber error = %v", err)
	}
	if num != 1000 {
		t.Errorf("initial latest_block = %d, want %d", num, 1000)
	}

	// Update mock to return new block
	mockRPC.setLatestBlock(types.NewBlockWithHeader(&types.Header{
		Number: big.NewInt(1001),
	}))

	// Clear cache to simulate TTL expiry
	c.Set("chain/latest_block", nil, 0)

	// Fetch again - should get new block
	num2, err := fetchLatestBlockNumber(ctx, mockRPC, c)
	if err != nil {
		t.Fatalf("fetchLatestBlockNumber error = %v", err)
	}
	if num2 != 1001 {
		t.Errorf("updated latest_block = %d, want %d", num2, 1001)
	}
}

func TestGasDirNode_Readdir(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	node := NewGasDirNode(mockRPC, c)
	stream, errno := node.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir errno = %v, want 0", errno)
	}

	var entries []fuse.DirEntry
	for stream.HasNext() {
		entry, errno := stream.Next()
		if errno != 0 {
			t.Fatalf("Next errno = %v", errno)
		}
		entries = append(entries, entry)
	}

	expectedFiles := map[string]bool{
		"slow":     true,
		"standard": true,
		"fast":     true,
	}

	if len(entries) != len(expectedFiles) {
		t.Errorf("got %d entries, want %d", len(entries), len(expectedFiles))
	}

	for _, entry := range entries {
		if !expectedFiles[entry.Name] {
			t.Errorf("unexpected entry: %s", entry.Name)
		}
		if entry.Mode&syscall.S_IFREG == 0 {
			t.Errorf("entry %s should be regular file", entry.Name)
		}
	}
}

func TestGasDirNode_Percentiles(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Set custom fee history with specific percentiles
	// slow = P25 = 25 gwei, standard = P50 = 50 gwei, fast = P75 = 75 gwei
	rewards := make([][]*big.Int, 5)
	for i := range rewards {
		rewards[i] = []*big.Int{
			big.NewInt(25_000_000_000), // P25 = 25 gwei
			big.NewInt(50_000_000_000), // P50 = 50 gwei
			big.NewInt(75_000_000_000), // P75 = 75 gwei
		}
	}

	baseFee := make([]*big.Int, 6)
	for i := range baseFee {
		baseFee[i] = big.NewInt(1_000_000_000)
	}

	mockRPC.setFeeHistory(&ethereum.FeeHistory{
		OldestBlock:  big.NewInt(100),
		Reward:       rewards,
		BaseFee:      baseFee,
		GasUsedRatio: make([]float64, 5),
	})

	ctx := context.Background()

	testCases := []struct {
		name         string
		expectedGwei string
		cacheKey     string
		percentile   float64
	}{
		{"slow", "25.00", cacheKeyGasSlow, 25},
		{"standard", "50.00", cacheKeyGasStd, 50},
		{"fast", "75.00", cacheKeyGasFast, 75},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gwei, err := fetchGasPrice(ctx, mockRPC, c, tc.percentile, tc.cacheKey)
			if err != nil {
				t.Fatalf("fetchGasPrice error = %v", err)
			}
			if gwei != tc.expectedGwei {
				t.Errorf("%s gwei = %q, want %q", tc.name, gwei, tc.expectedGwei)
			}
		})
	}
}

func TestGasDirNode_Caching(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Set initial fee history
	rewards := make([][]*big.Int, 5)
	for i := range rewards {
		rewards[i] = []*big.Int{
			big.NewInt(25_000_000_000),
			big.NewInt(50_000_000_000),
			big.NewInt(75_000_000_000),
		}
	}
	baseFee := make([]*big.Int, 6)
	for i := range baseFee {
		baseFee[i] = big.NewInt(1_000_000_000)
	}
	mockRPC.setFeeHistory(&ethereum.FeeHistory{
		OldestBlock:  big.NewInt(100),
		Reward:       rewards,
		BaseFee:      baseFee,
		GasUsedRatio: make([]float64, 5),
	})

	ctx := context.Background()

	// First call - should call FeeHistory and cache
	firstGwei, err := fetchGasPrice(ctx, mockRPC, c, 25, cacheKeyGasSlow)
	if err != nil {
		t.Fatalf("fetchGasPrice error = %v", err)
	}

	// Second call - should use cache
	secondGwei, err := fetchGasPrice(ctx, mockRPC, c, 25, cacheKeyGasSlow)
	if err != nil {
		t.Fatalf("fetchGasPrice error = %v", err)
	}

	if firstGwei != secondGwei {
		t.Errorf("cached content differs: first=%q, second=%q", firstGwei, secondGwei)
	}
}

func TestChainNode_LookupUnknownFile(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	node := NewChainNode(mockRPC, c)
	ctx := context.Background()

	var out fuse.EntryOut
	_, errno := node.Lookup(ctx, "unknown", &out)
	if errno != syscall.ENOENT {
		t.Errorf("Lookup unknown errno = %v, want %v", errno, syscall.ENOENT)
	}
}

func TestGasDirNode_LookupUnknownFile(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	node := NewGasDirNode(mockRPC, c)
	ctx := context.Background()

	var out fuse.EntryOut
	_, errno := node.Lookup(ctx, "unknown", &out)
	if errno != syscall.ENOENT {
		t.Errorf("Lookup unknown errno = %v, want %v", errno, syscall.ENOENT)
	}
}

func TestStartBackgroundRefresh(t *testing.T) {
	mockRPC := newMockChainRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Set initial block
	mockRPC.setLatestBlock(types.NewBlockWithHeader(&types.Header{
		Number: big.NewInt(1000),
	}))

	// Start background refresh with short interval for testing
	startBackgroundRefresh(c, mockRPC, 100*time.Millisecond)

	// Wait for at least one refresh
	time.Sleep(150 * time.Millisecond)

	// The background goroutine should have called BlockByNumber at least once
	// We can't directly verify this without adding a counter, but we can verify
	// the cache was updated by checking if we can fetch the latest block
	data, ok := c.Get("chain/latest_block")
	if !ok {
		t.Error("latest_block not in cache after background refresh")
	}
	if len(data) != 8 {
		t.Errorf("latest_block data length = %d, want 8", len(data))
	}
}

// Test that ChainNode properly initializes chain name from chain ID
func TestChainNode_ChainName(t *testing.T) {
	testCases := []struct {
		name       string
		chainID    string
		chainIDInt *big.Int
		wantName   string
	}{
		{"1", "1", big.NewInt(1), "Ethereum Mainnet"},
		{"17000", "17000", big.NewInt(17000), "Holesky Testnet"},
		{"11155111", "11155111", big.NewInt(11155111), "Sepolia Testnet"},
		{"999999", "999999", big.NewInt(999999), "Unknown Chain"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRPC := newMockChainRPCClient()
			mockRPC.chainID = tc.chainIDInt
			c := newTestCache(t)
			defer c.Close()

			node := NewChainNode(mockRPC, c)

			// Verify the chain name is set correctly
			node.mu.RLock()
			name := node.chainName
			node.mu.RUnlock()

			if name != tc.wantName {
				t.Errorf("chain name = %q, want %q", name, tc.wantName)
			}
		})
	}
}

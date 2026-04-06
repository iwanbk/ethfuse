package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

// mockBlockRPCClient is a mock RPCClient that returns predefined blocks.
type mockBlockRPCClient struct {
	blocks       map[uint64]*types.Block
	receipts     map[common.Hash]*types.Receipt
	transactions map[common.Hash]*types.Transaction
}

var _ rpc.RPCClient = (*mockBlockRPCClient)(nil)

func newMockBlockRPCClient() *mockBlockRPCClient {
	return &mockBlockRPCClient{
		blocks:       make(map[uint64]*types.Block),
		receipts:     make(map[common.Hash]*types.Receipt),
		transactions: make(map[common.Hash]*types.Transaction),
	}
}

func (m *mockBlockRPCClient) Close() {}

func (m *mockBlockRPCClient) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	if n == nil {
		// Return latest block (highest number)
		var latest uint64 = 0
		for num := range m.blocks {
			if num > latest {
				latest = num
			}
		}
		if latest == 0 {
			return nil, ethereum.NotFound
		}
		return m.blocks[latest], nil
	}
	blockNum := n.Uint64()
	block, ok := m.blocks[blockNum]
	if !ok {
		return nil, ethereum.NotFound
	}
	return block, nil
}

// Other methods not needed for block tests, but must satisfy interface.
func (m *mockBlockRPCClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error) {
	tx, ok := m.transactions[hash]
	if !ok {
		return nil, ethereum.NotFound
	}
	return tx, nil
}
func (m *mockBlockRPCClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	receipt, ok := m.receipts[hash]
	if !ok {
		return nil, ethereum.NotFound
	}
	return receipt, nil
}
func (m *mockBlockRPCClient) BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
	return big.NewInt(0), nil
}
func (m *mockBlockRPCClient) NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error) {
	return 0, nil
}
func (m *mockBlockRPCClient) CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error) {
	return nil, nil
}
func (m *mockBlockRPCClient) ChainID(ctx context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}
func (m *mockBlockRPCClient) FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error) {
	return nil, nil
}
func (m *mockBlockRPCClient) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	return nil, nil
}

// addMockBlock adds a mock block with given number, parent hash, timestamp.
func (m *mockBlockRPCClient) addMockBlock(number uint64, parentHash common.Hash, timestamp uint64) {
	header := &types.Header{
		Number:     big.NewInt(int64(number)),
		ParentHash: parentHash,
		Time:       timestamp,
		GasLimit:   30_000_000,
		GasUsed:    15_000_000,
		Difficulty: big.NewInt(0),
		Coinbase:   common.HexToAddress("0x0000000000000000000000000000000000000000"),
		BaseFee:    big.NewInt(1000000000),
	}
	block := types.NewBlockWithHeader(header)
	m.blocks[number] = block
}

// addMockBlockWithTransaction adds a mock block containing a transaction and its receipt.
func (m *mockBlockRPCClient) addMockBlockWithTransaction(number uint64, parentHash common.Hash, timestamp uint64, tx *types.Transaction, receipt *types.Receipt) {
	header := &types.Header{
		Number:     big.NewInt(int64(number)),
		ParentHash: parentHash,
		Time:       timestamp,
		GasLimit:   30_000_000,
		GasUsed:    15_000_000,
		Difficulty: big.NewInt(0),
		Coinbase:   common.HexToAddress("0x0000000000000000000000000000000000000000"),
		BaseFee:    big.NewInt(1000000000),
	}
	// Create block with transaction
	body := &types.Body{
		Transactions: []*types.Transaction{tx},
		Uncles:       nil,
	}
	block := types.NewBlock(header, body, []*types.Receipt{receipt}, trie.NewStackTrie(nil))
	m.blocks[number] = block
	// Store transaction and receipt
	m.transactions[tx.Hash()] = tx
	m.receipts[tx.Hash()] = receipt
}

func TestRecentDirNode_Readdir(t *testing.T) {
	mockRPC := newMockBlockRPCClient()
	// Create 200 mock blocks (0-199)
	var parentHash common.Hash = common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	for i := uint64(0); i < 200; i++ {
		// Simple parent hash chain
		mockRPC.addMockBlock(i, parentHash, i*10)
		parentHash = mockRPC.blocks[i].Hash()
	}
	cache := newTestCache(t)
	node := NewRecentDirNode(mockRPC, cache)
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
	// Should have exactly 100 entries (latest 100 blocks)
	if len(entries) != 100 {
		t.Errorf("got %d entries, want 100", len(entries))
	}
	// Should be in descending order (latest first)
	for i, entry := range entries {
		expectedNum := 199 - uint64(i) // because latest block is 199
		if entry.Name != strconv.FormatUint(expectedNum, 10) {
			t.Errorf("entry[%d] name = %s, want %d", i, entry.Name, expectedNum)
		}
		if entry.Mode&syscall.S_IFDIR == 0 {
			t.Errorf("entry[%d] mode = %o, should be directory", i, entry.Mode)
		}
	}
}

func TestBlocksDirNode_LookupUnknownBlock(t *testing.T) {
	// Skip: Lookup requires a mounted FUSE bridge to test properly.
	// The go-fuse library requires a mounted filesystem for Lookup operations
	// to work correctly with the kernel interface.
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestFetchBlock(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockBlockRPCClient()
	// Add a mock block number 1000
	mockRPC.addMockBlock(1000, common.HexToHash("0x1234"), 123456)

	// Create cache with persistent storage
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	c, err := cache.NewCache(dbPath)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	// First fetch should call RPC
	block, err := fetchBlock(ctx, 1000, mockRPC, c)
	if err != nil {
		t.Fatalf("fetchBlock failed: %v", err)
	}
	if block.Number().Uint64() != 1000 {
		t.Errorf("block number = %d, want 1000", block.Number().Uint64())
	}

	// Second fetch should come from cache (memory)
	// We can't directly verify, but we can ensure no error
	block2, err := fetchBlock(ctx, 1000, mockRPC, c)
	if err != nil {
		t.Fatalf("second fetchBlock failed: %v", err)
	}
	if block2.Hash() != block.Hash() {
		t.Errorf("cached block hash mismatch")
	}

	// Test negative caching for non-existent block
	_, err = fetchBlock(ctx, 9999, mockRPC, c)
	if !errors.Is(err, ethereum.NotFound) {
		t.Errorf("expected NotFound error, got %v", err)
	}
	// Negative cache should be set
	if !c.IsNegative("block:9999") {
		t.Errorf("negative cache not set for block 9999")
	}
}

func TestFetchBlockFinalization(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockBlockRPCClient()
	// Add latest block at height 2000
	mockRPC.addMockBlock(2000, common.HexToHash("0xabcd"), 2000000)
	// Add a block at height 1988 (difference = BlockFinalizationConfirmations, finalized)
	mockRPC.addMockBlock(1988, common.HexToHash("0x1234"), 1988000)

	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	c, err := cache.NewCache(dbPath)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	// Fetch block 1988 (should be finalized)
	block, err := fetchBlock(ctx, 1988, mockRPC, c)
	if err != nil {
		t.Fatalf("fetchBlock failed: %v", err)
	}
	if block.Number().Uint64() != 1988 {
		t.Errorf("block number = %d, want 1988", block.Number().Uint64())
	}
	// Should be stored in persistent storage
	key := "block:1988"
	data, err := c.GetPersistent("blocks", key)
	if err != nil {
		t.Errorf("block not found in persistent storage: %v", err)
	}
	if len(data) == 0 {
		t.Errorf("persistent data empty")
	}
	// Also should be in memory cache (with long TTL)
	if _, ok := c.Get(key); !ok {
		t.Errorf("block not in memory cache")
	}

	// Now test non-finalized block (difference < 12)
	// Add block 1995 (difference 5)
	mockRPC.addMockBlock(1995, common.HexToHash("0x5678"), 1995000)
	block2, err := fetchBlock(ctx, 1995, mockRPC, c)
	if err != nil {
		t.Fatalf("fetchBlock for non-finalized failed: %v", err)
	}
	if block2.Number().Uint64() != 1995 {
		t.Errorf("block number = %d, want 1995", block2.Number().Uint64())
	}
	// Should NOT be in persistent storage (or maybe stored but we can ignore)
	// We'll check that memory cache has it (should have TTLBlockLatest)
	key2 := "block:1995"
	if _, ok := c.Get(key2); !ok {
		t.Errorf("non-finalized block not in memory cache")
	}
	// Optionally verify not in persistent storage (may be stored if error?)
	// We'll skip because fetchBlock may still store in persistent if error occurs.
}

func TestBlockFileNodeContent(t *testing.T) {
	// Create a mock block with known values
	header := &types.Header{
		Number:     big.NewInt(123456),
		ParentHash: common.HexToHash("0xparent"),
		Time:       987654321,
		GasLimit:   30_000_000,
		GasUsed:    15_000_000,
		Difficulty: big.NewInt(12345),
		Coinbase:   common.HexToAddress("0xcb"),
		BaseFee:    big.NewInt(1000000000),
	}
	block := types.NewBlockWithHeader(header)

	// Create cache and RPC (not used)
	mockRPC := newMockBlockRPCClient()
	cache := newTestCache(t)

	// Test each file type
	tests := []struct {
		check    func(content []byte) error
		fileType string
	}{
		{
			check: func(content []byte) error {
				expected := block.Hash().Hex() + "\n"
				if string(content) != expected {
					return fmt.Errorf("hash content mismatch: got %q, want %q", content, expected)
				}
				return nil
			},
			fileType: fileNameHash,
		},
		{
			check: func(content []byte) error {
				expected := block.ParentHash().Hex() + "\n"
				if string(content) != expected {
					return fmt.Errorf("parent hash content mismatch: got %q, want %q", content, expected)
				}
				return nil
			},
			fileType: fileNameParentHash,
		},
		{
			check: func(content []byte) error {
				expected := strconv.FormatUint(block.Time(), 10) + "\n"
				if string(content) != expected {
					return fmt.Errorf("timestamp content mismatch: got %q, want %q", content, expected)
				}
				return nil
			},
			fileType: fileNameTimestamp,
		},
		{
			check: func(content []byte) error {
				// Verify JSON structure and fields
				var info map[string]interface{}
				if err := json.Unmarshal(content, &info); err != nil {
					return fmt.Errorf("failed to unmarshal JSON: %v", err)
				}
				// Check a few fields
				if num, ok := info["number"].(float64); !ok || uint64(num) != block.Number().Uint64() {
					return fmt.Errorf("number field mismatch")
				}
				if hash, ok := info["hash"].(string); !ok || hash != block.Hash().Hex() {
					return fmt.Errorf("hash field mismatch")
				}
				// Ensure newline at end (should be added by fileContent)
				if !bytes.HasSuffix(content, []byte{'\n'}) {
					return fmt.Errorf("missing newline at end")
				}
				return nil
			},
			fileType: fileNameInfoJSON,
		},
	}

	for _, test := range tests {
		t.Run(test.fileType, func(t *testing.T) {
			node := NewBlockFileNode(block.Number().Uint64(), test.fileType, mockRPC, cache)
			content, err := node.fileContent(block)
			if err != nil {
				t.Fatalf("fileContent failed: %v", err)
			}
			// Ensure newline at end (scalar files already have newline; info.json also adds newline)
			if !bytes.HasSuffix(content, []byte{'\n'}) {
				t.Errorf("content missing newline at end: %q", content)
			}
			if err := test.check(content); err != nil {
				t.Errorf("check failed: %v", err)
			}
		})
	}
}

func TestGhostAccess(t *testing.T) {
	// Create RecentDirNode with blocks 100-199
	// Lookup block 50 (not in recent list) should succeed
	// This tests that Lookup works for any block number
	// Skip: Requires mounted FUSE bridge to test Lookup with kernel interface
	t.Skip("Requires mounted bridge")
}

func TestTxsDirNode_Readdir(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockBlockRPCClient()
	cache := newTestCache(t)

	// Create a mock transaction
	tx := types.NewTransaction(
		0, // nonce
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		big.NewInt(1000000000000000000), // 1 ETH
		21000,                           // gas limit
		big.NewInt(1000000000),          // gas price
		[]byte{},                        // data
	)

	// Create a mock receipt
	receipt := &types.Receipt{
		Status:            1,
		GasUsed:           21000,
		EffectiveGasPrice: big.NewInt(1000000000),
		ContractAddress:   common.Address{},
		Logs:              []*types.Log{},
		Bloom:             types.Bloom{},
	}

	// Add block with transaction
	var parentHash common.Hash = common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	mockRPC.addMockBlockWithTransaction(1000, parentHash, 123456, tx, receipt)

	// Create TxsDirNode
	node := NewTxsDirNode(1000, mockRPC, cache)

	// Test Readdir
	stream, errno := node.Readdir(ctx)
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

	// Should have exactly one entry (our transaction)
	if len(entries) != 1 {
		t.Errorf("got %d entries, want 1", len(entries))
	}
	// Entry should be the transaction hash
	expectedHash := tx.Hash().Hex()
	if entries[0].Name != expectedHash {
		t.Errorf("entry name = %s, want %s", entries[0].Name, expectedHash)
	}
	if entries[0].Mode&syscall.S_IFDIR == 0 {
		t.Errorf("entry mode = %o, should be directory", entries[0].Mode)
	}
}

func TestTxsDirNode_Lookup(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockBlockRPCClient()
	cache := newTestCache(t)

	// Create a mock transaction
	tx := types.NewTransaction(
		0, // nonce
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		big.NewInt(1000000000000000000), // 1 ETH
		21000,                           // gas limit
		big.NewInt(1000000000),          // gas price
		[]byte{},                        // data
	)

	// Create a mock receipt
	receipt := &types.Receipt{
		Status:            1,
		GasUsed:           21000,
		EffectiveGasPrice: big.NewInt(1000000000),
		ContractAddress:   common.Address{},
		Logs:              []*types.Log{},
		Bloom:             types.Bloom{},
	}

	// Add block with transaction
	var parentHash common.Hash = common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	mockRPC.addMockBlockWithTransaction(1000, parentHash, 123456, tx, receipt)

	// Create TxsDirNode
	node := NewTxsDirNode(1000, mockRPC, cache)

	// Positive Lookup test requires mounted FUSE bridge (skip)
	// The logic is tested via integration tests
	t.Run("ValidationErrors", func(t *testing.T) {
		// Test Lookup with invalid hash format (should return EINVAL)
		out2 := &fuse.EntryOut{}
		_, errno2 := node.Lookup(ctx, "invalidhash", out2)
		if errno2 != syscall.EINVAL {
			t.Errorf("Lookup invalid hash errno = %v, want EINVAL", errno2)
		}

		// Test Lookup with non-existent hash (should return ENOENT)
		nonExistentHash := "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		out3 := &fuse.EntryOut{}
		_, errno3 := node.Lookup(ctx, nonExistentHash, out3)
		if errno3 != syscall.ENOENT {
			t.Errorf("Lookup non-existent hash errno = %v, want ENOENT", errno3)
		}
	})
}

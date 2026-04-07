package fs

import (
	"context"
	"math/big"
	"sync"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/rpc"
)

// mockAccountRPCClient is a mock RPCClient that returns predefined account data.
type mockAccountRPCClient struct {
	blocks       map[uint64]interface{}
	receipts     map[common.Hash]interface{}
	transactions map[common.Hash]interface{}
	balances     map[common.Address]*big.Int
	nonces       map[common.Address]uint64
	codes        map[common.Address][]byte
	callCounts   map[string]int
	mu           sync.Mutex
}

var _ rpc.RPCClient = (*mockAccountRPCClient)(nil)

func newMockAccountRPCClient() *mockAccountRPCClient {
	return &mockAccountRPCClient{
		blocks:       make(map[uint64]interface{}),
		receipts:     make(map[common.Hash]interface{}),
		transactions: make(map[common.Hash]interface{}),
		balances:     make(map[common.Address]*big.Int),
		nonces:       make(map[common.Address]uint64),
		codes:        make(map[common.Address][]byte),
		callCounts:   make(map[string]int),
	}
}

func (m *mockAccountRPCClient) Close() {}

func (m *mockAccountRPCClient) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	return nil, ethereum.NotFound
}

func (m *mockAccountRPCClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error) {
	return nil, ethereum.NotFound
}

func (m *mockAccountRPCClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	return nil, ethereum.NotFound
}

func (m *mockAccountRPCClient) BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
	m.mu.Lock()
	m.callCounts["BalanceAt"]++
	m.mu.Unlock()

	balance, ok := m.balances[addr]
	if !ok {
		return big.NewInt(0), nil
	}
	return balance, nil
}

func (m *mockAccountRPCClient) NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error) {
	m.mu.Lock()
	m.callCounts["NonceAt"]++
	m.mu.Unlock()

	nonce, ok := m.nonces[addr]
	if !ok {
		return 0, nil
	}
	return nonce, nil
}

func (m *mockAccountRPCClient) CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error) {
	m.mu.Lock()
	m.callCounts["CodeAt"]++
	m.mu.Unlock()

	code, ok := m.codes[addr]
	if !ok {
		return nil, nil // EOA: empty code
	}
	return code, nil
}

func (m *mockAccountRPCClient) ChainID(ctx context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}

func (m *mockAccountRPCClient) FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error) {
	return nil, nil
}

func (m *mockAccountRPCClient) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	return nil, nil
}

func (m *mockAccountRPCClient) getCallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCounts[method]
}

func (m *mockAccountRPCClient) resetCallCounts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCounts = make(map[string]int)
}

// setEOA sets up an EOA account with given balance and nonce (empty code).
func (m *mockAccountRPCClient) setEOA(addr common.Address, balance *big.Int, nonce uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balances[addr] = balance
	m.nonces[addr] = nonce
	m.codes[addr] = nil // EOA has no code
}

// setContract sets up a contract account with given balance, nonce, and code.
func (m *mockAccountRPCClient) setContract(addr common.Address, balance *big.Int, nonce uint64, code []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balances[addr] = balance
	m.nonces[addr] = nonce
	m.codes[addr] = code
}

func TestAccountsDirNode_ReaddirEmpty(t *testing.T) {
	mockRPC := newMockAccountRPCClient()
	c := newTestCache(t)
	defer c.Close()

	node := NewAccountsDirNode(mockRPC, c)
	stream, errno := node.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir errno = %v, want 0", errno)
	}
	if stream.HasNext() {
		t.Error("Readdir should return empty stream for ghost directory")
	}
}

func TestAccountsDirNode_LookupInvalidAddress(t *testing.T) {
	mockRPC := newMockAccountRPCClient()
	c := newTestCache(t)
	defer c.Close()

	node := NewAccountsDirNode(mockRPC, c)

	// Test various invalid address formats
	invalidAddresses := []string{
		"invalid",
		"0x1234",     // too short
		"1234567890", // no 0x prefix
		"0xGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG", // invalid hex chars
		"", // empty
	}

	for _, addr := range invalidAddresses {
		out := &fuse.EntryOut{}
		_, errno := node.Lookup(context.Background(), addr, out)
		if errno != syscall.EINVAL {
			t.Errorf("Lookup(%q) errno = %v, want EINVAL", addr, errno)
		}
	}
}

func TestAccountsDirNode_LookupValidAddress(t *testing.T) {
	// Skip: Lookup requires a mounted FUSE bridge to test properly.
	// The go-fuse library requires a mounted filesystem for Lookup operations
	// to work correctly with the kernel interface.
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestAccountNode_Readdir(t *testing.T) {
	mockRPC := newMockAccountRPCClient()
	c := newTestCache(t)
	defer c.Close()

	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	node := NewAccountNode(addr, mockRPC, c)

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

	// Should have exactly 3 entries: balance, nonce, code
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}

	expectedNames := map[string]bool{
		fileNameBalance: false,
		fileNameNonce:   false,
		fileNameCode:    false,
	}

	for _, entry := range entries {
		if _, ok := expectedNames[entry.Name]; !ok {
			t.Errorf("unexpected entry: %s", entry.Name)
		} else {
			expectedNames[entry.Name] = true
		}
		if entry.Mode&syscall.S_IFREG == 0 {
			t.Errorf("entry %s mode = %o, should be regular file", entry.Name, entry.Mode)
		}
	}

	for name, found := range expectedNames {
		if !found {
			t.Errorf("missing entry: %s", name)
		}
	}
}

func TestAccountNode_Lookup(t *testing.T) {
	// Skip: Lookup requires a mounted FUSE bridge to test properly.
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestAccountFileNode_EOAContent(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockAccountRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Set up an EOA with non-zero balance and nonce
	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	balance, _ := new(big.Int).SetString("1234560000000000000000", 10) // 1234.56 ETH in wei
	nonce := uint64(382)
	mockRPC.setEOA(addr, balance, nonce)

	// Test balance file
	balanceNode := NewAccountFileNode(addr, fileNameBalance, mockRPC, c)
	balanceContent, err := balanceNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("balanceContent error: %v", err)
	}
	expectedBalance := balance.String() + "\n"
	if string(balanceContent) != expectedBalance {
		t.Errorf("balance content = %q, want %q", balanceContent, expectedBalance)
	}

	// Test nonce file
	nonceNode := NewAccountFileNode(addr, fileNameNonce, mockRPC, c)
	nonceContent, err := nonceNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("nonceContent error: %v", err)
	}
	expectedNonce := "382\n"
	if string(nonceContent) != expectedNonce {
		t.Errorf("nonce content = %q, want %q", nonceContent, expectedNonce)
	}

	// Test code file (EOA should return exactly "0x\n")
	codeNode := NewAccountFileNode(addr, fileNameCode, mockRPC, c)
	codeContent, err := codeNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("codeContent error: %v", err)
	}
	expectedCode := "0x\n"
	if string(codeContent) != expectedCode {
		t.Errorf("code content = %q, want %q", codeContent, expectedCode)
	}
}

func TestAccountFileNode_ContractContent(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockAccountRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Set up a contract account (USDC on mainnet)
	addr := common.HexToAddress("0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	balance := big.NewInt(0)
	nonce := uint64(1)
	// Simplified bytecode that starts with 0x6080 (PUSH1 0x80)
	code := []byte{0x60, 0x80, 0x60, 0x40, 0x52, 0x36}
	mockRPC.setContract(addr, balance, nonce, code)

	// Test code file
	codeNode := NewAccountFileNode(addr, fileNameCode, mockRPC, c)
	codeContent, err := codeNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("codeContent error: %v", err)
	}

	// Code should start with "0x6080..."
	expectedPrefix := "0x6080"
	if len(codeContent) < len(expectedPrefix) {
		t.Errorf("code content too short: %q", codeContent)
	} else if string(codeContent[:len(expectedPrefix)]) != expectedPrefix {
		t.Errorf("code content prefix = %q, want %q", codeContent[:len(expectedPrefix)], expectedPrefix)
	}

	// Verify the full hex encoding
	expectedCode := "0x608060405236\n"
	if string(codeContent) != expectedCode {
		t.Errorf("code content = %q, want %q", codeContent, expectedCode)
	}
}

func TestAccountFileNode_CacheBehavior(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockAccountRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Set up an account
	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	balance := big.NewInt(1000000000000000000) // 1 ETH
	nonce := uint64(10)
	mockRPC.setEOA(addr, balance, nonce)

	// First read should trigger RPC call
	balanceNode := NewAccountFileNode(addr, fileNameBalance, mockRPC, c)
	_, err := balanceNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("first balanceContent error: %v", err)
	}
	firstCallCount := mockRPC.getCallCount("BalanceAt")
	if firstCallCount != 1 {
		t.Errorf("first read triggered %d BalanceAt RPC calls, want 1", firstCallCount)
	}

	// Second read within TTL should NOT trigger RPC call (use cache)
	mockRPC.resetCallCounts()
	_, err = balanceNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("second balanceContent error: %v", err)
	}
	secondCallCount := mockRPC.getCallCount("BalanceAt")
	if secondCallCount != 0 {
		t.Errorf("second read within TTL triggered %d BalanceAt RPC calls, want 0", secondCallCount)
	}

	// Test nonce caching
	mockRPC.resetCallCounts()
	nonceNode := NewAccountFileNode(addr, fileNameNonce, mockRPC, c)
	_, err = nonceNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("first nonceContent error: %v", err)
	}
	firstNonceCallCount := mockRPC.getCallCount("NonceAt")
	if firstNonceCallCount != 1 {
		t.Errorf("first nonce read triggered %d NonceAt RPC calls, want 1", firstNonceCallCount)
	}

	mockRPC.resetCallCounts()
	_, err = nonceNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("second nonceContent error: %v", err)
	}
	secondNonceCallCount := mockRPC.getCallCount("NonceAt")
	if secondNonceCallCount != 0 {
		t.Errorf("second nonce read within TTL triggered %d NonceAt RPC calls, want 0", secondNonceCallCount)
	}

	// Test code caching
	mockRPC.resetCallCounts()
	codeNode := NewAccountFileNode(addr, fileNameCode, mockRPC, c)
	_, err = codeNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("first codeContent error: %v", err)
	}
	firstCodeCallCount := mockRPC.getCallCount("CodeAt")
	if firstCodeCallCount != 1 {
		t.Errorf("first code read triggered %d CodeAt RPC calls, want 1", firstCodeCallCount)
	}

	mockRPC.resetCallCounts()
	_, err = codeNode.fileContent(ctx)
	if err != nil {
		t.Fatalf("second codeContent error: %v", err)
	}
	secondCodeCallCount := mockRPC.getCallCount("CodeAt")
	if secondCodeCallCount != 0 {
		t.Errorf("second code read within TTL triggered %d CodeAt RPC calls, want 0", secondCodeCallCount)
	}
}

func TestAccountFileNode_ContentTermination(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockAccountRPCClient()
	c := newTestCache(t)
	defer c.Close()

	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	mockRPC.setEOA(addr, big.NewInt(1000), uint64(5))

	// All scalar files should be terminated with newline
	tests := []string{fileNameBalance, fileNameNonce, fileNameCode}

	for _, fileType := range tests {
		t.Run(fileType, func(t *testing.T) {
			node := NewAccountFileNode(addr, fileType, mockRPC, c)
			content, err := node.fileContent(ctx)
			if err != nil {
				t.Fatalf("fileContent error: %v", err)
			}
			if len(content) == 0 {
				t.Fatalf("content is empty")
			}
			if content[len(content)-1] != '\n' {
				t.Errorf("content not terminated with newline: %q", content)
			}
		})
	}
}

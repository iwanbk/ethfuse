package fs

import (
	"context"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

// mockAccountRPCClient is a mock RPCClient that returns predefined account data.
type mockAccountRPCClient struct {
	balances     map[common.Address]*big.Int
	nonces       map[common.Address]uint64
	codes        map[common.Address][]byte
	balanceCalls map[common.Address]int
	nonceCalls   map[common.Address]int
	codeCalls    map[common.Address]int
	mu           sync.RWMutex
}

var _ rpc.RPCClient = (*mockAccountRPCClient)(nil)

func newMockAccountRPCClient() *mockAccountRPCClient {
	return &mockAccountRPCClient{
		balances:     make(map[common.Address]*big.Int),
		nonces:       make(map[common.Address]uint64),
		codes:        make(map[common.Address][]byte),
		balanceCalls: make(map[common.Address]int),
		nonceCalls:   make(map[common.Address]int),
		codeCalls:    make(map[common.Address]int),
	}
}

func (m *mockAccountRPCClient) Close() {}

func (m *mockAccountRPCClient) BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
	m.mu.Lock()
	m.balanceCalls[addr]++
	m.mu.Unlock()
	m.mu.RLock()
	defer m.mu.RUnlock()
	balance, ok := m.balances[addr]
	if !ok {
		return big.NewInt(0), nil // default zero balance
	}
	return balance, nil
}

func (m *mockAccountRPCClient) NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error) {
	m.mu.Lock()
	m.nonceCalls[addr]++
	m.mu.Unlock()
	m.mu.RLock()
	defer m.mu.RUnlock()
	nonce, ok := m.nonces[addr]
	if !ok {
		return 0, nil // default zero nonce
	}
	return nonce, nil
}

func (m *mockAccountRPCClient) CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error) {
	m.mu.Lock()
	m.codeCalls[addr]++
	m.mu.Unlock()
	m.mu.RLock()
	defer m.mu.RUnlock()
	code, ok := m.codes[addr]
	if !ok {
		return nil, nil // default empty code (EOA)
	}
	return code, nil
}

// Other RPC methods not needed for account tests, but must satisfy interface.
func (m *mockAccountRPCClient) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	return nil, ethereum.NotFound
}
func (m *mockAccountRPCClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error) {
	return nil, ethereum.NotFound
}
func (m *mockAccountRPCClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	return nil, ethereum.NotFound
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

func (m *mockAccountRPCClient) setBalance(addr common.Address, balance *big.Int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balances[addr] = balance
}

func (m *mockAccountRPCClient) setNonce(addr common.Address, nonce uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nonces[addr] = nonce
}

func (m *mockAccountRPCClient) setCode(addr common.Address, code []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[addr] = code
}

func TestAccountsDirNode_Lookup_InvalidAddress(t *testing.T) {
	mockRPC := newMockAccountRPCClient()
	c, err := cache.NewCache("")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	node := NewAccountsDirNode(mockRPC, c)
	ctx := context.Background()

	// Invalid address
	out := &fuse.EntryOut{}
	_, errno := node.Lookup(ctx, "not-an-address", out)
	if errno != syscall.EINVAL {
		t.Fatalf("Expected EINVAL, got %v", errno)
	}
}

func TestAccountNode_EOA(t *testing.T) {
	mockRPC := newMockAccountRPCClient()
	c, err := cache.NewCache("")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	balance := big.NewInt(1234567890123456789) // 1.234567890123456789 ETH
	nonce := uint64(42)
	// code empty for EOA
	mockRPC.setBalance(addr, balance)
	mockRPC.setNonce(addr, nonce)
	mockRPC.setCode(addr, nil)

	node := NewAccountNode(addr, mockRPC, c)
	ctx := context.Background()

	// Test Readdir lists balance, nonce, code (just ensure no error)
	_, errno := node.Readdir(ctx)
	if errno != 0 {
		t.Fatalf("Readdir failed with errno %v", errno)
	}

	// Test reading balance file
	balanceNode := NewAccountFileNode(addr, "balance", mockRPC, c)
	balanceContent, err := balanceNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedBalance := balance.String() + "\n"
	if string(balanceContent) != expectedBalance {
		t.Errorf("Balance content mismatch: got %q, want %q", balanceContent, expectedBalance)
	}

	// Test reading nonce file
	nonceNode := NewAccountFileNode(addr, "nonce", mockRPC, c)
	nonceContent, err := nonceNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedNonce := strconv.FormatUint(nonce, 10) + "\n"
	if string(nonceContent) != expectedNonce {
		t.Errorf("Nonce content mismatch: got %q, want %q", nonceContent, expectedNonce)
	}

	// Test reading code file (EOA)
	codeNode := NewAccountFileNode(addr, "code", mockRPC, c)
	codeContent, err := codeNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedCode := "0x\n"
	if string(codeContent) != expectedCode {
		t.Errorf("Code content mismatch: got %q, want %q", codeContent, expectedCode)
	}
}

func TestAccountNode_Contract(t *testing.T) {
	mockRPC := newMockAccountRPCClient()
	c, err := cache.NewCache("")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	addr := common.HexToAddress("0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	balance := big.NewInt(1000000)
	nonce := uint64(5)
	code := []byte{0x60, 0x80, 0x60, 0x40, 0x52} // sample bytecode
	mockRPC.setBalance(addr, balance)
	mockRPC.setNonce(addr, nonce)
	mockRPC.setCode(addr, code)

	ctx := context.Background()

	// Test reading code file
	codeNode := NewAccountFileNode(addr, "code", mockRPC, c)
	codeContent, err := codeNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedCodeHex := "0x" + strings.ToLower("6080604052") + "\n"
	if string(codeContent) != expectedCodeHex {
		t.Errorf("Code content mismatch: got %q, want %q", codeContent, expectedCodeHex)
	}
}

func TestAccountNode_Cache(t *testing.T) {
	mockRPC := newMockAccountRPCClient()
	c, err := cache.NewCache("")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	balance := big.NewInt(1000000)
	nonce := uint64(7)
	mockRPC.setBalance(addr, balance)
	mockRPC.setNonce(addr, nonce)

	ctx := context.Background()

	// First read should call RPC
	balanceNode := NewAccountFileNode(addr, "balance", mockRPC, c)
	_, err = balanceNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mockRPC.balanceCalls[addr] != 1 {
		t.Errorf("Expected 1 balance call, got %d", mockRPC.balanceCalls[addr])
	}

	// Second read within TTL should not call RPC again
	_, err = balanceNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mockRPC.balanceCalls[addr] != 1 {
		t.Errorf("Expected still 1 balance call after cache, got %d", mockRPC.balanceCalls[addr])
	}

	// Same for nonce
	nonceNode := NewAccountFileNode(addr, "nonce", mockRPC, c)
	_, err = nonceNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mockRPC.nonceCalls[addr] != 1 {
		t.Errorf("Expected 1 nonce call, got %d", mockRPC.nonceCalls[addr])
	}
	_, err = nonceNode.fileContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mockRPC.nonceCalls[addr] != 1 {
		t.Errorf("Expected still 1 nonce call after cache, got %d", mockRPC.nonceCalls[addr])
	}
}

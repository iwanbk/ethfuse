package fs

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/rpc"
)

// mockENSRPCClient is a mock RPCClient that simulates ENS registry and resolver.
type mockENSRPCClient struct {
	// name -> resolver address
	resolvers map[string]common.Address
	// name -> resolved address
	addresses map[string]common.Address
	// call counts
	registryCalls map[string]int
	resolverCalls map[string]int
	mu            sync.RWMutex
}

var _ rpc.RPCClient = (*mockENSRPCClient)(nil)

func newMockENSRPCClient() *mockENSRPCClient {
	return &mockENSRPCClient{
		resolvers:     make(map[string]common.Address),
		addresses:     make(map[string]common.Address),
		registryCalls: make(map[string]int),
		resolverCalls: make(map[string]int),
	}
}

func (m *mockENSRPCClient) Close() {}

func (m *mockENSRPCClient) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	return nil, nil
}

func (m *mockENSRPCClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error) {
	return nil, nil
}

func (m *mockENSRPCClient) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	return nil, nil
}

func (m *mockENSRPCClient) BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
	return big.NewInt(0), nil
}

func (m *mockENSRPCClient) NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error) {
	return 0, nil
}

func (m *mockENSRPCClient) CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error) {
	return nil, nil
}

func (m *mockENSRPCClient) ChainID(ctx context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}

func (m *mockENSRPCClient) FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error) {
	return nil, nil
}

// CallContract simulates ENS registry/resolver calls.
func (m *mockENSRPCClient) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	// Simulate only ENS registry at known address
	registryAddr := common.HexToAddress(ensRegistryAddr)
	if call.To != nil && *call.To == registryAddr {
		// Expect call data for resolver(bytes32)
		if len(call.Data) < 4 {
			return nil, ethereum.NotFound
		}
		// Extract node (bytes32) from call data (skip selector)
		if len(call.Data) != 4+32 {
			return nil, ethereum.NotFound
		}
		node := common.BytesToHash(call.Data[4:])
		// Find name by node (simplified: we only store mapping from name to resolver)
		// For simplicity, we'll iterate over resolvers and compute namehash.
		// This is okay for unit tests where we have few names.
		m.mu.RLock()
		defer m.mu.RUnlock()
		for name := range m.resolvers {
			if namehash(name) == node {
				m.registryCalls[name]++
				resolver := m.resolvers[name]
				if resolver == (common.Address{}) {
					return nil, ethereum.NotFound
				}
				// Pack address as ABI-encoded
				return common.LeftPadBytes(resolver.Bytes(), 32), nil
			}
		}
		return nil, ethereum.NotFound
	}
	// Otherwise assume it's a resolver call
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name := range m.addresses {
		resolverAddr := m.resolvers[name]
		if call.To != nil && *call.To == resolverAddr {
			// Expect call data for addr(bytes32)
			if len(call.Data) < 4 || len(call.Data) != 4+32 {
				return nil, ethereum.NotFound
			}
			node := common.BytesToHash(call.Data[4:])
			if namehash(name) == node {
				m.resolverCalls[name]++
				addr := m.addresses[name]
				if addr == (common.Address{}) {
					return nil, ethereum.NotFound
				}
				return common.LeftPadBytes(addr.Bytes(), 32), nil
			}
		}
	}
	return nil, ethereum.NotFound
}

// setResolver sets the resolver address for a given ENS name.
func (m *mockENSRPCClient) setResolver(name string, resolver common.Address) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolvers[name] = resolver
}

// setAddress sets the resolved address for a given ENS name.
func (m *mockENSRPCClient) setAddress(name string, addr common.Address) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addresses[name] = addr
}

// registryCallCount returns number of registry calls for a name.
func (m *mockENSRPCClient) registryCallCount(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registryCalls[name]
}

// resolverCallCount returns number of resolver calls for a name.
func (m *mockENSRPCClient) resolverCallCount(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.resolverCalls[name]
}

// TestENSDirNode_LookupValid tests that a valid .eth name resolves to a symlink.
// Note: Lookup with successful resolution requires a mounted FUSE bridge because
// it calls NewPersistentInode which needs a bridge. This is consistent with other
// node tests in blocks_test.go and txs_test.go where positive Lookup tests are skipped.
// We test the resolution logic via TestResolveENS_Caching and symlink target via
// TestENSNode_Readlink.
func TestENSDirNode_LookupValid(t *testing.T) {
	t.Skip("Lookup with successful resolution requires mounted bridge, consistent with other node tests")
}

// TestENSDirNode_LookupUnregistered tests that an unregistered name returns ENOENT.
func TestENSDirNode_LookupUnregistered(t *testing.T) {
	mockRPC := newMockENSRPCClient()
	cache := newTestCache(t)
	ensDir := NewENSDirNode(mockRPC, cache)

	ctx := context.TODO()
	var out fuse.EntryOut
	_, errno := ensDir.Lookup(ctx, "nonexistent.eth", &out)
	if errno != syscall.ENOENT {
		t.Errorf("Lookup with unregistered name errno = %v, want ENOENT", errno)
	}
}

// TestENSDirNode_LookupNonETHSuffix tests that a non-.eth suffix returns EINVAL.
func TestENSDirNode_LookupNonETHSuffix(t *testing.T) {
	mockRPC := newMockENSRPCClient()
	cache := newTestCache(t)
	ensDir := NewENSDirNode(mockRPC, cache)

	ctx := context.TODO()
	var out fuse.EntryOut
	_, errno := ensDir.Lookup(ctx, "vitalik.com", &out)
	if errno != syscall.EINVAL {
		t.Errorf("Lookup with non-.eth suffix errno = %v, want EINVAL", errno)
	}
}

// TestENSDirNode_Caching tests that second lookup is served from cache.
func TestResolveENS_Caching(t *testing.T) {
	mockRPC := newMockENSRPCClient()
	cache := newTestCache(t)

	name := "vitalik.eth"
	resolverAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	targetAddr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	mockRPC.setResolver(name, resolverAddr)
	mockRPC.setAddress(name, targetAddr)

	ctx := context.TODO()
	// First call
	addr, err := ResolveENS(ctx, name, mockRPC, cache)
	if err != nil {
		t.Fatalf("First ResolveENS error: %v", err)
	}
	if addr != targetAddr {
		t.Errorf("ResolveENS returned address %v, want %v", addr, targetAddr)
	}
	// Verify RPC calls were made
	if regCalls := mockRPC.registryCallCount(name); regCalls != 1 {
		t.Errorf("Expected 1 registry call, got %d", regCalls)
	}
	if resCalls := mockRPC.resolverCallCount(name); resCalls != 1 {
		t.Errorf("Expected 1 resolver call, got %d", resCalls)
	}
	// Second call
	addr2, err := ResolveENS(ctx, name, mockRPC, cache)
	if err != nil {
		t.Fatalf("Second ResolveENS error: %v", err)
	}
	if addr2 != targetAddr {
		t.Errorf("Second ResolveENS returned address %v, want %v", addr2, targetAddr)
	}
	// Verify no additional RPC calls
	if regCalls := mockRPC.registryCallCount(name); regCalls != 1 {
		t.Errorf("Expected still 1 registry call after cache, got %d", regCalls)
	}
	if resCalls := mockRPC.resolverCallCount(name); resCalls != 1 {
		t.Errorf("Expected still 1 resolver call after cache, got %d", resCalls)
	}
}

func TestENSNode_Readlink(t *testing.T) {
	name := "vitalik.eth"
	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	node := NewENSNode(name, addr, nil, nil)
	target, errno := node.Readlink(context.TODO())
	if errno != 0 {
		t.Fatalf("Readlink errno = %v", errno)
	}
	expected := "../../accounts/" + strings.ToLower(addr.Hex()) + "/"
	if string(target) != expected {
		t.Errorf("Readlink returned %q, want %q", target, expected)
	}
}

// TestResolveENS_NegativeCache tests that negative caching works for unregistered names.
func TestResolveENS_NegativeCache(t *testing.T) {
	mockRPC := newMockENSRPCClient()
	cache := newTestCache(t)

	name := "unregistered.eth"
	// No resolver set in mock, so will return ethereum.NotFound

	ctx := context.TODO()
	// First call should return NotFound
	_, err := ResolveENS(ctx, name, mockRPC, cache)
	if !errors.Is(err, ethereum.NotFound) {
		t.Fatalf("Expected NotFound error, got %v", err)
	}
	// Verify negative cache was set
	key := cacheKeyENSPrefix + strings.ToLower(name)
	if !cache.IsNegative(key) {
		t.Error("Expected negative cache to be set for unregistered name")
	}
	// Second call should also return NotFound (served from negative cache)
	// and should not increase RPC call count
	registryCallsBefore := mockRPC.registryCallCount(name)
	_, err = ResolveENS(ctx, name, mockRPC, cache)
	if !errors.Is(err, ethereum.NotFound) {
		t.Fatalf("Second call: Expected NotFound error, got %v", err)
	}
	registryCallsAfter := mockRPC.registryCallCount(name)
	if registryCallsAfter != registryCallsBefore {
		t.Errorf("Expected no additional RPC calls after negative cache, got %d -> %d", registryCallsBefore, registryCallsAfter)
	}
}

package fs

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestRootTxsDirNode_ReaddirEmpty(t *testing.T) {
	mockRPC := newMockBlockRPCClient()
	c := newTestCache(t)
	defer c.Close()

	node := NewRootTxsDirNode(mockRPC, c)
	stream, errno := node.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir errno = %v, want 0", errno)
	}
	if stream.HasNext() {
		t.Error("Readdir should return empty stream")
	}
}

func TestRootTxsDirNode_LookupInvalidHash(t *testing.T) {
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestRootTxsDirNode_LookupUnknownHash(t *testing.T) {
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestRootTxsDirNode_LookupKnownHash(t *testing.T) {
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestRootTxNode_Readdir(t *testing.T) {
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestRootTxNode_LookupInfoJSON(t *testing.T) {
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestRootTxNode_LookupReceiptJSON(t *testing.T) {
	t.Skip("Lookup requires mounted bridge, skipping for now")
}

func TestFetchTransactionByHash(t *testing.T) {
	ctx := context.Background()
	mockRPC := newMockBlockRPCClient()
	c := newTestCache(t)
	defer c.Close()

	// Create a mock transaction and receipt inside a block
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    5,
		GasPrice: big.NewInt(1000000000),
		Gas:      21000,
		To:       &common.Address{},
		Value:    big.NewInt(0),
		Data:     nil,
		V:        big.NewInt(27),
		R:        big.NewInt(1),
		S:        big.NewInt(2),
	})
	receipt := &types.Receipt{
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Logs:              []*types.Log{},
		TxHash:            tx.Hash(),
		ContractAddress:   common.Address{},
		GasUsed:           21000,
		BlockNumber:       big.NewInt(1000),
	}
	// Add block with transaction
	mockRPC.addMockBlockWithTransaction(1000, common.HexToHash("0x0000"), 123456, tx, receipt)

	// Test fetching transaction
	fetchedTx, blockNum, err := fetchTransactionByHash(ctx, tx.Hash(), mockRPC, c)
	if err != nil {
		t.Fatalf("fetchTransactionByHash error: %v", err)
	}
	if fetchedTx.Hash() != tx.Hash() {
		t.Errorf("fetched transaction hash mismatch: got %v, want %v", fetchedTx.Hash(), tx.Hash())
	}
	if blockNum != 1000 {
		t.Errorf("block number mismatch: got %d, want 1000", blockNum)
	}

	// Test negative caching for unknown hash
	unknownHash := common.HexToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	_, _, err = fetchTransactionByHash(ctx, unknownHash, mockRPC, c)
	if !errors.Is(err, ethereum.NotFound) {
		t.Errorf("expected NotFound error, got %v", err)
	}
	// Second call should also return NotFound (negative cache)
	_, _, err = fetchTransactionByHash(ctx, unknownHash, mockRPC, c)
	if !errors.Is(err, ethereum.NotFound) {
		t.Errorf("expected NotFound error from negative cache, got %v", err)
	}
}

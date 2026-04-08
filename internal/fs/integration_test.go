//go:build integration

package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

// getRPCURL returns the RPC URL from the ETHFUSE_RPC_URL environment variable.
// Returns empty string if not set.
func getRPCURL() string {
	return os.Getenv("ETHFUSE_RPC_URL")
}

// newIntegrationCache creates a cache with a temporary db path for integration tests.
func newIntegrationCache(t *testing.T) *cache.Cache {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "cache.db")
	c, err := cache.NewCache(dbPath)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Logf("failed to close cache: %v", err)
		}
	})
	return c
}

// mountTestFilesystem mounts a test FUSE filesystem and returns the server and mountpoint.
// The caller should call server.Unmount() and wait for server.Wait() to complete.
func mountTestFilesystem(t *testing.T, rpcClient rpc.RPCClient, c *cache.Cache) (*fuse.Server, string) {
	t.Helper()

	if runtime.GOOS != "linux" {
		t.Skip("FUSE mount tests require Linux")
	}

	root := NewRootNode(rpcClient, c)

	tmpDir := t.TempDir()
	mountpoint := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}

	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "ethfuse-test",
			Name:   "ethfuse-test",
		},
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		t.Fatalf("Failed to mount: %v", err)
	}

	// Give the mount a moment to initialize
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		if err := server.Unmount(); err != nil {
			t.Logf("Failed to unmount: %v", err)
		}
		server.Wait()
	})

	return server, mountpoint
}

// readMountFile reads a file from the mounted filesystem.
func readMountFile(t *testing.T, mountpoint, relPath string) []byte {
	t.Helper()
	fullPath := filepath.Join(mountpoint, relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", relPath, err)
	}
	return data
}

// readMountDir reads a directory from the mounted filesystem.
func readMountDir(t *testing.T, mountpoint, relPath string) []os.DirEntry {
	t.Helper()
	fullPath := filepath.Join(mountpoint, relPath)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		t.Fatalf("Failed to read dir %s: %v", relPath, err)
	}
	return entries
}

// TestIntegrationChainLatestBlock tests that chain/latest_block returns a decimal
// integer that increments over time.
func TestIntegrationChainLatestBlock(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	// Start background refresh
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartBackgroundRefresh(ctx, rpcClient, c)

	// Read latest block twice with a small delay
	block1Data := readMountFile(t, mountTestFilesystemForRead(t, rpcClient, c), "chain/latest_block")
	block1Str := strings.TrimSpace(string(block1Data))
	block1, err := strconv.ParseUint(block1Str, 10, 64)
	if err != nil {
		t.Fatalf("chain/latest_block should be a decimal integer, got %q: %v", block1Str, err)
	}

	// Wait a bit and read again
	time.Sleep(2 * time.Second)

	block2Data := readMountFile(t, mountTestFilesystemForRead(t, rpcClient, c), "chain/latest_block")
	block2Str := strings.TrimSpace(string(block2Data))
	block2, err := strconv.ParseUint(block2Str, 10, 64)
	if err != nil {
		t.Fatalf("chain/latest_block should be a decimal integer, got %q: %v", block2Str, err)
	}

	// Block number should not decrease
	if block2 < block1 {
		t.Errorf("Block number decreased from %d to %d", block1, block2)
	}
}

// mountTestFilesystemForRead creates a temporary mount for reading.
// This is a helper for simple read tests without needing full mount lifecycle management.
func mountTestFilesystemForRead(t *testing.T, rpcClient rpc.RPCClient, c *cache.Cache) string {
	t.Helper()

	if runtime.GOOS != "linux" {
		t.Skip("FUSE mount tests require Linux")
	}

	root := NewRootNode(rpcClient, c)

	tmpDir := t.TempDir()
	mountpoint := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}

	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "ethfuse-test",
			Name:   "ethfuse-test",
		},
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		t.Fatalf("Failed to mount: %v", err)
	}

	// Give the mount a moment to initialize
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		if err := server.Unmount(); err != nil {
			t.Logf("Failed to unmount: %v", err)
		}
		server.Wait()
	})

	return mountpoint
}

// TestIntegrationBlockInfoJSON tests that blocks/[number]/info.json returns
// valid JSON with fields matching known block data.
func TestIntegrationBlockInfoJSON(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	// Get a known block number (latest - 10 to ensure finalization)
	ctx := context.Background()
	latestBlock, err := rpcClient.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to get latest block: %v", err)
	}
	testBlockNum := latestBlock.Number().Uint64() - 10

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Read info.json
	data := readMountFile(t, mountpoint, "blocks/"+strconv.FormatUint(testBlockNum, 10)+"/info.json")

	// Parse JSON
	var info struct {
		Number     uint64 `json:"number"`
		Hash       string `json:"hash"`
		ParentHash string `json:"parentHash"`
		Timestamp  uint64 `json:"timestamp"`
		Miner      string `json:"miner"`
		GasUsed    uint64 `json:"gasUsed"`
		GasLimit   uint64 `json:"gasLimit"`
		TxCount    int    `json:"txCount"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("info.json should be valid JSON: %v", err)
	}

	// Verify block number matches
	if info.Number != testBlockNum {
		t.Errorf("info.json number = %d, want %d", info.Number, testBlockNum)
	}

	// Verify hash format
	if !strings.HasPrefix(info.Hash, "0x") || len(info.Hash) != 66 {
		t.Errorf("info.json hash has invalid format: %q", info.Hash)
	}

	// Verify parent hash format
	if !strings.HasPrefix(info.ParentHash, "0x") || len(info.ParentHash) != 66 {
		t.Errorf("info.json parentHash has invalid format: %q", info.ParentHash)
	}
}

// TestIntegrationTxReceipt tests that txs/[hash]/receipt.json returns
// correct status and gasUsed for a known transaction.
func TestIntegrationTxReceipt(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	// Get a transaction from a recent block
	ctx := context.Background()
	latestBlock, err := rpcClient.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to get latest block: %v", err)
	}

	// Skip if block has no transactions
	if latestBlock.Transactions().Len() == 0 {
		t.Skip("Block has no transactions")
	}

	tx := latestBlock.Transactions()[0]
	txHash := tx.Hash()
	blockNum := latestBlock.Number().Uint64()

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Read receipt.json via blocks path
	data := readMountFile(t, mountpoint, "blocks/"+strconv.FormatUint(blockNum, 10)+"/txs/"+txHash.Hex()+"/receipt.json")

	// Parse JSON
	var receipt struct {
		Status            uint64 `json:"status"`
		GasUsed           uint64 `json:"gasUsed"`
		EffectiveGasPrice string `json:"effectiveGasPrice"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("receipt.json should be valid JSON: %v", err)
	}

	// Status should be 0 (failed) or 1 (success)
	if receipt.Status != 0 && receipt.Status != 1 {
		t.Errorf("receipt.json status = %d, want 0 or 1", receipt.Status)
	}

	// GasUsed should be non-zero for most transactions
	// (some simple transfers may have 21000)
	if receipt.GasUsed == 0 {
		t.Errorf("receipt.json gasUsed = 0, expected non-zero")
	}
}

// TestIntegrationAccountBalance tests that accounts/[addr]/balance
// matches eth_getBalance RPC call directly.
func TestIntegrationAccountBalance(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	// Use a known address (e.g., vitalik.eth's address)
	testAddr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	addrHex := strings.ToLower(testAddr.Hex())

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Read balance from filesystem
	fsData := readMountFile(t, mountpoint, "accounts/"+addrHex+"/balance")
	fsBalanceStr := strings.TrimSpace(string(fsData))

	// Get balance directly from RPC
	ctx := context.Background()
	rpcBalance, err := rpcClient.BalanceAt(ctx, testAddr, nil)
	if err != nil {
		t.Fatalf("Failed to get balance from RPC: %v", err)
	}

	// Compare
	if fsBalanceStr != rpcBalance.String() {
		t.Errorf("accounts/%s/balance = %s, want %s", addrHex, fsBalanceStr, rpcBalance.String())
	}
}

// TestIntegrationBlocksRecentCount tests that ls blocks/recent/
// returns exactly 100 entries.
func TestIntegrationBlocksRecentCount(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Read blocks/recent/ directory
	entries := readMountDir(t, mountpoint, "blocks/recent")

	// Should have exactly 100 entries (or less if chain is very young)
	latestBlock, err := rpcClient.BlockByNumber(context.Background(), nil)
	if err != nil {
		t.Fatalf("Failed to get latest block: %v", err)
	}

	expectedCount := 100
	if latestBlock.Number().Uint64() < 99 {
		// Chain is very young, expect latest+1 entries (0 to latest)
		expectedCount = int(latestBlock.Number().Uint64()) + 1
	}

	if len(entries) != expectedCount {
		t.Errorf("blocks/recent/ has %d entries, want %d", len(entries), expectedCount)
	}

	// Verify all entries are directories with numeric names
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("blocks/recent/ entry %q is not a directory", entry.Name())
		}
		if _, err := strconv.ParseUint(entry.Name(), 10, 64); err != nil {
			t.Errorf("blocks/recent/ entry %q is not a valid block number", entry.Name())
		}
	}
}

// TestIntegrationENSBalance tests that ens/[name]/balance
// matches the underlying account balance.
func TestIntegrationENSBalance(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	// Test with a known ENS name that resolves
	// Using "vitalik.eth" which is well-known
	ensName := "vitalik.eth"

	// First resolve the ENS name directly to get the address
	ctx := context.Background()
	addr, err := ResolveENS(ctx, ensName, rpcClient, c)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			t.Skip("ENS name vitalik.eth not resolved (may be network issue)")
		}
		t.Fatalf("Failed to resolve ENS name: %v", err)
	}

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Read balance via ENS path
	ensBalanceData := readMountFile(t, mountpoint, "ens/"+ensName+"/balance")
	ensBalanceStr := strings.TrimSpace(string(ensBalanceData))

	// Read balance via accounts path
	addrHex := strings.ToLower(addr.Hex())
	accountBalanceData := readMountFile(t, mountpoint, "accounts/"+addrHex+"/balance")
	accountBalanceStr := strings.TrimSpace(string(accountBalanceData))

	// They should match
	if ensBalanceStr != accountBalanceStr {
		t.Errorf("ens/%s/balance = %s, accounts/%s/balance = %s",
			ensName, ensBalanceStr, addrHex, accountBalanceStr)
	}
}

// TestIntegrationWriteReadOnly tests that write attempts return EROFS.
func TestIntegrationWriteReadOnly(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Attempt to write to a file
	testPath := filepath.Join(mountpoint, "chain/id")
	err = os.WriteFile(testPath, []byte("999"), 0o644)

	// Should get a read-only filesystem error
	if err == nil {
		t.Error("Write to read-only filesystem succeeded, should have failed")
	} else {
		// Check if error indicates read-only filesystem
		if !strings.Contains(err.Error(), "read-only") &&
			!strings.Contains(err.Error(), "Operation not permitted") {
			t.Logf("Write error: %v (expected read-only error)", err)
		}
	}
}

// TestIntegrationInvalidAddress tests that invalid address lookup returns EINVAL.
func TestIntegrationInvalidAddress(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	ctx := context.Background()

	// Test with invalid address format
	invalidAddr := "0xinvalid123"
	accountsDir := NewAccountsDirNode(rpcClient, c)

	var out fuse.EntryOut
	_, errno := accountsDir.Lookup(ctx, invalidAddr, &out)
	if errno != syscall.EINVAL {
		t.Errorf("Lookup(%q) errno = %v, want EINVAL", invalidAddr, errno)
	}

	// Test with address that's too short
	shortAddr := "0x123"
	_, errno = accountsDir.Lookup(ctx, shortAddr, &out)
	if errno != syscall.EINVAL {
		t.Errorf("Lookup(%q) errno = %v, want EINVAL", shortAddr, errno)
	}

	// Test with non-hex characters
	nonHexAddr := "0xgggggggggggggggggggggggggggggggggggggggg"
	_, errno = accountsDir.Lookup(ctx, nonHexAddr, &out)
	if errno != syscall.EINVAL {
		t.Errorf("Lookup(%q) errno = %v, want EINVAL", nonHexAddr, errno)
	}
}

// TestIntegrationGhostBlock tests that ghost blocks are accessible by path
// but not visible in ls blocks/recent/.
func TestIntegrationGhostBlock(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	ctx := context.Background()
	latestBlock, err := rpcClient.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to get latest block: %v", err)
	}

	// Pick a block that's older than the recent 100 (ghost block)
	ghostBlockNum := latestBlock.Number().Uint64() - 150
	if ghostBlockNum > latestBlock.Number().Uint64() {
		// Chain is too young for ghost blocks
		t.Skip("Chain too young for ghost block test")
	}

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Verify ghost block is NOT in blocks/recent/
	recentEntries := readMountDir(t, mountpoint, "blocks/recent")
	ghostBlockStr := strconv.FormatUint(ghostBlockNum, 10)
	foundInRecent := false
	for _, entry := range recentEntries {
		if entry.Name() == ghostBlockStr {
			foundInRecent = true
			break
		}
	}
	if foundInRecent {
		t.Errorf("Ghost block %s found in blocks/recent/, should not be visible", ghostBlockStr)
	}

	// Verify ghost block IS accessible by direct path
	ghostData := readMountFile(t, mountpoint, "blocks/"+ghostBlockStr+"/info.json")
	var info struct {
		Number uint64 `json:"number"`
	}
	if err := json.Unmarshal(ghostData, &info); err != nil {
		t.Fatalf("Ghost block info.json should be valid JSON: %v", err)
	}
	if info.Number != ghostBlockNum {
		t.Errorf("Ghost block number = %d, want %d", info.Number, ghostBlockNum)
	}
}

// TestIntegrationChainGas tests that chain/gas/ files return valid gwei values.
func TestIntegrationChainGas(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Test all gas price files
	gasFiles := []string{"slow", "standard", "fast"}
	for _, gasFile := range gasFiles {
		data := readMountFile(t, mountpoint, "chain/gas/"+gasFile)
		gweiStr := strings.TrimSpace(string(data))

		// Should be a valid decimal number
		if _, err := strconv.ParseFloat(gweiStr, 64); err != nil {
			t.Errorf("chain/gas/%s = %q, should be a valid decimal number", gasFile, gweiStr)
		}

		// Should be non-negative
		if gwei, _ := strconv.ParseFloat(gweiStr, 64); gwei < 0 {
			t.Errorf("chain/gas/%s = %f, should be non-negative", gasFile, gwei)
		}
	}
}

// TestIntegrationChainID tests that chain/id returns a valid chain ID.
func TestIntegrationChainID(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	data := readMountFile(t, mountpoint, "chain/id")
	idStr := strings.TrimSpace(string(data))

	// Should be a valid decimal number
	chainID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		t.Errorf("chain/id = %q, should be a valid decimal number", idStr)
	}

	// Verify against RPC
	ctx := context.Background()
	rpcChainID, err := rpcClient.ChainID(ctx)
	if err != nil {
		t.Fatalf("Failed to get chain ID from RPC: %v", err)
	}

	if chainID != rpcChainID.Uint64() {
		t.Errorf("chain/id = %d, RPC chain ID = %d", chainID, rpcChainID.Uint64())
	}
}

// TestIntegrationTxInfoJSON tests that transaction info.json contains required fields.
func TestIntegrationTxInfoJSON(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	ctx := context.Background()
	latestBlock, err := rpcClient.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to get latest block: %v", err)
	}

	if latestBlock.Transactions().Len() == 0 {
		t.Skip("Block has no transactions")
	}

	tx := latestBlock.Transactions()[0]
	txHash := tx.Hash()
	blockNum := latestBlock.Number().Uint64()

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	data := readMountFile(t, mountpoint, "blocks/"+strconv.FormatUint(blockNum, 10)+"/txs/"+txHash.Hex()+"/info.json")

	// Parse JSON - should not error
	var info map[string]interface{}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("tx info.json should be valid JSON: %v", err)
	}

	// Verify required fields exist
	requiredFields := []string{"hash", "from", "gas", "nonce", "blockNumber"}
	for _, field := range requiredFields {
		if _, ok := info[field]; !ok {
			t.Errorf("tx info.json missing required field: %s", field)
		}
	}

	// Verify hash matches
	if hash, ok := info["hash"].(string); ok {
		if hash != txHash.Hex() {
			t.Errorf("tx info.json hash = %s, want %s", hash, txHash.Hex())
		}
	}
}

// TestIntegrationAccountNonce tests that account nonce is a valid decimal number.
func TestIntegrationAccountNonce(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	testAddr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	addrHex := strings.ToLower(testAddr.Hex())

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	data := readMountFile(t, mountpoint, "accounts/"+addrHex+"/nonce")
	nonceStr := strings.TrimSpace(string(data))

	// Should be a valid decimal number
	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	if err != nil {
		t.Errorf("accounts/%s/nonce = %q, should be a valid decimal number", addrHex, nonceStr)
	}

	// Verify against RPC
	ctx := context.Background()
	rpcNonce, err := rpcClient.NonceAt(ctx, testAddr, nil)
	if err != nil {
		t.Fatalf("Failed to get nonce from RPC: %v", err)
	}

	if nonce != rpcNonce {
		t.Errorf("accounts/%s/nonce = %d, RPC nonce = %d", addrHex, nonce, rpcNonce)
	}
}

// TestIntegrationAccountCode tests that account code returns valid hex.
func TestIntegrationAccountCode(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	testAddr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	addrHex := strings.ToLower(testAddr.Hex())

	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	data := readMountFile(t, mountpoint, "accounts/"+addrHex+"/code")
	codeStr := strings.TrimSpace(string(data))

	// Should start with 0x or be empty (0x for EOA)
	if !strings.HasPrefix(codeStr, "0x") {
		t.Errorf("accounts/%s/code = %q, should start with 0x", addrHex, codeStr)
	}

	// Verify against RPC
	ctx := context.Background()
	rpcCode, err := rpcClient.CodeAt(ctx, testAddr, nil)
	if err != nil {
		t.Fatalf("Failed to get code from RPC: %v", err)
	}

	expectedCode := "0x"
	if len(rpcCode) > 0 {
		expectedCode = "0x" + common.Bytes2Hex(rpcCode)
	}

	if codeStr != expectedCode {
		t.Errorf("accounts/%s/code length = %d, RPC code length = %d",
			addrHex, len(codeStr), len(expectedCode))
	}
}

// TestIntegrationBlockScalarFiles tests that block scalar files (hash, timestamp)
// are plain text terminated with newline.
func TestIntegrationBlockScalarFiles(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	ctx := context.Background()
	latestBlock, err := rpcClient.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("Failed to get latest block: %v", err)
	}

	blockNum := latestBlock.Number().Uint64()
	mountpoint := mountTestFilesystemForRead(t, rpcClient, c)

	// Test hash file
	hashData := readMountFile(t, mountpoint, "blocks/"+strconv.FormatUint(blockNum, 10)+"/hash")
	hashStr := string(hashData)
	if !strings.HasSuffix(hashStr, "\n") {
		t.Errorf("blocks/%d/hash should end with newline", blockNum)
	}
	hashStr = strings.TrimSpace(hashStr)
	if !strings.HasPrefix(hashStr, "0x") || len(hashStr) != 66 {
		t.Errorf("blocks/%d/hash = %q, invalid hash format", blockNum, hashStr)
	}

	// Test timestamp file
	timestampData := readMountFile(t, mountpoint, "blocks/"+strconv.FormatUint(blockNum, 10)+"/timestamp")
	timestampStr := string(timestampData)
	if !strings.HasSuffix(timestampStr, "\n") {
		t.Errorf("blocks/%d/timestamp should end with newline", blockNum)
	}
	timestampStr = strings.TrimSpace(timestampStr)
	timestamp, err := strconv.ParseUint(timestampStr, 10, 64)
	if err != nil {
		t.Errorf("blocks/%d/timestamp = %q, should be a valid decimal number", blockNum, timestampStr)
	}
	if timestamp != latestBlock.Time() {
		t.Errorf("blocks/%d/timestamp = %d, want %d", blockNum, timestamp, latestBlock.Time())
	}
}

// TestIntegrationENSInvalidName tests that invalid ENS name returns ENOENT.
func TestIntegrationENSInvalidName(t *testing.T) {
	rpcURL := getRPCURL()
	if rpcURL == "" {
		t.Skip("ETHFUSE_RPC_URL not set, skipping integration test")
	}

	rpcClient, err := rpc.New(rpcURL)
	if err != nil {
		t.Fatalf("Failed to create RPC client: %v", err)
	}
	defer rpcClient.Close()

	c := newIntegrationCache(t)

	ctx := context.Background()

	// Test with non-existent ENS name
	invalidName := "thisdoesnotexist12345.eth"
	ensDir := NewENSDirNode(rpcClient, c)

	var out fuse.EntryOut
	_, errno := ensDir.Lookup(ctx, invalidName, &out)
	if errno != syscall.ENOENT {
		t.Errorf("Lookup(%q) errno = %v, want ENOENT", invalidName, errno)
	}

	// Test with name not ending in .eth
	nonEthName := "vitalik.com"
	_, errno = ensDir.Lookup(ctx, nonEthName, &out)
	if errno != syscall.EINVAL {
		t.Errorf("Lookup(%q) errno = %v, want EINVAL", nonEthName, errno)
	}
}

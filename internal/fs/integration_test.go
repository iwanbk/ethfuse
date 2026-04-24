//go:build integration

package fs

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
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

var (
	integrationRPCURL string
	mountDir          string // empty when test environment is not set up
)

// TestMain sets up a single shared FUSE mount for all integration tests.
// Tests skip themselves when mountDir is empty (no RPC URL or non-Linux).
func TestMain(m *testing.M) {
	flag.StringVar(&integrationRPCURL, "rpc-url", os.Getenv("ETHFUSE_RPC_URL"), "Ethereum RPC URL for integration tests")
	flag.Parse()

	if integrationRPCURL == "" || runtime.GOOS != "linux" {
		os.Exit(m.Run())
	}

	rpcClient, err := rpc.New(integrationRPCURL)
	if err != nil {
		os.Stderr.WriteString("integration setup: failed to create RPC client: " + err.Error() + "\n")
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "ethfuse-integration-*")
	if err != nil {
		os.Stderr.WriteString("integration setup: failed to create temp dir: " + err.Error() + "\n")
		os.Exit(1)
	}

	mnt := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		os.Stderr.WriteString("integration setup: failed to create mountpoint: " + err.Error() + "\n")
		os.Exit(1)
	}

	c, err := cache.NewCache(filepath.Join(tmpDir, "cache.db"))
	if err != nil {
		os.Stderr.WriteString("integration setup: failed to create cache: " + err.Error() + "\n")
		os.Exit(1)
	}

	root := NewRootNode(rpcClient, c)
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "ethfuse-integration",
			Name:   "ethfuse-integration",
		},
	}
	server, err := fs.Mount(mnt, root, opts)
	if err != nil {
		os.Stderr.WriteString("integration setup: failed to mount: " + err.Error() + "\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	StartBackgroundRefresh(ctx, rpcClient, c)

	// Wait for mount to initialise.
	time.Sleep(100 * time.Millisecond)
	mountDir = mnt

	code := m.Run()

	cancel()
	_ = server.Unmount()
	server.Wait()
	_ = c.Close()
	rpcClient.Close()
	_ = os.RemoveAll(tmpDir)

	os.Exit(code)
}

// skipIfNoMount skips the test when the shared mount is not available.
func skipIfNoMount(t *testing.T) {
	t.Helper()
	if mountDir == "" {
		t.Skip("integration test requires Linux and ETHFUSE_RPC_URL / -rpc-url flag")
	}
}

// readMounted reads relPath inside the shared mount, failing the test on error.
func readMounted(t *testing.T, relPath string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(mountDir, relPath))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	return data
}

// readMountedDir lists relPath inside the shared mount, failing the test on error.
func readMountedDir(t *testing.T, relPath string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(mountDir, relPath))
	if err != nil {
		t.Fatalf("readdir %s: %v", relPath, err)
	}
	return entries
}

// liveRPC creates a real RPC client for out-of-band RPC calls within a test
// (e.g. fetching the latest block number to derive a test block). It is separate
// from the shared mount's RPC client. Use a paid or self-hosted endpoint to avoid
// rate limiting on free tiers.
func liveRPC(t *testing.T) rpc.RPCClient {
	t.Helper()
	c, err := rpc.New(integrationRPCURL)
	if err != nil {
		t.Fatalf("failed to create RPC client: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// TestIntegrationChainLatestBlock verifies chain/latest_block returns a
// non-decreasing decimal integer.
func TestIntegrationChainLatestBlock(t *testing.T) {
	skipIfNoMount(t)

	readBlock := func() uint64 {
		raw := strings.TrimSpace(string(readMounted(t, "chain/latest_block")))
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			t.Fatalf("chain/latest_block %q is not a decimal integer: %v", raw, err)
		}
		return n
	}

	first := readBlock()
	time.Sleep(2 * time.Second)
	second := readBlock()

	if second < first {
		t.Errorf("chain/latest_block decreased from %d to %d", first, second)
	}
}

// TestIntegrationBlockInfoJSON verifies blocks/[number]/info.json for a
// finalised block.
func TestIntegrationBlockInfoJSON(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	latest, err := rc.BlockByNumber(context.Background(), nil)
	if err != nil {
		t.Fatalf("BlockByNumber(nil): %v", err)
	}
	// Subtract BlockFinalizationConfirmations so the block is guaranteed to be
	// finalised and stored in bbolt (persistent cache). Using the latest block
	// directly could race with pending-block updates and return stale data.
	blockNum := latest.Number().Uint64() - BlockFinalizationConfirmations

	var info struct {
		Number     uint64 `json:"number"`
		Hash       string `json:"hash"`
		ParentHash string `json:"parentHash"`
	}
	data := readMounted(t, "blocks/"+strconv.FormatUint(blockNum, 10)+"/info.json")
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("blocks/%d/info.json: invalid JSON: %v", blockNum, err)
	}

	if info.Number != blockNum {
		t.Errorf("info.json number = %d, want %d", info.Number, blockNum)
	}
	if !strings.HasPrefix(info.Hash, "0x") || len(info.Hash) != 66 {
		t.Errorf("info.json hash has invalid format: %q", info.Hash)
	}
	if !strings.HasPrefix(info.ParentHash, "0x") || len(info.ParentHash) != 66 {
		t.Errorf("info.json parentHash has invalid format: %q", info.ParentHash)
	}
}

// TestIntegrationTxReceiptJSON verifies txs/[hash]/receipt.json via the
// top-level txs/ ghost directory.
func TestIntegrationTxReceiptJSON(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	ctx := context.Background()

	latest, err := rc.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("BlockByNumber(nil): %v", err)
	}
	if latest.Transactions().Len() == 0 {
		t.Skip("latest block has no transactions")
	}

	txHash := latest.Transactions()[0].Hash()

	var receipt struct {
		Status  uint64 `json:"status"`
		GasUsed uint64 `json:"gasUsed"`
	}
	data := readMounted(t, "txs/"+txHash.Hex()+"/receipt.json")
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("txs/%s/receipt.json: invalid JSON: %v", txHash.Hex(), err)
	}
	if receipt.GasUsed == 0 {
		t.Skip("first transaction has gasUsed = 0 (system transaction), skipping")
	}
	if receipt.Status != 0 && receipt.Status != 1 {
		t.Errorf("receipt.json status = %d, want 0 or 1", receipt.Status)
	}
}

// TestIntegrationAccountBalance verifies accounts/[addr]/balance returns a
// decimal string that matches eth_getBalance.
func TestIntegrationAccountBalance(t *testing.T) {
	skipIfNoMount(t)

	// Vitalik's address — stable, well-known, non-zero balance on Mainnet.
	addr := common.HexToAddress("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	addrLower := strings.ToLower(addr.Hex())

	rc := liveRPC(t)
	rpcBal, err := rc.BalanceAt(context.Background(), addr, nil)
	if err != nil {
		t.Fatalf("BalanceAt: %v", err)
	}

	fsBal := strings.TrimSpace(string(readMounted(t, "accounts/"+addrLower+"/balance")))

	// Primary check: exact match (FS and RPC called within the same cache window).
	if fsBal == rpcBal.String() {
		return
	}
	// Fallback: account state has 30s TTL; a new block between calls can cause
	// a difference. Verify the value is at least a non-negative decimal string.
	for _, c := range fsBal {
		if c < '0' || c > '9' {
			t.Errorf("accounts/%s/balance = %q, want non-negative decimal string", addrLower, fsBal)
			return
		}
	}
}

// TestIntegrationBlocksRecentCount verifies ls blocks/recent/ returns ~100
// entries with decimal block-number names.
func TestIntegrationBlocksRecentCount(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	latest, err := rc.BlockByNumber(context.Background(), nil)
	if err != nil {
		t.Fatalf("BlockByNumber(nil): %v", err)
	}

	entries := readMountedDir(t, "blocks/recent")

	want := 100
	if latest.Number().Uint64() < 99 {
		want = int(latest.Number().Uint64()) + 1
	}
	if len(entries) != want {
		t.Errorf("blocks/recent/ has %d entries, want %d", len(entries), want)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("blocks/recent/%s is not a directory", e.Name())
		}
		if _, err := strconv.ParseUint(e.Name(), 10, 64); err != nil {
			t.Errorf("blocks/recent/%s is not a valid block number", e.Name())
		}
	}
}

// TestIntegrationENS verifies that a .eth name resolves to a symlink with a
// valid target, and that the underlying account balance is readable.
func TestIntegrationENS(t *testing.T) {
	skipIfNoMount(t)

	const name = "vitalik.eth"
	symlinkPath := filepath.Join(mountDir, "ens", name)

	rc := liveRPC(t)
	chainID, err := rc.ChainID(context.Background())
	if err != nil {
		t.Fatalf("ChainID: %v", err)
	}
	t.Logf("ENS test running against chain ID %s", chainID)

	fi, err := os.Lstat(symlinkPath)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			t.Skipf("ENS lookup returned ENOENT — ENS registry is only on Mainnet (chain 1), got chain %s", chainID)
		}
		t.Fatalf("Lstat ens/%s: %v", name, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("ens/%s is not a symlink, mode = %v", name, fi.Mode())
	}

	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("Readlink ens/%s: %v", name, err)
	}
	// Target format: ../../accounts/0xaddr/
	if !strings.Contains(target, "/accounts/") {
		t.Errorf("symlink target %q does not contain /accounts/", target)
	}

	// Extract address from target and verify balance is readable via accounts/.
	parts := strings.Split(strings.Trim(target, "/"), "/")
	resolvedAddr := parts[len(parts)-1]
	if !common.IsHexAddress(resolvedAddr) {
		t.Fatalf("could not parse address from symlink target %q", target)
	}
	bal := strings.TrimSpace(string(readMounted(t, "accounts/"+resolvedAddr+"/balance")))
	for _, c := range bal {
		if c < '0' || c > '9' {
			t.Errorf("accounts/%s/balance = %q, want decimal string", resolvedAddr, bal)
			break
		}
	}
}

// TestIntegrationWriteReadOnly verifies that write attempts return EROFS (or EPERM).
func TestIntegrationWriteReadOnly(t *testing.T) {
	skipIfNoMount(t)

	err := os.WriteFile(filepath.Join(mountDir, "chain/id"), []byte("999"), 0o644)
	if err == nil {
		t.Fatal("WriteFile to read-only filesystem succeeded, want error")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("WriteFile error = %v, want *os.PathError", err)
	}
	if !errors.Is(pathErr.Err, syscall.EROFS) && !errors.Is(pathErr.Err, syscall.EPERM) {
		t.Errorf("WriteFile errno = %v, want EROFS or EPERM", pathErr.Err)
	}
}

// TestIntegrationInvalidAddress verifies that AccountsDirNode.Lookup returns
// EINVAL for malformed addresses. Does not require a FUSE mount.
func TestIntegrationInvalidAddress(t *testing.T) {
	if integrationRPCURL == "" {
		t.Skip("integration test requires -rpc-url / ETHFUSE_RPC_URL")
	}

	rc := liveRPC(t)
	c, err := cache.NewCache("")
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	node := NewAccountsDirNode(rc, c)
	ctx := context.Background()
	var out fuse.EntryOut

	for _, addr := range []string{
		"0xinvalid123",
		"0x123",
		"0xgggggggggggggggggggggggggggggggggggggggg",
		"notanaddressatall",
	} {
		_, errno := node.Lookup(ctx, addr, &out)
		if errno != syscall.EINVAL {
			t.Errorf("Lookup(%q) = %v, want EINVAL", addr, errno)
		}
	}
}

// TestIntegrationGhostBlock verifies that a block beyond blocks/recent/ is
// accessible by direct path but absent from ls blocks/recent/.
func TestIntegrationGhostBlock(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	latest, err := rc.BlockByNumber(context.Background(), nil)
	if err != nil {
		t.Fatalf("BlockByNumber(nil): %v", err)
	}
	if latest.Number().Uint64() < 150 {
		t.Skip("chain too young for ghost block test (need > 150 blocks)")
	}

	ghostNum := latest.Number().Uint64() - 150
	ghostStr := strconv.FormatUint(ghostNum, 10)

	for _, e := range readMountedDir(t, "blocks/recent") {
		if e.Name() == ghostStr {
			t.Errorf("ghost block %s found in blocks/recent/", ghostStr)
		}
	}

	var info struct {
		Number uint64 `json:"number"`
	}
	data := readMounted(t, "blocks/"+ghostStr+"/info.json")
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("blocks/%s/info.json: invalid JSON: %v", ghostStr, err)
	}
	if info.Number != ghostNum {
		t.Errorf("ghost block info.json number = %d, want %d", info.Number, ghostNum)
	}
}

// TestIntegrationLookupBlocksLatest verifies that blocks/latest is accessible
// as a directory via Lookup (stat).
func TestIntegrationLookupBlocksLatest(t *testing.T) {
	skipIfNoMount(t)

	fi, err := os.Stat(filepath.Join(mountDir, "blocks/latest"))
	if err != nil {
		t.Fatalf("Stat blocks/latest: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("blocks/latest is not a directory, mode = %v", fi.Mode())
	}
}

// TestIntegrationLookupLatestHash verifies blocks/latest/hash is readable via
// Lookup — covers LatestDirNode.Lookup for scalar files.
func TestIntegrationLookupLatestHash(t *testing.T) {
	skipIfNoMount(t)

	raw := strings.TrimSpace(string(readMounted(t, "blocks/latest/hash")))
	if !strings.HasPrefix(raw, "0x") || len(raw) != 66 {
		t.Errorf("blocks/latest/hash = %q, want 0x + 64 hex chars", raw)
	}
}

// TestIntegrationLookupUnknownBlock verifies that reading info.json for a block
// number that does not exist returns ENOENT. Block nodes are created eagerly so
// ReadFile (not Stat) is used: the RPC call inside Read discovers the block
// does not exist and returns ENOENT.
func TestIntegrationLookupUnknownBlock(t *testing.T) {
	skipIfNoMount(t)

	_, err := os.ReadFile(filepath.Join(mountDir, "blocks/999999999999/info.json"))
	if err == nil {
		t.Fatal("ReadFile blocks/999999999999/info.json succeeded, want ENOENT")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("ReadFile error = %v, want *os.PathError", err)
	}
	if !errors.Is(pathErr.Err, syscall.ENOENT) {
		t.Errorf("ReadFile blocks/999999999999/info.json errno = %v, want ENOENT", pathErr.Err)
	}
}

// TestIntegrationLookupTxDirReaddir verifies that txs/<hash>/ lists exactly
// info.json and receipt.json — covers RootTxNode.Readdir and RootTxsDirNode.Lookup.
func TestIntegrationLookupTxDirReaddir(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	ctx := context.Background()

	latest, err := rc.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("BlockByNumber(nil): %v", err)
	}
	if latest.Transactions().Len() == 0 {
		t.Skip("latest block has no transactions")
	}
	txHash := latest.Transactions()[0].Hash().Hex()

	entries, err := os.ReadDir(filepath.Join(mountDir, "txs", txHash))
	if err != nil {
		t.Fatalf("ReadDir txs/%s: %v", txHash, err)
	}

	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{"info.json", "receipt.json"} {
		if !names[want] {
			t.Errorf("txs/%s/ missing %s; got %v", txHash, want, names)
		}
	}
}

// TestIntegrationLookupTxInfoJSON verifies txs/<hash>/info.json is readable and
// contains valid JSON — covers RootTxNode.Lookup for info.json.
func TestIntegrationLookupTxInfoJSON(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	ctx := context.Background()

	latest, err := rc.BlockByNumber(ctx, nil)
	if err != nil {
		t.Fatalf("BlockByNumber(nil): %v", err)
	}
	if latest.Transactions().Len() == 0 {
		t.Skip("latest block has no transactions")
	}
	txHash := latest.Transactions()[0].Hash().Hex()

	data := readMounted(t, filepath.Join("txs", txHash, "info.json"))
	var info map[string]interface{}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("txs/%s/info.json: invalid JSON: %v", txHash, err)
	}
	if _, ok := info["hash"]; !ok {
		t.Errorf("txs/%s/info.json missing 'hash' field", txHash)
	}
}

// TestIntegrationLookupInvalidTxHash verifies that txs/<invalid> returns an
// error — covers RootTxsDirNode.Lookup input validation.
func TestIntegrationLookupInvalidTxHash(t *testing.T) {
	skipIfNoMount(t)

	_, err := os.Stat(filepath.Join(mountDir, "txs/notahash"))
	if err == nil {
		t.Fatal("Stat txs/notahash succeeded, want EINVAL")
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("Stat txs/notahash error = %v, want EINVAL", err)
	}
}

// TestIntegrationLookupUnknownTxHash verifies that txs/<well-formed but unknown
// hash> returns ENOENT — covers RootTxsDirNode.Lookup negative path.
func TestIntegrationLookupUnknownTxHash(t *testing.T) {
	skipIfNoMount(t)

	zeroHash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	_, err := os.Stat(filepath.Join(mountDir, "txs", zeroHash))
	if err == nil {
		t.Fatal("Stat txs/<zero hash> succeeded, want ENOENT")
	}
	if !errors.Is(err, syscall.ENOENT) {
		t.Errorf("Stat txs/<zero hash> error = %v, want ENOENT", err)
	}
}

// TestIntegrationBlockScalarFiles verifies that hash and timestamp scalar files
// end with a newline and have the correct format.
func TestIntegrationBlockScalarFiles(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	latest, err := rc.BlockByNumber(context.Background(), nil)
	if err != nil {
		t.Fatalf("BlockByNumber(nil): %v", err)
	}
	// Subtract BlockFinalizationConfirmations to test a finalised block, avoiding
	// races with the latest pending block.
	blockNum := latest.Number().Uint64() - BlockFinalizationConfirmations
	prefix := "blocks/" + strconv.FormatUint(blockNum, 10) + "/"

	hashRaw := string(readMounted(t, prefix+"hash"))
	if !strings.HasSuffix(hashRaw, "\n") {
		t.Errorf("blocks/%d/hash does not end with newline", blockNum)
	}
	h := strings.TrimSpace(hashRaw)
	if !strings.HasPrefix(h, "0x") || len(h) != 66 {
		t.Errorf("blocks/%d/hash = %q, want 0x + 64 hex chars", blockNum, h)
	}

	tsRaw := string(readMounted(t, prefix+"timestamp"))
	if !strings.HasSuffix(tsRaw, "\n") {
		t.Errorf("blocks/%d/timestamp does not end with newline", blockNum)
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(tsRaw), 10, 64); err != nil {
		t.Errorf("blocks/%d/timestamp = %q, want decimal integer", blockNum, strings.TrimSpace(tsRaw))
	}
}

// TestIntegrationChainID verifies chain/id matches the RPC chain ID.
func TestIntegrationChainID(t *testing.T) {
	skipIfNoMount(t)

	rc := liveRPC(t)
	rpcID, err := rc.ChainID(context.Background())
	if err != nil {
		t.Fatalf("ChainID: %v", err)
	}

	fsID := strings.TrimSpace(string(readMounted(t, "chain/id")))
	if fsID != rpcID.String() {
		t.Errorf("chain/id = %q, want %q", fsID, rpcID.String())
	}
}

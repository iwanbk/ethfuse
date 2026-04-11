package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestRetryBehaviorHTTP429 tests that the client retries on HTTP 429 (rate limit)
func TestRetryBehaviorHTTP429(t *testing.T) {
	var attempts int32
	maxTestAttempts := int32(3)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < maxTestAttempts {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Success on the last attempt
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  "0x1",
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	// Use newClient with validate=false to skip startup validation
	client, err := newClient(server.URL, false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.ChainID(ctx)
	if err != nil {
		t.Fatalf("Expected success after retries, got: %v", err)
	}

	if atomic.LoadInt32(&attempts) != maxTestAttempts {
		t.Errorf("Expected %d attempts, got %d", maxTestAttempts, atomic.LoadInt32(&attempts))
	}
}

// TestRetryBehaviorHTTP500 tests that the client retries on HTTP 500
func TestRetryBehaviorHTTP500(t *testing.T) {
	var attempts int32
	maxTestAttempts := int32(3)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < maxTestAttempts {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Success on the last attempt
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  "0x1",
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	// Use newClient with validate=false to skip startup validation
	client, err := newClient(server.URL, false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.ChainID(ctx)
	if err != nil {
		t.Fatalf("Expected success after retries, got: %v", err)
	}

	if atomic.LoadInt32(&attempts) != maxTestAttempts {
		t.Errorf("Expected %d attempts, got %d", maxTestAttempts, atomic.LoadInt32(&attempts))
	}
}

// TestRetryBehaviorTimeout tests that the client handles timeouts
func TestRetryBehaviorTimeout(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		// Simulate timeout by sleeping longer than the context timeout
		time.Sleep(5 * time.Second)
	}))
	defer server.Close()

	// Use newClient with validate=false to skip startup validation
	client, err := newClient(server.URL, false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err = client.ChainID(ctx)
	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}

	// Should have attempted at least once
	if atomic.LoadInt32(&attempts) < 1 {
		t.Errorf("Expected at least 1 attempt, got %d", atomic.LoadInt32(&attempts))
	}
}

// TestRetryExhaustion tests that errors are returned after max retries
func TestRetryExhaustion(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Use newClient with validate=false to skip startup validation
	client, err := newClient(server.URL, false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.ChainID(ctx)
	if err == nil {
		t.Fatal("Expected error after max retries, got nil")
	}

	if atomic.LoadInt32(&attempts) != maxRetries {
		t.Errorf("Expected %d attempts (maxRetries), got %d", maxRetries, atomic.LoadInt32(&attempts))
	}
}

// TestNonRetryableErrors tests that context cancellation is not retried
func TestNonRetryableErrors(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		// Simulate slow response that will trigger context timeout
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	// Use newClient with validate=false to skip startup validation
	client, err := newClient(server.URL, false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = client.ChainID(ctx)
	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}

	// Should only attempt once (no retry for context.DeadlineExceeded)
	// Note: due to timing, it might retry once before the timeout is detected
	// The key is that it doesn't retry all maxRetries times
	maxExpectedAttempts := int32(2)
	if atomic.LoadInt32(&attempts) > maxExpectedAttempts {
		t.Errorf("Expected at most %d attempts (no retry for context timeout), got %d", maxExpectedAttempts, atomic.LoadInt32(&attempts))
	}
}

// TestErrorSanitization tests that API keys and URLs are stripped from error messages
func TestErrorSanitization(t *testing.T) {
	apiKey := "test-api-key-12345"
	// Use a fake URL that won't actually connect - this tests sanitization during dial
	rpcURL := "https://eth-mainnet.g.alchemy.com/v2/" + apiKey

	// Create a server that will never be contacted (we're testing dial error sanitization)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This won't be called since we're using a different URL
		w.WriteHeader(http.StatusOK)
	}))
	server.Close() // Close immediately so connection fails

	// Try to connect to the fake alchemy URL (not the test server)
	_, err := New(rpcURL)
	if err == nil {
		t.Fatal("Expected error from New(), got nil")
	}

	errStr := err.Error()

	// Check that the API key is not in the error message
	if strings.Contains(errStr, apiKey) {
		t.Errorf("API key should be sanitized from error message: %s", errStr)
	}

	// Check that the domain is not in the error message
	if strings.Contains(errStr, "alchemy.com") {
		t.Errorf("Domain should be sanitized from error message: %s", errStr)
	}
}

// TestErrorSanitizationPreservesType tests that sanitizeError preserves error type
func TestErrorSanitizationPreservesType(t *testing.T) {
	originalErr := ethereum.NotFound
	sanitized := sanitizeError(originalErr, "")

	// Should be able to unwrap to original error
	if !errors.Is(sanitized, ethereum.NotFound) {
		t.Error("Sanitized error should preserve original error type for errors.Is")
	}
}

// TestErrorSanitizationInURLPattern tests sanitization of URLs in error messages
func TestErrorSanitizationInURLPattern(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "https URL with API key",
			input:    "failed to connect to https://eth-mainnet.g.alchemy.com/v2/secret-key-123",
			expected: "failed to connect to [redacted]",
		},
		{
			name:     "http URL",
			input:    "connection failed: http://localhost:8545",
			expected: "connection failed: [redacted]",
		},
		{
			name:     "multiple URLs",
			input:    "redirect from https://example.com to http://other.com",
			expected: "redirect from [redacted] to [redacted]",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := sanitizeError(errors.New(tc.input), "")
			errStr := err.Error()
			if errStr != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, errStr)
			}
		})
	}
}

// TestErrorSanitizationWithRPCHost tests sanitization of RPC host in errors
func TestErrorSanitizationWithRPCHost(t *testing.T) {
	rpcURL := "https://api.infura.io/v1/abc123"

	err := sanitizeError(errors.New("connection refused to api.infura.io"), rpcURL)
	errStr := err.Error()

	if strings.Contains(errStr, "api.infura.io") {
		t.Errorf("RPC host should be sanitized: %s", errStr)
	}

	if !strings.Contains(errStr, "[redacted]") {
		t.Errorf("Expected [redacted] in error message, got: %s", errStr)
	}
}

// TestNewFailFast tests that New() fails fast with clear message when RPC is unreachable
func TestNewFailFast(t *testing.T) {
	// Test with invalid URL
	_, err := New("http://invalid-hostname-that-does-not-exist.example.com:8545")
	if err == nil {
		t.Fatal("Expected error for invalid hostname, got nil")
	}

	if !strings.Contains(err.Error(), "startup validation failed") {
		t.Errorf("Expected 'startup validation failed' in error message, got: %s", err.Error())
	}
}

// TestNewWithInvalidURL tests New() with malformed URL
func TestNewWithInvalidURL(t *testing.T) {
	_, err := New("://invalid-url")
	if err == nil {
		t.Fatal("Expected error for invalid URL format, got nil")
	}
}

// blockMockResponse returns a complete block JSON result for go-ethereum's unmarshaler.
// All header fields are required; omitting any causes unmarshal failure.
func blockMockResponse(blockNumber string) map[string]interface{} {
	logsBloom := "0x" + strings.Repeat("00", 256)
	return map[string]interface{}{
		"hash":                  "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"parentHash":            "0x0000000000000000000000000000000000000000000000000000000000000000",
		"sha3Uncles":            "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		"miner":                 "0x0000000000000000000000000000000000000000",
		"stateRoot":             "0x0000000000000000000000000000000000000000000000000000000000000000",
		"transactionsRoot":      "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		"receiptsRoot":          "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		"logsBloom":             logsBloom,
		"difficulty":            "0x0",
		"number":                blockNumber,
		"gasLimit":              "0x1c9c380",
		"gasUsed":               "0x0",
		"timestamp":             "0x64000000",
		"extraData":             "0x",
		"mixHash":               "0x0000000000000000000000000000000000000000000000000000000000000000",
		"nonce":                 "0x0000000000000000",
		"baseFeePerGas":         "0x3b9aca00",
		"withdrawalsRoot":       "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		"blobGasUsed":           "0x0",
		"excessBlobGas":         "0x0",
		"parentBeaconBlockRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
		"transactions":          []interface{}{},
		"uncles":                []interface{}{},
		"withdrawals":           []interface{}{},
	}
}

// txMockResponse returns a complete EIP-1559 transaction JSON result.
func txMockResponse() map[string]interface{} {
	return map[string]interface{}{
		"hash":                 "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"nonce":                "0x5",
		"blockHash":            "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"blockNumber":          "0x1312d00",
		"transactionIndex":     "0x0",
		"from":                 "0x1234567890123456789012345678901234567890",
		"to":                   "0xabcdef1234567890abcdef1234567890abcdef12",
		"value":                "0xde0b6b3a7640000",
		"gas":                  "0x5208",
		"gasPrice":             "0x3b9aca00",
		"maxFeePerGas":         "0x77359400",
		"maxPriorityFeePerGas": "0x3b9aca00",
		"input":                "0x",
		"v":                    "0x1",
		"r":                    "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"s":                    "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"type":                 "0x2",
		"chainId":              "0x1",
		"accessList":           []interface{}{},
	}
}

// respondJSON writes the given value as a JSON-RPC response.
func respondJSON(w http.ResponseWriter, id interface{}, result interface{}) {
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// TestBlockByNumber tests the BlockByNumber method
func TestBlockByNumber(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if method, _ := req["method"].(string); method == "eth_chainId" {
				respondJSON(w, req["id"], "0x1")
				return
			}
			respondJSON(w, req["id"], blockMockResponse("0x1312d00"))
		}))
		defer server.Close()

		client, err := New(server.URL)
		if err != nil {
			t.Fatalf("Failed to create client: %v", err)
		}
		defer client.Close()

		block, err := client.BlockByNumber(context.Background(), big.NewInt(20000000))
		if err != nil {
			t.Fatalf("BlockByNumber failed: %v", err)
		}
		if block == nil {
			t.Fatal("Expected non-nil block")
		}
		if block.NumberU64() != 20000000 {
			t.Errorf("Expected block number 20000000, got %d", block.NumberU64())
		}
	})

	t.Run("not_found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if method, _ := req["method"].(string); method == "eth_chainId" {
				respondJSON(w, req["id"], "0x1")
				return
			}
			respondJSON(w, req["id"], nil)
		}))
		defer server.Close()

		client, err := New(server.URL)
		if err != nil {
			t.Fatalf("Failed to create client: %v", err)
		}
		defer client.Close()

		_, err = client.BlockByNumber(context.Background(), big.NewInt(999999999))
		if !errors.Is(err, ethereum.NotFound) {
			t.Errorf("Expected ethereum.NotFound, got: %v", err)
		}
	})

	t.Run("retry_on_rate_limit", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if method, _ := req["method"].(string); method == "eth_chainId" {
				respondJSON(w, req["id"], "0x1")
				return
			}
			if atomic.AddInt32(&attempts, 1) < 3 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			respondJSON(w, req["id"], blockMockResponse("0x1312d00"))
		}))
		defer server.Close()

		client, err := newClient(server.URL, false)
		if err != nil {
			t.Fatalf("Failed to create client: %v", err)
		}
		defer client.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		block, err := client.BlockByNumber(ctx, big.NewInt(20000000))
		if err != nil {
			t.Fatalf("Expected success after retries, got: %v", err)
		}
		if block == nil {
			t.Fatal("Expected non-nil block")
		}
		if atomic.LoadInt32(&attempts) != 3 {
			t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
		}
	})
}

// TestTransactionByHash tests the TransactionByHash method
func TestTransactionByHash(t *testing.T) {
	txHash := common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if method, _ := req["method"].(string); method == "eth_chainId" {
				respondJSON(w, req["id"], "0x1")
				return
			}
			respondJSON(w, req["id"], txMockResponse())
		}))
		defer server.Close()

		client, err := New(server.URL)
		if err != nil {
			t.Fatalf("Failed to create client: %v", err)
		}
		defer client.Close()

		tx, err := client.TransactionByHash(context.Background(), txHash)
		if err != nil {
			t.Fatalf("TransactionByHash failed: %v", err)
		}
		if tx == nil {
			t.Fatal("Expected non-nil transaction")
		}
		if tx.Nonce() != 5 {
			t.Errorf("Expected nonce 5, got %d", tx.Nonce())
		}
		wantValue := new(big.Int)
		wantValue.SetString("1000000000000000000", 10) // 1 ETH
		if tx.Value().Cmp(wantValue) != 0 {
			t.Errorf("Expected value 1 ETH, got %s", tx.Value().String())
		}
	})

	t.Run("not_found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if method, _ := req["method"].(string); method == "eth_chainId" {
				respondJSON(w, req["id"], "0x1")
				return
			}
			respondJSON(w, req["id"], nil)
		}))
		defer server.Close()

		client, err := New(server.URL)
		if err != nil {
			t.Fatalf("Failed to create client: %v", err)
		}
		defer client.Close()

		_, err = client.TransactionByHash(context.Background(), txHash)
		if !errors.Is(err, ethereum.NotFound) {
			t.Errorf("Expected ethereum.NotFound, got: %v", err)
		}
	})

	t.Run("retry_on_rate_limit", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if method, _ := req["method"].(string); method == "eth_chainId" {
				respondJSON(w, req["id"], "0x1")
				return
			}
			if atomic.AddInt32(&attempts, 1) < 3 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			respondJSON(w, req["id"], txMockResponse())
		}))
		defer server.Close()

		client, err := newClient(server.URL, false)
		if err != nil {
			t.Fatalf("Failed to create client: %v", err)
		}
		defer client.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		tx, err := client.TransactionByHash(ctx, txHash)
		if err != nil {
			t.Fatalf("Expected success after retries, got: %v", err)
		}
		if tx == nil {
			t.Fatal("Expected non-nil transaction")
		}
		if atomic.LoadInt32(&attempts) != 3 {
			t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
		}
	})
}

// TestChainID tests the ChainID method
func TestChainID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  "0x1", // Chain ID 1 (mainnet)
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	chainID, err := client.ChainID(ctx)
	if err != nil {
		t.Fatalf("ChainID failed: %v", err)
	}

	if chainID.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("Expected chain ID 1, got %s", chainID.String())
	}
}

// TestTransactionReceipt tests the TransactionReceipt method
func TestTransactionReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var response map[string]interface{}
		if method, ok := req["method"].(string); ok && method == "eth_chainId" {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x1",
			}
		} else {
			// TransactionReceipt response
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"transactionHash":   "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					"transactionIndex":  "0x0",
					"blockHash":         "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
					"blockNumber":       "0x1",
					"from":              "0x1234567890123456789012345678901234567890",
					"to":                "0xabcdef1234567890abcdef1234567890abcdef12",
					"cumulativeGasUsed": "0x5208",
					"gasUsed":           "0x5208",
					"status":            "0x1",
					"logs":              []interface{}{},
					"logsBloom":         "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
				},
			}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	txHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	receipt, err := client.TransactionReceipt(ctx, txHash)
	if err != nil {
		t.Fatalf("TransactionReceipt failed: %v", err)
	}

	if receipt == nil {
		t.Fatal("Expected non-nil receipt")
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Errorf("Expected successful status, got %d", receipt.Status)
	}
}

// TestBalanceAt tests the BalanceAt method
func TestBalanceAt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var response map[string]interface{}
		if method, ok := req["method"].(string); ok && method == "eth_chainId" {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x1",
			}
		} else {
			// BalanceAt response
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0xde0b6b3a7640000", // 1 ETH in wei
			}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	balance, err := client.BalanceAt(ctx, addr, nil)
	if err != nil {
		t.Fatalf("BalanceAt failed: %v", err)
	}

	expected := new(big.Int)
	expected.SetString("1000000000000000000", 10) // 1 ETH
	if balance.Cmp(expected) != 0 {
		t.Errorf("Expected balance %s, got %s", expected.String(), balance.String())
	}
}

// TestNonceAt tests the NonceAt method
func TestNonceAt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var response map[string]interface{}
		if method, ok := req["method"].(string); ok && method == "eth_chainId" {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x1",
			}
		} else {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x2a", // Nonce 42
			}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	nonce, err := client.NonceAt(ctx, addr, nil)
	if err != nil {
		t.Fatalf("NonceAt failed: %v", err)
	}

	if nonce != 42 {
		t.Errorf("Expected nonce 42, got %d", nonce)
	}
}

// TestCodeAt tests the CodeAt method
func TestCodeAt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var response map[string]interface{}
		if method, ok := req["method"].(string); ok && method == "eth_chainId" {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x1",
			}
		} else {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x608060405234801561001057",
			}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	code, err := client.CodeAt(ctx, addr, nil)
	if err != nil {
		t.Fatalf("CodeAt failed: %v", err)
	}

	if len(code) == 0 {
		t.Error("Expected non-empty code")
	}
}

// TestFeeHistory tests the FeeHistory method
func TestFeeHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var response map[string]interface{}
		if method, ok := req["method"].(string); ok && method == "eth_chainId" {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x1",
			}
		} else {
			// FeeHistory response
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"oldestBlock":   "0x1",
					"baseFeePerGas": []string{"0x3b9aca00", "0x3b9aca00"},
					"gasUsedRatio":  []float64{0.5, 0.6},
				},
			}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	feeHistory, err := client.FeeHistory(ctx, 2, big.NewInt(100), []float64{25, 75})
	if err != nil {
		t.Fatalf("FeeHistory failed: %v", err)
	}

	if feeHistory.OldestBlock.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("Expected oldest block 1, got %s", feeHistory.OldestBlock.String())
	}
}

// TestCallContract tests the CallContract method
func TestCallContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var response map[string]interface{}
		if method, ok := req["method"].(string); ok && method == "eth_chainId" {
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x1",
			}
		} else {
			// CallContract response
			response = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  "0x0000000000000000000000000000000000000000000000000000000000000001",
			}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	toAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	call := ethereum.CallMsg{
		To: &toAddr,
	}
	result, err := client.CallContract(ctx, call, nil)
	if err != nil {
		t.Fatalf("CallContract failed: %v", err)
	}

	if len(result) == 0 {
		t.Error("Expected non-empty result")
	}
}

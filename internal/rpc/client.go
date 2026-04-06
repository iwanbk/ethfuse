package rpc

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"golang.org/x/time/rate"
)

// RPCClient defines the interface for Ethereum RPC operations.
// This interface-first approach enables mocking in tests.
type RPCClient interface {
	BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error)
	TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error)
	TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error)
	NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error)
	CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error)
	ChainID(ctx context.Context) (*big.Int, error)
	FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error)
	CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error)
}

// Client implements RPCClient with rate limiting and retry logic.
type Client struct {
	eth     *ethclient.Client
	limiter *rate.Limiter
}

// rateLimit is the maximum number of requests per second.
const rateLimit = 10

// rateBurst is the maximum burst size for the rate limiter.
const rateBurst = 5

// maxRetries is the maximum number of retry attempts.
const maxRetries = 3

// initialBackoff is the initial backoff duration for retries.
const initialBackoff = 100 * time.Millisecond

// maxBackoff is the maximum backoff duration for retries.
const maxBackoff = 10 * time.Second

// startupValidationTimeout is the timeout for startup validation.
const startupValidationTimeout = 10 * time.Second

// urlPattern matches URLs for sanitization.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// sanitizedError wraps an error with a sanitized message while preserving the original error.
type sanitizedError struct {
	wrapped error
	msg     string
}

func (e *sanitizedError) Error() string { return e.msg }
func (e *sanitizedError) Unwrap() error { return e.wrapped }

// New creates a new RPC client with the given RPC URL.
// It performs startup validation by calling eth_chainId.
// Returns an error if the RPC endpoint is unreachable.
func New(rpcURL string) (*Client, error) {
	return newClient(rpcURL, true)
}

// newClient creates a new RPC client with optional validation.
// If validate is false, the startup chainId check is skipped (for testing).
func newClient(rpcURL string, validate bool) (*Client, error) {
	eth, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, sanitizeError(err, rpcURL)
	}

	// Startup validation: call eth_chainId to fail fast if RPC is unreachable
	if validate {
		ctx, cancel := context.WithTimeout(context.Background(), startupValidationTimeout)
		defer cancel()

		_, err = eth.ChainID(ctx)
		if err != nil {
			eth.Close()
			return nil, fmt.Errorf("startup validation failed: %w", sanitizeError(err, rpcURL))
		}
	}

	limiter := rate.NewLimiter(rate.Limit(rateLimit), rateBurst)

	return &Client{
		eth:     eth,
		limiter: limiter,
	}, nil
}

// Close closes the underlying RPC client connection.
func (c *Client) Close() {
	if c != nil && c.eth != nil {
		c.eth.Close()
	}
}

// BlockByNumber fetches a block by number with retry and rate limiting.
func (c *Client) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	var block *types.Block
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		block, err = c.eth.BlockByNumber(ctx, n)
		return err
	})
	return block, err
}

// TransactionByHash fetches a transaction by hash with retry and rate limiting.
func (c *Client) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, error) {
	var tx *types.Transaction
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		tx, _, err = c.eth.TransactionByHash(ctx, hash)
		return err
	})
	return tx, err
}

// TransactionReceipt fetches a transaction receipt with retry and rate limiting.
func (c *Client) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	var receipt *types.Receipt
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		receipt, err = c.eth.TransactionReceipt(ctx, hash)
		return err
	})
	return receipt, err
}

// BalanceAt fetches the balance of an address at a specific block.
func (c *Client) BalanceAt(ctx context.Context, addr common.Address, block *big.Int) (*big.Int, error) {
	var balance *big.Int
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		balance, err = c.eth.BalanceAt(ctx, addr, block)
		return err
	})
	return balance, err
}

// NonceAt fetches the nonce of an address at a specific block.
func (c *Client) NonceAt(ctx context.Context, addr common.Address, block *big.Int) (uint64, error) {
	var nonce uint64
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		nonce, err = c.eth.NonceAt(ctx, addr, block)
		return err
	})
	return nonce, err
}

// CodeAt fetches the code at a specific address and block.
func (c *Client) CodeAt(ctx context.Context, addr common.Address, block *big.Int) ([]byte, error) {
	var code []byte
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		code, err = c.eth.CodeAt(ctx, addr, block)
		return err
	})
	return code, err
}

// ChainID fetches the chain ID.
func (c *Client) ChainID(ctx context.Context) (*big.Int, error) {
	var chainID *big.Int
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		chainID, err = c.eth.ChainID(ctx)
		return err
	})
	return chainID, err
}

// FeeHistory fetches fee history for gas estimation.
func (c *Client) FeeHistory(ctx context.Context, blockCount uint64, lastBlock *big.Int, rewardPercentiles []float64) (*ethereum.FeeHistory, error) {
	var feeHistory *ethereum.FeeHistory
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		feeHistory, err = c.eth.FeeHistory(ctx, blockCount, lastBlock, rewardPercentiles)
		return err
	})
	return feeHistory, err
}

// CallContract calls a contract with the given call message.
func (c *Client) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	var result []byte
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var err error
		result, err = c.eth.CallContract(ctx, call, block)
		return err
	})
	return result, err
}

// isRetryableError checks if an error should be retried.
// Returns false for non-retryable errors like NotFound or context cancellation.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// Don't retry on context cancellation
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Don't retry on not found errors
	if errors.Is(err, ethereum.NotFound) {
		return false
	}
	return true
}

// withRetry executes the given function with exponential backoff retry.
// It also applies rate limiting before each attempt.
func (c *Client) withRetry(ctx context.Context, fn func(context.Context) error) error {
	var lastErr error
	backoff := initialBackoff

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Apply rate limiting
		if err := c.limiter.Wait(ctx); err != nil {
			return sanitizeError(err, "")
		}

		// Execute the function
		if err := fn(ctx); err == nil {
			return nil
		} else {
			lastErr = err
			// Don't retry non-retryable errors
			if !isRetryableError(err) {
				return sanitizeError(err, "")
			}
		}

		// Don't sleep on the last attempt
		if attempt < maxRetries-1 {
			select {
			case <-time.After(backoff):
				// Exponential backoff with cap (integer arithmetic)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			case <-ctx.Done():
				return sanitizeError(ctx.Err(), "")
			}
		}
	}

	return sanitizeError(lastErr, "")
}

// sanitizeError removes URLs and API keys from error messages.
// It preserves the original error type by wrapping it.
func sanitizeError(err error, rpcURL string) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// Sanitize the provided RPC URL if present
	if rpcURL != "" {
		if u, parseErr := url.Parse(rpcURL); parseErr == nil && u.Host != "" {
			errStr = strings.ReplaceAll(errStr, u.Host, "[redacted]")
		}
	}

	// Sanitize any URLs found in the error message
	errStr = urlPattern.ReplaceAllString(errStr, "[redacted]")

	// Wrap the original error to preserve error type for errors.Is/As
	return &sanitizedError{msg: errStr, wrapped: err}
}

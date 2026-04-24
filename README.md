# ethfuse

Mount the Ethereum blockchain as a FUSE filesystem. Navigate blocks, transactions, accounts, and ENS names as ordinary files and directories.

```bash
cat /mnt/eth/chain/latest_block
cat /mnt/eth/blocks/21000042/info.json
cat /mnt/eth/accounts/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/balance
cat /mnt/eth/ens/vitalik.eth/balance
```

---

## Features

- **Blocks** — browse any block by number; list the 100 most recent blocks
- **Transactions** — read transaction data and receipts, via block or direct hash lookup
- **Accounts** — query balance, nonce, and bytecode for any address
- **ENS** — resolve `.eth` names as symlinks to their underlying account directories
- **Chain metadata** — chain ID, name, latest block number, and real-time gas estimates
- **Two-tier cache** — volatile data in TTL memory cache; finalized blocks/txs persisted to bbolt
- **Read-only** — safe to mount against any Ethereum node; all writes return `EROFS`

---

## Requirements

- Linux or macOS with FUSE support
  - macOS: install [macFUSE](https://osxfuse.github.io/)
  - Linux: `libfuse3` (most distributions include it)
- Go 1.25+
- An Ethereum JSON-RPC endpoint (Alchemy, Infura, or a local node)

---

## Installation

```bash
go install github.com/iwanbk/ethfuse/cmd/ethfuse@latest
```

Or build from source:

```bash
git clone https://github.com/iwanbk/ethfuse
cd ethfuse
go build -o ethfuse ./cmd/ethfuse
```

---

## Configuration

The RPC URL is required. It is resolved in this order:

| Source | Example |
|--------|---------|
| `-rpc-url` flag | `-rpc-url https://eth-mainnet.g.alchemy.com/v2/KEY` |
| `ETHFUSE_RPC_URL` env var | `export ETHFUSE_RPC_URL=https://...` |
| `~/.config/ethfuse/config.toml` | `rpc_url = "https://..."` |

`config.toml` example:

```toml
rpc_url = "https://eth-mainnet.g.alchemy.com/v2/YOUR_API_KEY"
```

---

## Usage

You'll need an Ethereum JSON-RPC URL. Free tiers are available from:

- [Alchemy](https://www.alchemy.com/)
- [Infura](https://www.infura.io/)
- [QuickNode](https://www.quicknode.com/)
- [Ankr](https://www.ankr.com/)
- Public endpoints from [chainlist.org](https://chainlist.org/) (no signup, lower rate limits)

```bash
# Create a mountpoint
mkdir /mnt/eth

# Mount (blocks until Ctrl-C or SIGTERM)
ethfuse -rpc-url https://eth-mainnet.g.alchemy.com/v2/KEY /mnt/eth

# Or with env var
export ETHFUSE_RPC_URL=https://eth-mainnet.g.alchemy.com/v2/KEY
ethfuse /mnt/eth
```

Unmount cleanly with Ctrl-C or `umount /mnt/eth` (macOS: `diskutil unmount /mnt/eth`).

---

## Filesystem Layout

```
/mnt/eth/
├── chain/
│   ├── id                          # "1"
│   ├── name                        # "Ethereum Mainnet"
│   ├── latest_block                # current head block number
│   └── gas/
│       ├── slow                    # P25 fee estimate (gwei)
│       ├── standard                # P50 fee estimate (gwei)
│       └── fast                    # P75 fee estimate (gwei)
│
├── blocks/
│   ├── recent/                     # last 100 blocks (visible in ls)
│   │   └── [number]/
│   └── [number]/                   # any block — ghost node (not in ls)
│       ├── info.json
│       ├── hash
│       ├── parent_hash
│       ├── timestamp
│       └── txs/
│           └── [0xhash]/
│               ├── info.json
│               └── receipt.json
│
├── txs/
│   └── [0xhash]/                   # ghost node — direct lookup by hash
│       ├── info.json
│       └── receipt.json
│
├── accounts/
│   └── [0xaddress]/                # ghost node — not shown in ls
│       ├── balance                 # wei as decimal string
│       ├── nonce
│       └── code                    # "0x" for EOA; bytecode for contracts
│
└── ens/
    └── [name.eth]                  # symlink → ../accounts/[resolved address]/
```

**Ghost nodes** are addressable by path but do not appear in `ls`. This keeps directory listings manageable while still allowing direct access to any block number, transaction hash, or account address.

### File formats

- **Scalar files** (`hash`, `balance`, `nonce`, `timestamp`, …): plain text, newline-terminated — pipe-friendly
- **JSON files** (`info.json`, `receipt.json`): pretty-printed, 2-space indent
- **Large numbers** (`balance`, `value`): decimal string — avoids shell integer overflow

---

## Example Commands

```bash
# Chain info
cat /mnt/eth/chain/id
cat /mnt/eth/chain/latest_block
cat /mnt/eth/chain/gas/fast

# Block data
cat /mnt/eth/blocks/21000042/info.json
cat /mnt/eth/blocks/21000042/hash
ls  /mnt/eth/blocks/21000042/txs/

# Transaction
cat /mnt/eth/txs/0xabc123.../info.json
cat /mnt/eth/txs/0xabc123.../receipt.json

# Account
cat /mnt/eth/accounts/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/balance
cat /mnt/eth/accounts/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/nonce

# ENS
ls  /mnt/eth/ens/vitalik.eth/
cat /mnt/eth/ens/vitalik.eth/balance

# Recent blocks
ls /mnt/eth/blocks/recent/

# Script: sum gas used across last 10 blocks
for n in $(ls /mnt/eth/blocks/recent/ | tail -10); do
  cat /mnt/eth/blocks/recent/$n/info.json | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['gasUsed'])"
done | paste -sd+ | bc
```

---

## Architecture

For a detailed breakdown of the layer architecture, node design, cache TTL strategy, error mapping, and request lifecycle, see [architecture.md](architecture.md).

---

## Development

```bash
# Build
go build ./...

# Unit tests
go test ./...

# Integration tests (requires a live RPC endpoint)
go test -tags=integration -rpc-url "$ETHFUSE_RPC_URL" ./...

# Lint
go fmt ./...
go vet ./...
golangci-lint run
```

The RPC client interface (`internal/rpc.RPCClient`) is injected via dependency injection, so node tests use a `MockRPCClient` without hitting the network.

---

## License

BSD 3-Clause

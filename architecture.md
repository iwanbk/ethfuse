# Architecture: ethfuse (Go/Ethereum)

---

## 1. Overview

### Workflow Coverage

| Workflow | Etherscan equivalent | ethfuse | Status |
|----------|----------------------|-------|--------|
| View latest block | "Latest Block" on homepage | `cat chain/latest_block` | v1 |
| Browse recent blocks | "Latest Blocks" list | `ls blocks/recent/` | v1 |
| Block detail by number | `/block/21000000` | `cat blocks/21000000/info.json` | v1 |
| List transactions in a block | Block → Transactions tab | `ls blocks/21000000/txs/` | v1 |
| Transaction detail | `/tx/0xabc...` | `cat txs/0xabc.../info.json` | v1 |
| Transaction status & gas used | TX → receipt | `cat txs/0xabc.../receipt.json` | v1 |
| Check address balance | `/address/0xabc...` | `cat accounts/0xabc.../balance` | v1 |
| Check address nonce | Address → Nonce | `cat accounts/0xabc.../nonce` | v1 |
| Check if address is a contract | Address → Code tab | `cat accounts/0xabc.../code` | v1 |
| ENS name lookup | Search `vitalik.eth` | `ls ens/vitalik.eth/` | v1 |
| Gas price tracker | Gas Tracker | `cat chain/gas/fast` | v1 |
| Chain info | Network selector | `cat chain/id`, `cat chain/name` | v1 |
| Pending transactions | Mempool / pending txs | `ls blocks/pending/txs/` | v2 |
| Internal transactions | TX → Internal Txns tab | `ls txs/0xabc.../internal/` | v2 |
| Token balances (ERC20) | Address → Tokens tab | `ls accounts/[addr]/tokens/` | v2 |
| Contract ABI | Contract → ABI tab | `cat accounts/[contract]/abi.json` | v2 |
| Decoded tx input | TX → Input Data (decoded) | `cat txs/[hash]/decoded_input.json` | v2 |
| Historical tx list per address | Address → Transactions tab | `ls accounts/[addr]/txs/` | v3 — requires external indexer (Alchemy / Etherscan API) |
| Verify contract source | Contract → Verify & Publish | — | out of scope — write operation to external service |

---

## 2. Filesystem Design

### Layer Architecture

```
┌─────────────────────────────────────┐
│           CLI (stdlib flag)         │
│   ethfuse mount /mnt/eth --rpc=...    │
└──────────────────┬──────────────────┘
                   │ mount()
┌──────────────────▼──────────────────┐
│        FUSE Layer (go-fuse/v2)      │
│   intercepts: lookup, getattr,      │
│   readdir, read                     │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│         Virtual Node Tree           │
│  RootNode → BlocksDirNode →         │
│  BlockNode → TxNode → ...           │
└──────────────────┬──────────────────┘
                   │ cache miss
┌──────────────────▼──────────────────┐
│           Cache Layer               │
│  ttlcache: volatile data + TTL      │
│  bbolt: finalized data, persistent  │
└──────────────────┬──────────────────┘
                   │ cache miss
┌──────────────────▼──────────────────┐
│           RPC Client                │
│  ethclient + retry + backoff        │
└──────────────────┬──────────────────┘
                   │
┌──────────────────▼──────────────────┐
│        Ethereum Node                │
│  Alchemy / Infura / self-hosted     │
└─────────────────────────────────────┘
```

### Filesystem Structure

```
/
├── blocks/
│   ├── latest/               ← TTL 12s, always fresh
│   │   ├── info.json
│   │   ├── hash
│   │   ├── parent_hash
│   │   ├── timestamp
│   │   └── txs/
│   ├── pending/              ← [v2] never cached, always live
│   │   └── txs/
│   ├── recent/               ← last 100 blocks (visible in ls)
│   │   ├── 21000000/
│   │   ├── 21000001/
│   │   └── ...
│   └── [number]/             ← ghost: not shown in ls,
│       ├── info.json           but accessible directly by path
│       ├── hash
│       ├── parent_hash
│       ├── timestamp
│       └── txs/
│           └── [0xhash]/
│               ├── info.json
│               └── receipt.json
├── txs/
│   └── [0xhash]/             ← ghost files
│       ├── info.json
│       └── receipt.json
├── accounts/
│   └── [0xaddress]/
│       ├── balance           ← in wei
│       ├── nonce
│       ├── code              ← 0x for EOA, hex bytecode for contract
│       └── txs/              ← [v3] historical txs — symlinks to txs/[0xhash]/
│           └── [0xhash] → ../../txs/[0xhash]/
├── chain/
│   ├── id
│   ├── name
│   ├── latest_block
│   └── gas/
│       ├── slow          ← estimated gwei for slow confirmation
│       ├── standard      ← estimated gwei for normal confirmation
│       └── fast          ← estimated gwei for fast confirmation
└── ens/
    └── [name.eth]/       ← ghost: resolves ENS → symlink to accounts/[0xaddr]/
```

### Blockchain → Filesystem Mapping

| Blockchain Concept | Filesystem Representation |
|--------------------|--------------------------|
| Block | directory `blocks/[number]/` |
| Transaction | directory `txs/[0xhash]/` (also nested under block) |
| Account / Contract | directory `accounts/[0xaddress]/` |
| Chain metadata + gas estimates | directories `chain/`, `chain/gas/` |
| ENS name | directory `ens/[name.eth]/` → symlink to `accounts/[0xaddr]/` |
| Scalar field (hash, number, string) | plain text file (one value per file) |
| Group of related fields | `info.json` file |
| Event log / receipt | `receipt.json` file |

#### File contents per node

**`blocks/[number]/`** — source: `eth_getBlockByNumber`

| File | Content | Type |
|------|---------|------|
| `info.json` | `{ number, hash, parentHash, timestamp, miner, gasUsed, gasLimit, baseFeePerGas, difficulty, size, txCount }` | JSON |
| `hash` | hex string, e.g. `0xabc...` | plain text |
| `parent_hash` | hex string | plain text |
| `timestamp` | decimal Unix timestamp, e.g. `1712345678` | plain text |

**`blocks/[number]/txs/[0xhash]/`** and **`txs/[0xhash]/`** — source: `eth_getTransactionByHash` + `eth_getTransactionReceipt`

| File | Content | Type |
|------|---------|------|
| `info.json` | `{ hash, from, to, value, gas, gasPrice, maxFeePerGas, maxPriorityFeePerGas, nonce, input, blockNumber, transactionIndex }` | JSON |
| `receipt.json` | `{ status, gasUsed, effectiveGasPrice, contractAddress, logs, logsBloom }` | JSON |

> `value` is in wei (decimal string, not hex) to avoid overflow when read by tools like `awk`.
> `input` is raw hex calldata; ABI decoding is out of scope for the filesystem.

**`accounts/[0xaddress]/`** — source: `eth_getBalance`, `eth_getTransactionCount`, `eth_getCode`

| File | Content | Type |
|------|---------|------|
| `balance` | balance in wei, decimal string | plain text |
| `nonce` | decimal number | plain text |
| `code` | hex bytecode (`0x` for EOA, `0x...` for contract) | plain text |

**`chain/`** — source: `eth_chainId`, `net_version`, `eth_blockNumber`

| File | Content | Type |
|------|---------|------|
| `id` | decimal chain ID, e.g. `1` | plain text |
| `name` | human-readable name, e.g. `mainnet` | plain text |
| `latest_block` | latest block number, decimal | plain text |

**`chain/gas/`** — source: `eth_feeHistory`

| File | Content | Type |
|------|---------|------|
| `slow` | estimated base+tip in gwei, decimal string | plain text |
| `standard` | estimated base+tip in gwei, decimal string | plain text |
| `fast` | estimated base+tip in gwei, decimal string | plain text |

> TTL 5s — same as `blocks/latest/`. Calculation: `slow` = P25, `standard` = P50, `fast` = P75 from `eth_feeHistory` over last 5 blocks.

**`ens/[name.eth]/`** — source: `eth_call` to ENS registry (`0x00000000000C2E074eC69A0dFb2997BA6C7d2e1e`)

| Entry | Content |
|-------|---------|
| directory `ens/vitalik.eth/` | symlink to `accounts/0xd8dA.../` via `NodeReadlinker` |

> If ENS does not resolve (name not registered): `ENOENT`. Result cached in ttlcache TTL 300s — ENS names rarely change.

#### Format principles

- **Scalar files** (hash, balance, nonce, timestamp): plain text terminated with `\n` — so they can be `cat`-ed and piped to Unix tools directly.
- **JSON files**: pretty-printed with 2-space indent — so `cat` remains readable without `jq`.
- **All large numbers** (value, balance): decimal string, not hex — avoids overflow in shell.

### Example Outputs

**chain/**
```
$ cat chain/id
1

$ cat chain/name
mainnet

$ cat chain/latest_block
21000042

$ cat chain/gas/slow
12
$ cat chain/gas/standard
18
$ cat chain/gas/fast
25
```

**blocks/**
```
$ ls blocks/recent/
21000042
21000041
21000040
...

$ cat blocks/21000042/hash
0x3d9a6b8e1f2c5a7d0e4b9c2f8a1d3e6b5c4f7a2e9d0b3c6a5f8e1d4b7c0a3f6

$ cat blocks/21000042/timestamp
1712345678

$ cat blocks/21000042/info.json
{
  "number": 21000042,
  "hash": "0x3d9a6b8e...",
  "parentHash": "0x1a2b3c4d...",
  "timestamp": 1712345678,
  "miner": "0xab5801a7d398351b8be11c439e05c5b3259aec9b",
  "gasUsed": 15000000,
  "gasLimit": 30000000,
  "baseFeePerGas": 12000000000,
  "difficulty": 0,
  "size": 42103,
  "txCount": 187
}

$ ls blocks/21000042/txs/
0xabc123...
0xdef456...
0x789abc...
```

**txs/**
```
$ cat txs/0xabc123.../info.json
{
  "hash": "0xabc123...",
  "from": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
  "to": "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
  "value": "0",
  "gas": 100000,
  "gasPrice": "18000000000",
  "maxFeePerGas": "25000000000",
  "maxPriorityFeePerGas": "1500000000",
  "nonce": 382,
  "input": "0xa9059cbb000000000000000000000000...",
  "blockNumber": 21000042,
  "transactionIndex": 12
}

$ cat txs/0xabc123.../receipt.json
{
  "status": 1,
  "gasUsed": 65000,
  "effectiveGasPrice": "18000000000",
  "contractAddress": null,
  "logs": [...],
  "logsBloom": "0x000000..."
}
```

**accounts/**
```
$ cat accounts/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/balance
1234560000000000000000

$ cat accounts/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/nonce
382

$ cat accounts/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/code
0x

$ cat accounts/0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48/code
0x60806040523615...
```

> `balance` is in wei — no unit suffix. Convert to ETH: `echo "$(cat balance) / 10^18" | bc -l`
> `code` is `0x` for EOA (not empty string).

**ens/**
```
$ ls ens/
vitalik.eth -> ../accounts/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/

$ ls ens/vitalik.eth/
balance
code
nonce

$ cat ens/vitalik.eth/balance
1234560000000000000000
```

> `ls ens/vitalik.eth/` follows the symlink — standard Unix behavior, no special handling needed.

---

## 3. Implementation

### Node Design

go-fuse uses a node-based API: each path segment is a struct that implements interfaces according to its capabilities.

```
Interfaces implemented per node:

NodeLookuper   → resolve child by name (called by kernel before anything else)
NodeReaddirer  → list directory contents (ls)
NodeGetattrer  → file attributes: size, mode, mtime (stat)
NodeReader     → read file contents (cat)
NodeReadlinker → read symlink target (for ENS)
```

```
RootNode
  └── BlocksDirNode       (NodeLookuper, NodeReaddirer)
        ├── RecentDirNode (NodeReaddirer → last 100)
        ├── LatestDirNode (NodeReaddirer → TTL 5s)
        └── BlockNode     (NodeLookuper, NodeReaddirer, NodeGetattrer)
              ├── FileNode: info.json, hash, ...  (NodeReader)
              └── TxsDirNode
                    └── TxNode      (NodeLookuper, NodeReaddirer)
                          └── FileNode: info.json, receipt.json
  └── TxsDirNode          (NodeLookuper — ghost only, no readdir)
        └── TxNode
  └── AccountsDirNode     (NodeLookuper — ghost only)
        └── AccountNode   (NodeReaddirer)
              └── FileNode: balance, nonce, code
  └── ChainNode           (NodeReaddirer)
        ├── FileNode: id, name, latest_block
        └── GasDirNode    (NodeReaddirer)
              └── FileNode: slow, standard, fast  (NodeReader, TTL 5s)
  └── ENSDirNode          (NodeLookuper — ghost only)
        └── ENSNode       (NodeReadlinker → symlink to AccountNode)
```

### Request Lifecycle

Flow when the kernel reads a file (example: `cat /blocks/latest/hash`):

```
kernel (VFS)
    │  read("/blocks/latest/hash")
    ▼
FUSE Layer (go-fuse/v2)
    │  dispatch to NodeReader / NodeGetattrer
    ▼
Virtual Node (LatestDirNode → FileNode)
    │
    ├─── ttlcache hit? ──────────────────────────────────► return data
    │
    │    ttlcache miss
    ├─── finalized data? ── bbolt hit? ─────────────────► return data
    │                           │
    │                       bbolt miss
    │
    ▼
ethclient (RPC call + retry/backoff)
    │
    ├─── error? ──── rate limit → EAGAIN
    │                timeout    → EIO
    │                not found  → ENOENT
    │
    ▼  success
parse response
    │
    ├─── volatile data  → write to ttlcache (with TTL)
    └─── finalized data → write to bbolt (permanent)
    │
    ▼
return data to kernel
```

> Retry and backoff are entirely within the RPC Client layer — layers above do not need to know.
> Nodes hold no state; all cache access goes through a centralized interface.

### Cache TTL Strategy

| Data | TTL | Storage |
|------|-----|---------|
| `chain/latest_block` | 10s | ttlcache |
| `blocks/latest/` | 12s | ttlcache (one block time) |
| `chain/gas/` | 5s | ttlcache |
| Account state | 30s | ttlcache |
| Negative cache (invalid path) | 30s | ttlcache |
| ENS name resolution | 300s | ttlcache |
| Block headers (finalized, ≥12 conf) | ∞ | bbolt |
| Tx data (finalized) | ∞ | bbolt |
| Pending tx | Not cached | — |

**Why two storages:** ttlcache for data that needs TTL (volatile), bbolt for immutable data that must survive restarts. bbolt has no native TTL — that's fine because finalized data never needs to expire.

### Inode Numbering

- Root inode = **1** (hardcoded by FUSE)
- Use deterministic hash from path: `hash/fnv` — `fnv.New64a()` from path string
- For a read-only filesystem, generation numbers are not needed

### Error Mapping

| Condition | FUSE errno |
|-----------|------------|
| Block/tx/account not found | `ENOENT` |
| RPC rate limit | `EAGAIN` |
| Timeout / network error | `EIO` |
| Invalid address/hash format | `EINVAL` |
| Write to read-only filesystem | `EROFS` |

---

## 4. Testing Strategy

### Layers and boundaries

```
┌──────────────────────────────────────────────────────┐
│                   Integration tests                  │
│   mount real FUSE + real RPC (Sepolia/Holesky)       │
│   verify actual file contents from terminal          │
├──────────────────────────────────────────────────────┤
│                     Unit tests                       │
│   each node type in isolation with MockRPCClient     │
│   no FUSE, no network, no disk                       │
├──────────────────────────────────────────────────────┤
│                     RPC tests                        │
│   Client.retry behavior with a fake HTTP server      │
│   error sanitization (API key not leaked)            │
└──────────────────────────────────────────────────────┘
```

### Unit tests — per node type

Each node is tested in isolation by injecting `MockRPCClient`. No FUSE mount, no network.

| Node | What to test |
|------|-------------|
| `BlockNode` | `info.json` fields match RPC response; scalar files (`hash`, `timestamp`) terminated with `\n`; `ENOENT` for unknown number |
| `TxNode` | `info.json` built from block cache (no extra RPC call); `receipt.json` lazy-fetched only on read; `value` is decimal string not hex |
| `AccountNode` | `balance`/`nonce`/`code` correct values; `code` is `0x` for EOA (not empty string); `EINVAL` for malformed address |
| `ChainNode` | `latest_block` reflects background refresh; `gas/` files use P25/P50/P75 from `FeeHistory` |
| `ENSNode` | symlink target is correct path; `ENOENT` for unregistered name; result served from cache on second call (no extra RPC call) |
| Cache layer | ttlcache expires after TTL; bbolt survives restart; negative cache blocks repeated RPC calls |

### RPC layer tests

Use a fake HTTP server (stdlib `net/http/httptest`) — no real node needed.

| Scenario | Expected behavior |
|----------|-------------------|
| HTTP 429 (rate limit) | retry with backoff, eventually return `EAGAIN` |
| HTTP 500 / timeout | retry with backoff, eventually return `EIO` |
| API key in error message | stripped to `[redacted]` before reaching caller |
| Startup with unreachable RPC | fail fast with clear message, non-zero exit |

### Integration tests

Run against Sepolia or Holesky testnet with a real RPC URL. Guarded by build tag so they don't run by default:

```bash
go test -tags=integration -rpc-url $ETHFS_RPC_URL ./...
```

| Scenario | What to verify |
|----------|---------------|
| `cat chain/latest_block` | returns a decimal integer, increments over time |
| `cat blocks/[number]/info.json` | valid JSON, fields match known block on testnet |
| `cat txs/[hash]/receipt.json` | correct `status` and `gasUsed` for a known tx |
| `cat accounts/[addr]/balance` | matches balance from `eth_getBalance` directly |
| `ls blocks/recent/` | returns exactly 100 entries |
| `cat ens/[name]/balance` | matches underlying account balance |
| Write attempt | returns `EROFS` |
| Invalid address lookup | returns `EINVAL` |
| Ghost block access | accessible by path even though not in `ls` |

### What is not tested

- FUSE kernel behavior (that is go-fuse's responsibility)
- Ethereum consensus rules (that is go-ethereum's responsibility)
- RPC provider uptime or data correctness

---

## 5. Dependencies

### Library Decisions

| Component | Library | Reason |
|-----------|---------|--------|
| FUSE | `hanwen/go-fuse/v2` | Actively maintained, FUSE3, node-based API, used by rclone |
| Ethereum RPC | `go-ethereum/ethclient` | Official library, mature |
| Cache (memory) | `jellydator/ttlcache` | Native per-key TTL, generics, simple, for volatile data |
| Cache (disk) | `go.etcd.io/bbolt` | Embedded, pure Go, ACID, for finalized data |
| CLI | stdlib `flag` | Sufficient for a single `mount` subcommand |
| Config | `BurntSushi/toml` + `os.Getenv` | Lightweight, no need for viper |
| Logging | `log/slog` | Stdlib Go 1.21+, no extra dependency |

> Avoid: `bazil.org/fuse` (inactive since 2022), `cgofuse` (no FUSE3 roadmap)
> Avoid: `syndtr/goleveldb` (unmaintained)
> Alternative if you want a single library for both: `dgraph-io/badger` (native TTL + disk), but heavier than bbolt
> Alternative memory cache without dependency: `sync.Map` + `expiredAt time.Time` in value

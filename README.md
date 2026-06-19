# go-multiseed

A multi-blockchain Bitcoin P2P DNS seeder written in Go. Simultaneously crawls and serves seed IPs for up to any number of bitcoin-based blockchain networks from a single binary.

Rewrite of [multiseeder](https://github.com/team-exor/generic-seeder) with zero global state, native Cloudflare integration, and a clean per-coin architecture.

## Features

- Crawls multiple blockchains in parallel — one goroutine pool per coin, no shared global state
- Built-in UDP DNS server routes queries by hostname to the correct coin's address pool
- Exponential reliability scoring over five time windows (2H / 8H / 1D / 1W / 1M)
- Native Cloudflare DNS sync — no Python dependency
- HTTP block-height polling with primary + fallback explorer URLs
- Address database persisted as JSON; survives restarts
- Compatible with existing `settings.conf` format from the C++ generic-seeder

## Requirements

- Go 1.21+
- Linux (Ubuntu 18.04+ / Debian 8+ recommended)

## Build

```bash
git clone https://github.com/senasgr-eth/go-seeder.git
cd go-seeder
make
```

Or directly:
```bash
/usr/local/go/bin/go build -o multiseed ./cmd/multiseeder/
```

## Configuration

The seeder auto-discovers coin configs by scanning `/usr/local/sDNS.*/settings.conf` at startup.

### Setting up a coin

```bash
# Create the data directory for your coin
sudo mkdir -p /usr/local/sDNS.BTC

# Copy and edit the example config
sudo cp settings.conf.example /usr/local/sDNS.BTC/settings.conf
sudo nano /usr/local/sDNS.BTC/settings.conf
```

For multi-coin, repeat for each coin:
```bash
sudo mkdir -p /usr/local/sDNS.LTC
sudo cp settings.conf.example /usr/local/sDNS.LTC/settings.conf
# edit wallet_port, seeds, magic bytes, host, etc.
```

### Key fields to fill in

| Field | Where to find it |
|-------|-----------------|
| `pchMessageStart_0..3` | `src/chainparams.cpp` → `pchMessageStart` |
| `protocol_version` | `src/version.h` → `PROTOCOL_VERSION` |
| `min_peer_proto_version` | `src/version.h` → `MIN_PEER_PROTO_VERSION` |
| `wallet_port` | `src/chainparams.cpp` → `nDefaultPort` |
| `seed_1..10` | `src/chainparams.cpp` → `vSeeds` |
| `block_count` | Any block explorer — set to a recent block height |

See [`settings.conf.example`](settings.conf.example) for full documentation of every field.

## Running

### Local DNS server mode

The built-in DNS server listens on UDP port 53. Running on port 53 requires either root or a capability grant:

```bash
# Option A — run as root
sudo ./multiseed

# Option B — grant capability once, then run without sudo
sudo setcap 'cap_net_bind_service=+ep' ./multiseed
./multiseed
```

### Non-standard port (no root required)

Redirect port 53 → 5353 via iptables, then run on 5353:
```bash
sudo iptables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-port 5353
# In settings.conf, set: nPort = "5353"
./multiseed
```

### Single config file

Skip auto-discovery and load one config explicitly:
```bash
./multiseed -config /path/to/settings.conf
```

### Cloudflare mode

Set `cf_api_key` in `settings.conf`. The seeder pushes good IPs to Cloudflare DNS automatically every 30 minutes — no separate cron or Python script needed.

Verify it works:
```bash
nslookup seed-btc.example.com
```

## DNS zone setup

To use local DNS server mode, add these records to your domain's DNS zone:

| Type | Name | Value |
|------|------|-------|
| A | `vps` | `<your-server-IP>` |
| NS | `seed-btc` | `vps.example.com` |

The `host` field in `settings.conf` must match the NS record (`seed-btc.example.com`).

## Data directory

Each coin's data is stored in its config directory:

```
/usr/local/sDNS.BTC/
├── settings.conf   ← config (you create this)
├── addrdb.json     ← persisted address database (auto-generated)
└── dnsseed.dump    ← good nodes list for Cloudflare / monitoring (auto-generated)
```

## Architecture

```
cmd/multiseeder/       — entry point, config discovery, signal handling
internal/
  config/             — settings.conf parser (libconfig-compatible)
  addr/               — AddrDB: node tracking with exponential decay stats
  p2p/                — Bitcoin P2P wire protocol (version handshake, addr messages)
  crawler/            — goroutine pool that dials peers and feeds results to AddrDB
  height/             — HTTP poller for current block height
  dns/                — single UDP DNS server, routes by coin hostname
  cloudflare/         — Cloudflare DNS REST API client
  coin/               — per-coin Manager: owns AddrDB, Crawler, HeightPoller, DNS registration
```

Each coin is an independent `coin.Manager` struct — there is no global state. Adding a new coin is purely config: drop a `settings.conf` and restart.

## Install

```bash
make install   # copies binary to /usr/local/bin/multiseed (requires sudo)
```

Or manually:
```bash
sudo cp multiseed /usr/local/bin/
```

### systemd service (optional)

```ini
[Unit]
Description=go-multiseed DNS seeder
After=network.target

[Service]
ExecStart=/usr/local/bin/multiseed
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

```bash
sudo cp multiseed.service /etc/systemd/system/
sudo systemctl enable --now multiseed
sudo journalctl -fu multiseed
```

## License

MIT

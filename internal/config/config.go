package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type DNSConfig struct {
	Port        int
	Threads     int
	NS          string
	Host        string
	Mbox        string
	IPAddr      string
}

type CloudflareConfig struct {
	Domain       string
	DomainPrefix string
	Username     string
	APIKey       string
	SeedDumpFile string
	ServicesBits []uint64
}

type CoinConfig struct {
	BlockchainName      string
	Ticker              string
	CrawlerThreads      int
	ProtocolVersion     int32
	InitProtoVersion    int32
	MinPeerProtoVersion int32
	CAddrTimeVersion    int32
	NetMagic            [4]byte
	WalletPort          uint16
	ExplorerURL         string
	SecondExplorerURL   string
	ExplorerRequerySecs int
	BlockCount          int64
	Seeds               []string
	DNS                 DNSConfig
	CF                  CloudflareConfig
}

func Load(path string) (*CoinConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	kv := make(map[string]string)
	scanner := bufio.NewScanner(f)
	inBlock := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "/*") {
			inBlock = true
		}
		if inBlock {
			if strings.Contains(line, "*/") {
				inBlock = false
			}
			continue
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"`)
		val = strings.TrimSpace(val)
		kv[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	cfg := &CoinConfig{
		CrawlerThreads:      96,
		ExplorerRequerySecs: 120,
		DNS: DNSConfig{
			Port:    53,
			Threads: 4,
		},
	}

	cfg.BlockchainName = kv["blockchain_name"]
	cfg.Ticker = kv["ticker"]
	cfg.ExplorerURL = kv["explorer_url"]
	cfg.SecondExplorerURL = kv["second_explorer_url"]
	cfg.DNS.NS = kv["ns"]
	cfg.DNS.Host = kv["host"]
	cfg.DNS.Mbox = kv["mbox"]
	cfg.DNS.IPAddr = kv["ip_addr"]
	cfg.CF.Domain = kv["cf_domain"]
	cfg.CF.DomainPrefix = kv["cf_domain_prefix"]
	cfg.CF.Username = kv["cf_username"]
	cfg.CF.APIKey = kv["cf_api_key"]
	cfg.CF.SeedDumpFile = kv["cf_seed_dump"]
	cfg.CF.ServicesBits = parseServicesBits(kv["cf_svc_bits"])

	parseInt := func(key string, def int) int {
		if v := kv[key]; v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	}

	cfg.CrawlerThreads = parseInt("nThreads", cfg.CrawlerThreads)
	cfg.DNS.Port = parseInt("nPort", cfg.DNS.Port)
	cfg.DNS.Threads = parseInt("nDnsThreads", cfg.DNS.Threads)
	cfg.ProtocolVersion = int32(parseInt("protocol_version", 70015))
	cfg.InitProtoVersion = int32(parseInt("init_proto_version", 209))
	cfg.MinPeerProtoVersion = int32(parseInt("min_peer_proto_version", 209))
	cfg.CAddrTimeVersion = int32(parseInt("caddr_time_version", 31402))
	cfg.WalletPort = uint16(parseInt("wallet_port", 8333))
	cfg.ExplorerRequerySecs = parseInt("explorer_requery_seconds", cfg.ExplorerRequerySecs)
	cfg.BlockCount = int64(parseInt("block_count", 0))

	parseHex := func(key string) byte {
		v := strings.TrimPrefix(kv[key], "0x")
		v = strings.TrimPrefix(v, "0X")
		n, _ := strconv.ParseUint(v, 16, 8)
		return byte(n)
	}
	cfg.NetMagic[0] = parseHex("pchMessageStart_0")
	cfg.NetMagic[1] = parseHex("pchMessageStart_1")
	cfg.NetMagic[2] = parseHex("pchMessageStart_2")
	cfg.NetMagic[3] = parseHex("pchMessageStart_3")

	for i := 1; i <= 10; i++ {
		if v := kv[fmt.Sprintf("seed_%d", i)]; v != "" {
			cfg.Seeds = append(cfg.Seeds, v)
		}
	}

	return cfg, nil
}

// Discover finds all settings.conf files under /usr/local/sDNS.*/
func Discover() ([]*CoinConfig, error) {
	matches, err := filepath.Glob("/usr/local/sDNS.*/settings.conf")
	if err != nil {
		return nil, err
	}
	var configs []*CoinConfig
	for _, m := range matches {
		cfg, err := Load(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: skipping %s: %v\n", m, err)
			continue
		}
		if cfg.DNS.Host == "" {
			fmt.Fprintf(os.Stderr, "warn: skipping %s: host not set\n", m)
			continue
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// parseServicesBits parses a comma-separated list of service bit values,
// e.g. "1,3" or "0x1,0x3". Empty input yields an empty list.
func parseServicesBits(s string) []uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var bits []uint64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.ParseUint(part, 0, 64); err == nil {
			bits = append(bits, n)
		}
	}
	return bits
}

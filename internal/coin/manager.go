package coin

import (
	"context"
	"log"
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/Senasgr/go-multiseed/internal/addr"
	"github.com/Senasgr/go-multiseed/internal/cloudflare"
	"github.com/Senasgr/go-multiseed/internal/config"
	"github.com/Senasgr/go-multiseed/internal/crawler"
	"github.com/Senasgr/go-multiseed/internal/height"
	"github.com/Senasgr/go-multiseed/internal/p2p"
)

const (
	dumpInterval   = 5 * time.Minute
	saveInterval   = 10 * time.Minute
	statsInterval  = 60 * time.Second
	cfSyncInterval = 30 * time.Minute
	maxCFSeeds     = 25
)

// Manager orchestrates all components for a single blockchain.
type Manager struct {
	cfg     *config.CoinConfig
	db      *addr.AddrDB
	heightP *height.Poller
	dataDir string
}

// dataDir is where we store the DB and dump files for this coin: e.g. /usr/local/sDNS.<TICKER>/
func NewManager(cfg *config.CoinConfig, dataDir string) *Manager {
	db := addr.New(cfg.MinPeerProtoVersion, cfg.WalletPort, cfg.BlockCount)
	hp := height.New(
		cfg.ExplorerURL,
		cfg.SecondExplorerURL,
		cfg.ExplorerRequerySecs,
		cfg.BlockCount,
	)
	return &Manager{cfg: cfg, db: db, heightP: hp, dataDir: dataDir}
}

func (m *Manager) Run(ctx context.Context) {
	log.Printf("[%s] starting seeder: host=%s port=%d threads=%d",
		m.cfg.Ticker, m.cfg.DNS.Host, m.cfg.DNS.Port, m.cfg.CrawlerThreads)

	// Load persisted DB
	dbPath := filepath.Join(m.dataDir, "addrdb.json")
	if err := m.db.Load(dbPath); err != nil {
		log.Printf("[%s] load db: %v", m.cfg.Ticker, err)
	}

	// Bootstrap with seed nodes from config
	for _, seed := range m.cfg.Seeds {
		go func(s string, p uint16) {
			addrs := resolveHost(s, p)
			m.db.AddMany(addrs)
			if len(addrs) > 0 {
				log.Printf("[%s] bootstrapped %d addrs from %s", m.cfg.Ticker, len(addrs), s)
			}
		}(seed, m.cfg.WalletPort)
	}

	// Start height poller
	go m.heightP.Run(ctx)

	// Start crawler
	c := crawler.New(m.cfg.Ticker, m.db, crawler.Config{
		Workers: m.cfg.CrawlerThreads,
		PeerCfg: p2p.PeerConfig{
			NetMagic:            m.cfg.NetMagic,
			ProtocolVersion:     m.cfg.ProtocolVersion,
			InitProtoVersion:    m.cfg.InitProtoVersion,
			MinPeerProtoVersion: m.cfg.MinPeerProtoVersion,
			CAddrTimeVersion:    m.cfg.CAddrTimeVersion,
			WalletPort:          m.cfg.WalletPort,
			UserAgent:           "/go-multiseed:0.1/",
		},
		MinHeight: m.heightP.Current,
	})
	go c.Run(ctx)

	// Periodic tasks
	dumpTicker := time.NewTicker(dumpInterval)
	saveTicker := time.NewTicker(saveInterval)
	statsTicker := time.NewTicker(statsInterval)
	cfTicker := time.NewTicker(cfSyncInterval)
	defer dumpTicker.Stop()
	defer saveTicker.Stop()
	defer statsTicker.Stop()
	defer cfTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.db.Save(dbPath)
			return

		case <-dumpTicker.C:
			dumpPath := filepath.Join(m.dataDir, m.dumpFile())
			if err := m.db.WriteDump(dumpPath); err != nil {
				log.Printf("[%s] dump: %v", m.cfg.Ticker, err)
			}

		case <-saveTicker.C:
			if err := m.db.Save(dbPath); err != nil {
				log.Printf("[%s] save: %v", m.cfg.Ticker, err)
			}

		case <-statsTicker.C:
			s := m.db.Stats()
			log.Printf("[%s] stats: total=%d good=%d tracked=%d banned=%d height=%d",
				m.cfg.Ticker, s.Total, s.Good, s.Tracked, s.Banned, m.heightP.Current())

		case <-cfTicker.C:
			if m.cfg.CF.APIKey != "" {
				go m.syncCloudflare()
			}
		}
	}
}

func (m *Manager) syncCloudflare() {
	cf := cloudflare.New(m.cfg.CF.Username, m.cfg.CF.APIKey)
	allGood := append(m.db.GetGood(false, maxCFSeeds), m.db.GetGood(true, maxCFSeeds)...)
	if err := cf.Sync(m.cfg.CF.Domain, m.cfg.CF.DomainPrefix, allGood, maxCFSeeds); err != nil {
		log.Printf("[%s] cloudflare sync: %v", m.cfg.Ticker, err)
	} else {
		log.Printf("[%s] cloudflare sync: pushed %d IPs", m.cfg.Ticker, len(allGood))
	}
}

func (m *Manager) dumpFile() string {
	if m.cfg.CF.SeedDumpFile != "" {
		return m.cfg.CF.SeedDumpFile
	}
	return "dnsseed.dump"
}

// GoodAddrs implements dns.AddrSource.
func (m *Manager) GoodAddrs(ipv6 bool) []netip.Addr {
	return m.db.GetGood(ipv6, 50)
}

// Host implements dns.AddrSource.
func (m *Manager) Host() string { return m.cfg.DNS.Host }

// NS implements dns.AddrSource.
func (m *Manager) NS() string { return m.cfg.DNS.NS }

// Mbox implements dns.AddrSource.
func (m *Manager) Mbox() string { return m.cfg.DNS.Mbox }

// resolveHost resolves a hostname to AddrPorts using the system resolver.
func resolveHost(host string, port uint16) []netip.AddrPort {
	// Strip port if present
	if strings.Contains(host, ":") {
		h, _, err := splitHostPort(host)
		if err == nil {
			host = h
		}
	}
	addrs, err := netip.ParseAddr(host)
	if err == nil {
		return []netip.AddrPort{netip.AddrPortFrom(addrs, port)}
	}
	// Try DNS resolution via net package
	ips, err := lookupHost(host)
	if err != nil {
		return nil
	}
	result := make([]netip.AddrPort, 0, len(ips))
	for _, ip := range ips {
		result = append(result, netip.AddrPortFrom(ip, port))
	}
	return result
}

func splitHostPort(hostport string) (host string, portStr string, err error) {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return hostport, "", nil
	}
	return hostport[:i], hostport[i+1:], nil
}

func lookupHost(host string) ([]netip.Addr, error) {
	// Use net.DefaultResolver via net/netip
	// We import net only here to avoid polluting the package
	return doLookup(host)
}

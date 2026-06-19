package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/Senasgr/go-multiseed/internal/coin"
	"github.com/Senasgr/go-multiseed/internal/config"
	"github.com/Senasgr/go-multiseed/internal/dns"
)

const version = "0.1.0"

func main() {
	var (
		cfgDir  = flag.String("config-dir", "/usr/local", "base directory for sDNS.<TICKER>/settings.conf discovery")
		cfgFile = flag.String("config", "", "path to a single settings.conf (skips discovery)")
		ver     = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *ver {
		fmt.Printf("go-multiseed %s\n", version)
		os.Exit(0)
	}

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("go-multiseed %s starting", version)

	var configs []*config.CoinConfig
	var err error

	if *cfgFile != "" {
		cfg, err := config.Load(*cfgFile)
		if err != nil {
			log.Fatalf("load config %s: %v", *cfgFile, err)
		}
		configs = []*config.CoinConfig{cfg}
	} else {
		configs, err = discoverConfigs(*cfgDir)
		if err != nil {
			log.Fatalf("discover configs: %v", err)
		}
	}

	if len(configs) == 0 {
		log.Fatal("no coin configs found — create /usr/local/sDNS.<TICKER>/settings.conf")
	}

	log.Printf("loaded %d coin config(s)", len(configs))

	// DNS server — shared across all coins
	// Use port from first config (all coins should share one port)
	dnsPort := configs[0].DNS.Port
	dnsThreads := configs[0].DNS.Threads
	dnsServer := dns.New(dnsPort, dnsThreads)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	for _, cfg := range configs {
		dataDir := dataDirFor(*cfgDir, cfg.Ticker)
		mgr := coin.NewManager(cfg, dataDir)
		dnsServer.Register(mgr)

		wg.Add(1)
		go func(m *coin.Manager) {
			defer wg.Done()
			m.Run(ctx)
		}(mgr)

		log.Printf("started coin: %s (%s) -> %s", cfg.Ticker, cfg.BlockchainName, cfg.DNS.Host)
	}

	// Start DNS server in background
	go func() {
		if err := dnsServer.ListenAndServe(); err != nil {
			log.Printf("DNS server error: %v", err)
		}
	}()

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down...")
	cancel()
	wg.Wait()
	log.Printf("done")
}

func discoverConfigs(baseDir string) ([]*config.CoinConfig, error) {
	pattern := filepath.Join(baseDir, "sDNS.*/settings.conf")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var configs []*config.CoinConfig
	for _, m := range matches {
		cfg, err := config.Load(m)
		if err != nil {
			log.Printf("warn: skipping %s: %v", m, err)
			continue
		}
		if cfg.DNS.Host == "" {
			log.Printf("warn: skipping %s: host not set", m)
			continue
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

func dataDirFor(baseDir, ticker string) string {
	ticker = strings.ToUpper(ticker)
	return filepath.Join(baseDir, "sDNS."+ticker)
}

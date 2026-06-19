package crawler

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"sync"
	"time"

	"github.com/Senasgr/go-multiseed/internal/addr"
	"github.com/Senasgr/go-multiseed/internal/p2p"
)

type Config struct {
	Workers   int
	PeerCfg   p2p.PeerConfig
	MinHeight func() int64 // returns current required block height
}

type Crawler struct {
	cfg    Config
	db     *addr.AddrDB
	ticker string
}

func New(ticker string, db *addr.AddrDB, cfg Config) *Crawler {
	if cfg.Workers <= 0 {
		cfg.Workers = 96
	}
	return &Crawler{cfg: cfg, db: db, ticker: ticker}
}

func (c *Crawler) Run(ctx context.Context) {
	sem := make(chan struct{}, c.cfg.Workers)
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}

		candidates := c.db.NextToCrawl(c.cfg.Workers)
		if len(candidates) == 0 {
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		for _, ap := range candidates {
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case sem <- struct{}{}:
			}

			wg.Add(1)
			go func(ap netip.AddrPort) {
				defer wg.Done()
				defer func() { <-sem }()
				c.crawlOne(ap)
			}(ap)
		}
	}
}

func (c *Crawler) crawlOne(ap netip.AddrPort) {
	result, err := p2p.Crawl(ap, c.cfg.PeerCfg)
	if err != nil {
		c.db.Report(addr.CrawlResult{Addr: ap, Good: false})
		return
	}

	minH := int64(0)
	if c.cfg.MinHeight != nil {
		minH = c.cfg.MinHeight()
	}

	inSync := minH == 0 || int64(result.Height) >= minH

	c.db.Report(addr.CrawlResult{
		Addr:          ap,
		Good:          result.Good,
		ClientVersion: result.ClientVersion,
		SubVersion:    result.SubVersion,
		Height:        result.Height,
		Services:      result.Services,
		InSync:        inSync,
	})

	if result.Good && len(result.Addrs) > 0 {
		c.db.AddMany(result.Addrs)
		log.Printf("[%s] %s -> good v%d h%d +%d peers",
			c.ticker, ap, result.ClientVersion, result.Height, len(result.Addrs))
	} else {
		log.Printf("[%s] %s -> %s", c.ticker, ap, badReason(result, err))
	}
}

func badReason(r p2p.PeerResult, err error) string {
	if err != nil {
		return fmt.Sprintf("err: %v", err)
	}
	if !r.Good {
		return "no verack"
	}
	return "bad"
}

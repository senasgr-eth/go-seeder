package height

import (
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Poller fetches the current block height from an explorer HTTP API.
// The API must return the block height as a plain integer in the response body.
type Poller struct {
	primaryURL   string
	fallbackURL  string
	intervalSecs int
	current      atomic.Int64
	fallback     atomic.Int64 // static fallback from config
}

func New(primaryURL, fallbackURL string, intervalSecs int, staticFallback int64) *Poller {
	p := &Poller{
		primaryURL:   primaryURL,
		fallbackURL:  fallbackURL,
		intervalSecs: intervalSecs,
	}
	p.fallback.Store(staticFallback)
	p.current.Store(staticFallback)
	return p
}

func (p *Poller) Current() int64 {
	return p.current.Load()
}

func (p *Poller) Run(ctx context.Context) {
	if p.primaryURL == "" && p.fallbackURL == "" {
		return
	}
	ticker := time.NewTicker(time.Duration(p.intervalSecs) * time.Second)
	defer ticker.Stop()

	p.poll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

func (p *Poller) poll() {
	h, err := fetchHeight(p.primaryURL)
	if err != nil && p.fallbackURL != "" {
		h, err = fetchHeight(p.fallbackURL)
	}
	if err != nil {
		log.Printf("height poll failed: %v", err)
		return
	}
	if h > 0 {
		p.current.Store(h)
	}
}

func fetchHeight(url string) (int64, error) {
	if url == "" {
		return 0, nil
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return 0, err
	}
	h, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return 0, err
	}
	return h, nil
}

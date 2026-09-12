package addr

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/netip"
	"os"
	"sync"
	"time"
)

const (
	tau2H = float64(2 * 3600)
	tau8H = float64(8 * 3600)
	tau1D = float64(24 * 3600)
	tau1W = float64(7 * 24 * 3600)
	tau1M = float64(30 * 24 * 3600)

	minRetrySeconds = 60
)

type DecayStat struct {
	Weight      float64
	Count       float64
	Reliability float64
}

func (d *DecayStat) Update(good bool, age time.Duration, tau float64) {
	f := math.Exp(-age.Seconds() / tau)
	if good {
		d.Reliability = d.Reliability*f + (1.0 - f)
	} else {
		d.Reliability = d.Reliability * f
	}
	d.Count = d.Count*f + 1
	d.Weight = d.Weight*f + (1.0 - f)
}

type AddrInfo struct {
	mu            sync.Mutex
	Addr          netip.AddrPort
	Services      uint64
	LastTry       time.Time
	OurLastTry    time.Time
	OurLastGood   time.Time
	IgnoreTill    time.Time
	Stat2H        DecayStat
	Stat8H        DecayStat
	Stat1D        DecayStat
	Stat1W        DecayStat
	Stat1M        DecayStat
	ClientVersion int32
	Height        int
	Total         int
	Success       int
	SubVersion    string
	InSync        bool
	BanUntil      time.Time
}

func (a *AddrInfo) isGood(minProtoVersion int32, walletPort uint16, minHeight int64, requiredServices uint64) bool {
	if a.Addr.Port() != walletPort {
		return false
	}
	if requiredServices > 0 && a.Services&requiredServices != requiredServices {
		return false
	}
	ip := a.Addr.Addr()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return false
	}
	if minProtoVersion > 0 && a.ClientVersion > 0 && a.ClientVersion < minProtoVersion {
		return false
	}
// relaxed: allow IBD seed nodes (LBW seed serves full chain via whitelist)
if false && !a.InSync {
		return false
	}
	if a.Total <= 3 && a.Success*2 >= a.Total {
		return true
	}
	if a.Stat2H.Reliability > 0.85 && a.Stat2H.Count > 2 {
		return true
	}
	if a.Stat8H.Reliability > 0.70 && a.Stat8H.Count > 4 {
		return true
	}
	if a.Stat1D.Reliability > 0.55 && a.Stat1D.Count > 8 {
		return true
	}
	if a.Stat1W.Reliability > 0.45 && a.Stat1W.Count > 16 {
		return true
	}
	if a.Stat1M.Reliability > 0.35 && a.Stat1M.Count > 32 {
		return true
	}
	return false
}

func (a *AddrInfo) banTime() time.Duration {
	if a.Stat1M.Reliability-a.Stat1M.Weight+1.0 < 0.15 && a.Stat1M.Count > 32 {
		return 30 * 24 * time.Hour
	}
	if a.Stat1W.Reliability-a.Stat1W.Weight+1.0 < 0.10 && a.Stat1W.Count > 16 {
		return 7 * 24 * time.Hour
	}
	if a.Stat1D.Reliability-a.Stat1D.Weight+1.0 < 0.05 && a.Stat1D.Count > 8 {
		return 24 * time.Hour
	}
	return 0
}

func (a *AddrInfo) ignoreTime() time.Duration {
	if a.Stat1M.Reliability-a.Stat1M.Weight+1.0 < 0.20 && a.Stat1M.Count > 2 {
		return 10 * 24 * time.Hour
	}
	if a.Stat1W.Reliability-a.Stat1W.Weight+1.0 < 0.16 && a.Stat1W.Count > 2 {
		return 3 * 24 * time.Hour
	}
	if a.Stat1D.Reliability-a.Stat1D.Weight+1.0 < 0.12 && a.Stat1D.Count > 2 {
		return 8 * time.Hour
	}
	if a.Stat8H.Reliability-a.Stat8H.Weight+1.0 < 0.08 && a.Stat8H.Count > 2 {
		return 2 * time.Hour
	}
	return 0
}

type CrawlResult struct {
	Addr          netip.AddrPort
	Good          bool
	ClientVersion int32
	SubVersion    string
	Height        int
	Services      uint64
	InSync        bool
}

type Stats struct {
	Total   int
	Good    int
	Tracked int
	Banned  int
}

type AddrDB struct {
	mu              sync.RWMutex
	nodes           map[netip.AddrPort]*AddrInfo
	minProtoVersion int32
	walletPort      uint16
	minHeight       int64
	requiredServices uint64
}

func New(minProtoVersion int32, walletPort uint16, minHeight int64, requiredServices uint64) *AddrDB {
	return &AddrDB{
		nodes:           make(map[netip.AddrPort]*AddrInfo),
		minProtoVersion: minProtoVersion,
		walletPort:      walletPort,
		minHeight:       minHeight,
		requiredServices: requiredServices,
	}
}

func (db *AddrDB) UpdateMinHeight(h int64) {
	db.mu.Lock()
	db.minHeight = h
	db.mu.Unlock()
}

func (db *AddrDB) Add(ap netip.AddrPort) {
	if !ap.IsValid() {
		return
	}
	ip := ap.Addr().Unmap()
	ap = netip.AddrPortFrom(ip, db.walletPort)

	db.mu.Lock()
	defer db.mu.Unlock()
	if _, exists := db.nodes[ap]; !exists {
		db.nodes[ap] = &AddrInfo{Addr: ap}
	}
}

func (db *AddrDB) AddMany(addrs []netip.AddrPort) {
	for _, a := range addrs {
		db.Add(a)
	}
}

// NextToCrawl returns up to n addresses that are ready to be crawled.
func (db *AddrDB) NextToCrawl(n int) []netip.AddrPort {
	now := time.Now()
	db.mu.RLock()
	candidates := make([]*AddrInfo, 0, len(db.nodes))
	for _, info := range db.nodes {
		info.mu.Lock()
		banned := now.Before(info.BanUntil)
		ignored := now.Before(info.IgnoreTill)
		tooSoon := now.Sub(info.OurLastTry) < minRetrySeconds*time.Second
		info.mu.Unlock()
		if !banned && !ignored && !tooSoon {
			candidates = append(candidates, info)
		}
	}
	db.mu.RUnlock()

	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })

	result := make([]netip.AddrPort, 0, n)
	for _, info := range candidates {
		if len(result) >= n {
			break
		}
		result = append(result, info.Addr)
	}
	return result
}

// Report processes the result of a crawl attempt.
func (db *AddrDB) Report(r CrawlResult) {
	db.mu.RLock()
	info, ok := db.nodes[r.Addr]
	db.mu.RUnlock()
	if !ok {
		return
	}

	now := time.Now()
	info.mu.Lock()
	defer info.mu.Unlock()

	age := now.Sub(info.OurLastTry)
	if age < 0 {
		age = 0
	}
	info.OurLastTry = now
	info.Total++

	if r.Good {
		info.ClientVersion = r.ClientVersion
		info.SubVersion = r.SubVersion
		info.Height = r.Height
		info.Services = r.Services
		info.InSync = r.InSync
		info.OurLastGood = now
		info.Success++

		info.Stat2H.Update(true, age, tau2H)
		info.Stat8H.Update(true, age, tau8H)
		info.Stat1D.Update(true, age, tau1D)
		info.Stat1W.Update(true, age, tau1W)
		info.Stat1M.Update(true, age, tau1M)
	} else {
		info.Stat2H.Update(false, age, tau2H)
		info.Stat8H.Update(false, age, tau8H)
		info.Stat1D.Update(false, age, tau1D)
		info.Stat1W.Update(false, age, tau1W)
		info.Stat1M.Update(false, age, tau1M)

		if bt := info.banTime(); bt > 0 {
			info.BanUntil = now.Add(bt)
		} else if it := info.ignoreTime(); it > 0 {
			info.IgnoreTill = now.Add(it)
		}
	}
}

// GetGood returns good node IPs for DNS responses.
func (db *AddrDB) GetGood(ipv6 bool, max int) []netip.Addr {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var result []netip.Addr
	for _, info := range db.nodes {
		info.mu.Lock()
		good := info.isGood(db.minProtoVersion, db.walletPort, db.minHeight, db.requiredServices)
		addr := info.Addr.Addr()
		info.mu.Unlock()

		if !good {
			continue
		}
		is6 := addr.Is6()
		if ipv6 && !is6 {
			continue
		}
		if !ipv6 && is6 {
			continue
		}
		result = append(result, addr)
		if len(result) >= max {
			break
		}
	}
	rand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
	if len(result) > max {
		result = result[:max]
	}
	return result
}

// GetGoodWithServices returns good node IPs that advertise at least the
// required service bits (used for x%x.-prefixed DNS seed names).
func (db *AddrDB) GetGoodWithServices(ipv6 bool, max int, required uint64) []netip.Addr {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var result []netip.Addr
	for _, info := range db.nodes {
		info.mu.Lock()
		good := info.isGood(db.minProtoVersion, db.walletPort, db.minHeight, db.requiredServices)
		services := info.Services
		addr := info.Addr.Addr()
		info.mu.Unlock()

		if !good {
			continue
		}
		if services&required != required {
			continue
		}
		is6 := addr.Is6()
		if ipv6 && !is6 {
			continue
		}
		if !ipv6 && is6 {
			continue
		}
		result = append(result, addr)
		if len(result) >= max {
			break
		}
	}
	rand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
	if len(result) > max {
		result = result[:max]
	}
	return result
}

func (db *AddrDB) Stats() Stats {
	db.mu.RLock()
	defer db.mu.RUnlock()

	now := time.Now()
	s := Stats{Total: len(db.nodes)}
	for _, info := range db.nodes {
		info.mu.Lock()
		if now.Before(info.BanUntil) {
			s.Banned++
		} else if info.isGood(db.minProtoVersion, db.walletPort, db.minHeight, db.requiredServices) {
			s.Good++
		}
		if !info.OurLastTry.IsZero() {
			s.Tracked++
		}
		info.mu.Unlock()
	}
	return s
}

// WriteDump writes the dnsseed.dump file consumed by cf-uploader/seeder.py.
// Format per line: "<ip:port> <1|0>"
func (db *AddrDB) WriteDump(path string) error {
	db.mu.RLock()
	defer db.mu.RUnlock()

	f, err := os.CreateTemp("", "dnsseed-*.tmp")
	if err != nil {
		return err
	}
	tmpName := f.Name()

	for _, info := range db.nodes {
		info.mu.Lock()
		good := info.isGood(db.minProtoVersion, db.walletPort, db.minHeight, db.requiredServices)
		ap := info.Addr
		info.mu.Unlock()

		flag := 0
		if good {
			flag = 1
		}
		if ap.Addr().Is6() {
			fmt.Fprintf(f, "[%s]:%d %d\n", ap.Addr(), ap.Port(), flag)
		} else {
			fmt.Fprintf(f, "%s:%d %d\n", ap.Addr(), ap.Port(), flag)
		}
	}
	f.Close()
	return os.Rename(tmpName, path)
}

// persistEntry is the JSON-serializable form of AddrInfo.
type persistEntry struct {
	Addr          string    `json:"addr"`
	Services      uint64    `json:"services"`
	LastTry       time.Time `json:"last_try,omitempty"`
	OurLastTry    time.Time `json:"our_last_try,omitempty"`
	OurLastGood   time.Time `json:"our_last_good,omitempty"`
	IgnoreTill    time.Time `json:"ignore_till,omitempty"`
	BanUntil      time.Time `json:"ban_until,omitempty"`
	Stat2H        DecayStat `json:"stat_2h"`
	Stat8H        DecayStat `json:"stat_8h"`
	Stat1D        DecayStat `json:"stat_1d"`
	Stat1W        DecayStat `json:"stat_1w"`
	Stat1M        DecayStat `json:"stat_1m"`
	ClientVersion int32     `json:"client_version"`
	Height        int       `json:"height"`
	Total         int       `json:"total"`
	Success       int       `json:"success"`
	SubVersion    string    `json:"sub_version"`
	InSync        bool      `json:"in_sync"`
}

func (db *AddrDB) Save(path string) error {
	db.mu.RLock()
	entries := make([]persistEntry, 0, len(db.nodes))
	for _, info := range db.nodes {
		info.mu.Lock()
		e := persistEntry{
			Addr:          info.Addr.String(),
			Services:      info.Services,
			LastTry:       info.LastTry,
			OurLastTry:    info.OurLastTry,
			OurLastGood:   info.OurLastGood,
			IgnoreTill:    info.IgnoreTill,
			BanUntil:      info.BanUntil,
			Stat2H:        info.Stat2H,
			Stat8H:        info.Stat8H,
			Stat1D:        info.Stat1D,
			Stat1W:        info.Stat1W,
			Stat1M:        info.Stat1M,
			ClientVersion: info.ClientVersion,
			Height:        info.Height,
			Total:         info.Total,
			Success:       info.Success,
			SubVersion:    info.SubVersion,
			InSync:        info.InSync,
		}
		info.mu.Unlock()
		entries = append(entries, e)
	}
	db.mu.RUnlock()

	f, err := os.CreateTemp("", "addrdb-*.tmp")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(entries); err != nil {
		f.Close()
		os.Remove(tmpName)
		return err
	}
	f.Close()
	return os.Rename(tmpName, path)
}

func (db *AddrDB) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	var entries []persistEntry
	if err := json.NewDecoder(f).Decode(&entries); err != nil {
		return err
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	for _, e := range entries {
		ap, err := netip.ParseAddrPort(e.Addr)
		if err != nil {
			continue
		}
		info := &AddrInfo{
			Addr:          ap,
			Services:      e.Services,
			LastTry:       e.LastTry,
			OurLastTry:    e.OurLastTry,
			OurLastGood:   e.OurLastGood,
			IgnoreTill:    e.IgnoreTill,
			BanUntil:      e.BanUntil,
			Stat2H:        e.Stat2H,
			Stat8H:        e.Stat8H,
			Stat1D:        e.Stat1D,
			Stat1W:        e.Stat1W,
			Stat1M:        e.Stat1M,
			ClientVersion: e.ClientVersion,
			Height:        e.Height,
			Total:         e.Total,
			Success:       e.Success,
			SubVersion:    e.SubVersion,
			InSync:        e.InSync,
		}
		db.nodes[ap] = info
	}
	return nil
}

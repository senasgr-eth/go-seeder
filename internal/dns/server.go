package dns

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const (
	maxIPsPerResponse = 25
	ttl               = 30
)

// AddrSource is implemented by coin.Manager to provide good IPs for DNS responses.
type AddrSource interface {
	GoodAddrs(ipv6 bool) []netip.Addr
	Host() string
	NS() string
	Mbox() string
}

// Server is a single UDP DNS server that routes queries to the correct coin.
type Server struct {
	port    int
	coins   map[string]AddrSource // keyed by lowercased FQDN
	threads int
}

func New(port, threads int) *Server {
	return &Server{
		port:    port,
		coins:   make(map[string]AddrSource),
		threads: threads,
	}
}

func (s *Server) Register(src AddrSource) {
	host := strings.ToLower(strings.TrimRight(src.Host(), ".")) + "."
	s.coins[host] = src
}

func (s *Server) ListenAndServe() error {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleQuery)

	srv := &dns.Server{
		Addr:    net.JoinHostPort("", fmt.Sprintf("%d", s.port)),
		Net:     "udp",
		Handler: mux,
	}
	log.Printf("DNS server listening on UDP port %d", s.port)
	return srv.ListenAndServe()
}

func (s *Server) handleQuery(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = false

	if len(r.Question) == 0 {
		w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	qname := strings.ToLower(q.Name)

	src, ok := s.coins[qname]
	if !ok {
		m.SetRcode(r, dns.RcodeNameError)
		w.WriteMsg(m)
		return
	}

	switch q.Qtype {
	case dns.TypeA:
		addrs := src.GoodAddrs(false)
		limit(addrs, maxIPsPerResponse)
		for _, ip := range addrs {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
				A:   ip.AsSlice(),
			})
		}

	case dns.TypeAAAA:
		addrs := src.GoodAddrs(true)
		limit(addrs, maxIPsPerResponse)
		for _, ip := range addrs {
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
				AAAA: ip.AsSlice(),
			})
		}

	case dns.TypeNS:
		ns := src.NS()
		if ns != "" {
			m.Answer = append(m.Answer, &dns.NS{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: ttl},
				Ns:  fqdn(ns),
			})
		}

	case dns.TypeSOA:
		ns := src.NS()
		mbox := src.Mbox()
		if ns != "" {
			m.Answer = append(m.Answer, &dns.SOA{
				Hdr:     dns.RR_Header{Name: q.Name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: ttl},
				Ns:      fqdn(ns),
				Mbox:    soaMbox(mbox),
				Serial:  uint32(time.Now().Unix()),
				Refresh: 604800,
				Retry:   86400,
				Expire:  2592000,
				Minttl:  604800,
			})
		}

	default:
		m.SetRcode(r, dns.RcodeNotImplemented)
	}

	w.WriteMsg(m)
}

func fqdn(s string) string {
	if !strings.HasSuffix(s, ".") {
		return s + "."
	}
	return s
}

func soaMbox(mbox string) string {
	// Convert admin@example.com -> admin.example.com.
	mbox = strings.Replace(mbox, "@", ".", 1)
	return fqdn(mbox)
}

func limit(addrs []netip.Addr, max int) []netip.Addr {
	if len(addrs) > max {
		return addrs[:max]
	}
	return addrs
}

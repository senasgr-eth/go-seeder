package p2p

import (
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 15 * time.Second
	crawlTimeout     = 30 * time.Second
)

type PeerConfig struct {
	NetMagic            [4]byte
	ProtocolVersion     int32
	InitProtoVersion    int32
	MinPeerProtoVersion int32
	CAddrTimeVersion    int32
	WalletPort          uint16
	UserAgent           string
}

type PeerResult struct {
	Good          bool
	ClientVersion int32
	SubVersion    string
	Height        int
	Services      uint64
	Addrs         []netip.AddrPort
}

// Crawl connects to addr, performs the version handshake, sends getaddr, and
// collects addr messages. Returns the crawl result.
func Crawl(ap netip.AddrPort, cfg PeerConfig) (PeerResult, error) {
	conn, err := net.DialTimeout("tcp", ap.String(), dialTimeout)
	if err != nil {
		return PeerResult{}, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(crawlTimeout))

	// Send version
	versionPayload := MsgVersion(
		cfg.ProtocolVersion,
		cfg.InitProtoVersion,
		NodeNetwork,
		ap,
		cfg.UserAgent,
		0,
	)
	if err := writeMsg(conn, cfg.NetMagic, "version", versionPayload); err != nil {
		return PeerResult{}, fmt.Errorf("send version: %w", err)
	}

	var result PeerResult
	gotVersion := false
	gotVerack := false
	sentGetAddr := false

	for {
		conn.SetDeadline(time.Now().Add(handshakeTimeout))
		cmd, payload, err := readMsg(conn, cfg.NetMagic)
		if err != nil {
			if gotVersion && result.ClientVersion < cfg.MinPeerProtoVersion {
				return result, nil
			}
			break
		}

		switch cmd {
		case "version":
			ver, svc, height, ua, err := ParseVersionPayload(payload)
			if err != nil {
				return result, fmt.Errorf("parse version: %w", err)
			}
			result.ClientVersion = ver
			result.Services = svc | NodeNetwork
			result.Height = int(height)
			result.SubVersion = ua
			gotVersion = true

			// Send verack
			if err := writeMsg(conn, cfg.NetMagic, "verack", nil); err != nil {
				return result, fmt.Errorf("send verack: %w", err)
			}

		case "verack":
			gotVerack = true
			if gotVersion && !sentGetAddr {
				writeMsg(conn, cfg.NetMagic, "getaddr", nil)
				sentGetAddr = true
			}

		case "addr":
			withTimestamp := result.ClientVersion >= cfg.CAddrTimeVersion
			addrs, err := ParseAddrMsg(payload, withTimestamp)
			if err == nil {
				result.Addrs = append(result.Addrs, addrs...)
			}
			// After collecting addr, we have enough info
			if gotVersion && gotVerack {
				result.Good = true
				return result, nil
			}
		}
	}

	if gotVersion && gotVerack {
		result.Good = true
	}
	return result, nil
}

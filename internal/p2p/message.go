package p2p

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net/netip"
	"time"
)

const (
	NodeNetwork = uint64(1)
	MaxPayload  = 32 * 1024 * 1024
)

// header is the 24-byte Bitcoin message header.
type header struct {
	Magic    [4]byte
	Command  [12]byte
	Length   uint32
	Checksum [4]byte
}

func checksum(payload []byte) [4]byte {
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	var c [4]byte
	copy(c[:], h2[:4])
	return c
}

func writeMsg(w io.Writer, magic [4]byte, command string, payload []byte) error {
	var h header
	copy(h.Magic[:], magic[:])
	copy(h.Command[:], command)
	h.Length = uint32(len(payload))
	h.Checksum = checksum(payload)

	if err := binary.Write(w, binary.LittleEndian, h); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readMsg(r io.Reader, magic [4]byte) (command string, payload []byte, err error) {
	var h header
	if err = binary.Read(r, binary.LittleEndian, &h); err != nil {
		return
	}
	if h.Magic != magic {
		err = fmt.Errorf("magic mismatch: got %x want %x", h.Magic, magic)
		return
	}
	if h.Length > MaxPayload {
		err = fmt.Errorf("payload too large: %d", h.Length)
		return
	}
	payload = make([]byte, h.Length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	got := checksum(payload)
	if got != h.Checksum {
		err = fmt.Errorf("checksum mismatch")
		return
	}
	command = string(bytes.TrimRight(h.Command[:], "\x00"))
	return
}

// varint encoding / decoding (Bitcoin wire format)
func writeVarint(w *bytes.Buffer, n uint64) {
	switch {
	case n < 0xfd:
		w.WriteByte(byte(n))
	case n <= 0xffff:
		w.WriteByte(0xfd)
		b := [2]byte{}
		binary.LittleEndian.PutUint16(b[:], uint16(n))
		w.Write(b[:])
	case n <= 0xffffffff:
		w.WriteByte(0xfe)
		b := [4]byte{}
		binary.LittleEndian.PutUint32(b[:], uint32(n))
		w.Write(b[:])
	default:
		w.WriteByte(0xff)
		b := [8]byte{}
		binary.LittleEndian.PutUint64(b[:], n)
		w.Write(b[:])
	}
}

func readVarint(r *bytes.Reader) (uint64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	switch b {
	case 0xfd:
		var n uint16
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return 0, err
		}
		return uint64(n), nil
	case 0xfe:
		var n uint32
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return 0, err
		}
		return uint64(n), nil
	case 0xff:
		var n uint64
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return 0, err
		}
		return n, nil
	default:
		return uint64(b), nil
	}
}

func writeVarStr(w *bytes.Buffer, s string) {
	writeVarint(w, uint64(len(s)))
	w.WriteString(s)
}

// encodeNetAddr encodes a network address in Bitcoin format (26 bytes).
// services(8) + ip(16, IPv4-mapped if v4) + port(2, big endian)
func encodeNetAddr(w *bytes.Buffer, services uint64, ap netip.AddrPort) {
	binary.Write(w, binary.LittleEndian, services)
	ip := ap.Addr().Unmap()
	var ipBytes [16]byte
	if ip.Is4() {
		v4 := ip.As4()
		copy(ipBytes[10:12], []byte{0xff, 0xff})
		copy(ipBytes[12:], v4[:])
	} else {
		v6 := ip.As16()
		copy(ipBytes[:], v6[:])
	}
	w.Write(ipBytes[:])
	binary.Write(w, binary.BigEndian, ap.Port())
}

// MsgVersion builds a version message payload.
func MsgVersion(protoVersion, initProtoVersion int32, services uint64, toAddr netip.AddrPort, userAgent string, startHeight int32) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, protoVersion)
	binary.Write(&buf, binary.LittleEndian, services)
	binary.Write(&buf, binary.LittleEndian, time.Now().Unix())
	encodeNetAddr(&buf, NodeNetwork, toAddr)
	encodeNetAddr(&buf, services, netip.AddrPortFrom(netip.IPv4Unspecified(), 0))
	nonce := rand.Uint64()
	binary.Write(&buf, binary.LittleEndian, nonce)
	writeVarStr(&buf, userAgent)
	binary.Write(&buf, binary.LittleEndian, startHeight)
	return buf.Bytes()
}

// ParseAddrMsg parses a Bitcoin addr message payload.
// withTimestamp should be true if peer's protocol version >= caddr_time_version (31402).
func ParseAddrMsg(payload []byte, withTimestamp bool) ([]netip.AddrPort, error) {
	r := bytes.NewReader(payload)
	count, err := readVarint(r)
	if err != nil {
		return nil, err
	}
	if count > 1000 {
		count = 1000
	}

	addrs := make([]netip.AddrPort, 0, count)
	for i := uint64(0); i < count; i++ {
		if withTimestamp {
			var ts uint32
			if err := binary.Read(r, binary.LittleEndian, &ts); err != nil {
				break
			}
		}
		var services uint64
		if err := binary.Read(r, binary.LittleEndian, &services); err != nil {
			break
		}
		var ipBytes [16]byte
		if _, err := io.ReadFull(r, ipBytes[:]); err != nil {
			break
		}
		var port uint16
		if err := binary.Read(r, binary.BigEndian, &port); err != nil {
			break
		}

		// Determine if it's IPv4-mapped
		v4mapped := true
		for i := 0; i < 10; i++ {
			if ipBytes[i] != 0 {
				v4mapped = false
				break
			}
		}
		if v4mapped && ipBytes[10] == 0xff && ipBytes[11] == 0xff {
			v4mapped = true
		} else {
			v4mapped = false
		}

		var ip netip.Addr
		if v4mapped {
			ip = netip.AddrFrom4([4]byte{ipBytes[12], ipBytes[13], ipBytes[14], ipBytes[15]})
		} else {
			var v6 [16]byte
			copy(v6[:], ipBytes[:])
			ip = netip.AddrFrom16(v6)
		}
		addrs = append(addrs, netip.AddrPortFrom(ip, port))
	}
	return addrs, nil
}

// ParseVersionPayload extracts version and start height from a version message.
func ParseVersionPayload(payload []byte) (version int32, startHeight int32, userAgent string, err error) {
	r := bytes.NewReader(payload)
	if err = binary.Read(r, binary.LittleEndian, &version); err != nil {
		return
	}
	var services uint64
	if err = binary.Read(r, binary.LittleEndian, &services); err != nil {
		return
	}
	var ts int64
	if err = binary.Read(r, binary.LittleEndian, &ts); err != nil {
		return
	}
	// addr_recv (26 bytes) + addr_from (26 bytes)
	skip := make([]byte, 52)
	if _, err = io.ReadFull(r, skip); err != nil {
		return
	}
	var nonce uint64
	if err = binary.Read(r, binary.LittleEndian, &nonce); err != nil {
		return
	}
	uaLen, err2 := readVarint(r)
	if err2 != nil {
		err = err2
		return
	}
	if uaLen > 256 {
		uaLen = 256
	}
	uaBytes := make([]byte, uaLen)
	if _, err = io.ReadFull(r, uaBytes); err != nil {
		return
	}
	userAgent = string(uaBytes)
	binary.Read(r, binary.LittleEndian, &startHeight)
	return
}

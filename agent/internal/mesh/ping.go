package mesh

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"time"
)

// Ping sends one ICMP echo request from a mesh address to a mesh address, and gives the
// round-trip time. Only root can open the socket. The request also starts the handshake of
// a tunnel that has none.
func Ping(ctx context.Context, from, to netip.Addr, timeout time.Duration) (time.Duration, error) {
	if !from.Is4() || !to.Is4() {
		return 0, errors.New("mesh: a mesh address is an IPv4 address")
	}
	conn, err := net.ListenPacket("ip4:icmp", from.String())
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	// Type 8 (echo request), code 0, checksum, identifier, sequence number, and 8 bytes of
	// data. The identifier and the data are random, so that an answer for a different
	// request is never this answer.
	msg := make([]byte, 16)
	msg[0] = 8
	if _, err := rand.Read(msg[4:6]); err != nil {
		return 0, err
	}
	msg[7] = 1
	if _, err := rand.Read(msg[8:]); err != nil {
		return 0, err
	}
	binary.BigEndian.PutUint16(msg[2:4], checksum(msg))

	start := time.Now()
	if err := conn.SetDeadline(start.Add(timeout)); err != nil {
		return 0, err
	}
	if _, err := conn.WriteTo(msg, &net.IPAddr{IP: to.AsSlice()}); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		// The socket gets each ICMP message for this server. The read gives the message
		// with no IP header.
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return 0, err
		}
		ip, ok := peer.(*net.IPAddr)
		if !ok || !ip.IP.Equal(to.AsSlice()) || n != len(msg) || buf[0] != 0 {
			continue
		}
		if string(buf[4:n]) == string(msg[4:]) {
			return time.Since(start), nil
		}
	}
}

// checksum gives the internet checksum of an ICMP message whose checksum field is 0.
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

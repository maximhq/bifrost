package sni

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// TestExtract_RealClientHello starts a TLS client to capture a real ClientHello
// and verifies that Extract correctly parses the SNI hostname from it.
func TestExtract_RealClientHello(t *testing.T) {
	const sniHost = "api.openai.com"

	// Create a listener that captures the first bytes from the client
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	captured := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			captured <- nil
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		captured <- buf[:n]
	}()

	// Dial and start a TLS handshake with the expected SNI
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         sniHost,
		InsecureSkipVerify: true,
	})
	// Start handshake (will fail since server isn't TLS, but the ClientHello is sent)
	go tlsConn.Handshake()
	defer tlsConn.Close()

	buf := <-captured
	if buf == nil || len(buf) == 0 {
		t.Fatal("no data captured from client")
	}

	hostname, err := Extract(buf)
	if err != nil {
		t.Fatalf("Extract failed: %v (buf len=%d, first bytes=%x)", err, len(buf), buf[:min(10, len(buf))])
	}
	if hostname != sniHost {
		t.Errorf("got hostname %q, want %q", hostname, sniHost)
	}
}

// TestExtract_Errors tests various error cases.
func TestExtract_Errors(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		err  error
	}{
		{"empty", []byte{}, ErrTruncated},
		{"too short", []byte{0x16, 0x03, 0x01}, ErrTruncated},
		{"not TLS", []byte{0x17, 0x03, 0x01, 0x00, 0x05, 0, 0, 0, 0, 0}, ErrNotTLS},
		{"not ClientHello", []byte{
			0x16, 0x03, 0x01, 0x00, 0x05, // TLS record header (handshake, 5 bytes)
			0x02, 0x00, 0x00, 0x01, 0x00, // ServerHello msg type (0x02)
		}, ErrNotClientHello},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Extract(tt.buf)
			if err != tt.err {
				t.Errorf("got error %v, want %v", err, tt.err)
			}
		})
	}
}

// TestExtract_ManualClientHello tests with a manually constructed minimal ClientHello.
func TestExtract_ManualClientHello(t *testing.T) {
	const hostname = "test.example.com"
	hello := buildMinimalClientHello(hostname)

	got, err := Extract(hello)
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if got != hostname {
		t.Errorf("got %q, want %q", got, hostname)
	}
}

// buildMinimalClientHello constructs a minimal TLS ClientHello with the given SNI.
func buildMinimalClientHello(hostname string) []byte {
	// SNI extension
	sniData := []byte{sniTypeHostname}
	sniData = append(sniData, byte(len(hostname)>>8), byte(len(hostname)))
	sniData = append(sniData, []byte(hostname)...)

	sniList := []byte{byte(len(sniData) >> 8), byte(len(sniData))}
	sniList = append(sniList, sniData...)

	sniExt := []byte{0x00, 0x00} // extension type = SNI
	sniExt = append(sniExt, byte(len(sniList)>>8), byte(len(sniList)))
	sniExt = append(sniExt, sniList...)

	extensions := []byte{byte(len(sniExt) >> 8), byte(len(sniExt))}
	extensions = append(extensions, sniExt...)

	// ClientHello body: version(2) + random(32) + session_id_len(1) + cipher_suites_len(2) + one_suite(2) + comp_len(1) + comp(1) + extensions
	body := []byte{0x03, 0x03} // TLS 1.2
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)       // session ID length = 0
	body = append(body, 0x00, 0x02) // cipher suites length = 2
	body = append(body, 0x00, 0xff) // one cipher suite
	body = append(body, 0x01, 0x00) // compression methods length = 1, null
	body = append(body, extensions...)

	// Handshake header: type(1) + length(3)
	handshake := []byte{handshakeClientHello}
	handshake = append(handshake, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	handshake = append(handshake, body...)

	// TLS record header: type(1) + version(2) + length(2)
	record := []byte{recordTypeHandshake, 0x03, 0x01}
	record = append(record, byte(len(handshake)>>8), byte(len(handshake)))
	record = append(record, handshake...)

	return record
}

// Package sni extracts the Server Name Indication (SNI) hostname from a TLS
// ClientHello message. It operates on raw bytes without using crypto/tls,
// making it suitable for peeking at the first bytes of a connection before
// deciding how to handle it.
//
// Reference: RFC 5246 (TLS 1.2), RFC 8446 (TLS 1.3), RFC 6066 Section 3 (SNI).
package sni

import "errors"

var (
	// ErrNotTLS is returned when the data does not start with a TLS handshake record.
	ErrNotTLS = errors.New("not a TLS handshake record")

	// ErrTruncated is returned when the data is too short to contain a complete ClientHello.
	ErrTruncated = errors.New("truncated TLS record")

	// ErrNotClientHello is returned when the handshake message is not a ClientHello.
	ErrNotClientHello = errors.New("not a ClientHello message")

	// ErrNoSNI is returned when the ClientHello does not contain an SNI extension.
	ErrNoSNI = errors.New("no SNI extension found")
)

const (
	recordTypeHandshake  = 0x16
	handshakeClientHello = 0x01
	extensionSNI         = 0x0000
	sniTypeHostname      = 0x00
)

// Extract parses a TLS ClientHello from buf and returns the SNI hostname.
// buf must contain at least the complete ClientHello message (typically the
// first 1-2 KB of a TLS connection).
func Extract(buf []byte) (string, error) {
	// TLS record header: content_type(1) + version(2) + length(2) = 5 bytes
	if len(buf) < 5 {
		return "", ErrTruncated
	}
	if buf[0] != recordTypeHandshake {
		return "", ErrNotTLS
	}

	recordLen := int(buf[3])<<8 | int(buf[4])
	data := buf[5:]
	if len(data) < recordLen {
		return "", ErrTruncated
	}
	data = data[:recordLen]

	// Handshake header: msg_type(1) + length(3)
	if len(data) < 4 {
		return "", ErrTruncated
	}
	if data[0] != handshakeClientHello {
		return "", ErrNotClientHello
	}
	helloLen := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	data = data[4:]
	if len(data) < helloLen {
		return "", ErrTruncated
	}
	data = data[:helloLen]

	// ClientHello body:
	//   client_version(2) + random(32) = 34 bytes
	if len(data) < 34 {
		return "", ErrTruncated
	}
	data = data[34:]

	// session_id: length(1) + data
	if len(data) < 1 {
		return "", ErrTruncated
	}
	sessionIDLen := int(data[0])
	data = data[1:]
	if len(data) < sessionIDLen {
		return "", ErrTruncated
	}
	data = data[sessionIDLen:]

	// cipher_suites: length(2) + data
	if len(data) < 2 {
		return "", ErrTruncated
	}
	cipherSuitesLen := int(data[0])<<8 | int(data[1])
	data = data[2:]
	if len(data) < cipherSuitesLen {
		return "", ErrTruncated
	}
	data = data[cipherSuitesLen:]

	// compression_methods: length(1) + data
	if len(data) < 1 {
		return "", ErrTruncated
	}
	compMethodsLen := int(data[0])
	data = data[1:]
	if len(data) < compMethodsLen {
		return "", ErrTruncated
	}
	data = data[compMethodsLen:]

	// Extensions: length(2) + extension data
	if len(data) < 2 {
		return "", ErrNoSNI
	}
	extensionsLen := int(data[0])<<8 | int(data[1])
	data = data[2:]
	if len(data) < extensionsLen {
		return "", ErrTruncated
	}
	data = data[:extensionsLen]

	// Walk extensions looking for SNI (type 0x0000)
	for len(data) >= 4 {
		extType := int(data[0])<<8 | int(data[1])
		extLen := int(data[2])<<8 | int(data[3])
		data = data[4:]
		if len(data) < extLen {
			return "", ErrTruncated
		}

		if extType == extensionSNI {
			return parseSNIExtension(data[:extLen])
		}

		data = data[extLen:]
	}

	return "", ErrNoSNI
}

// parseSNIExtension extracts the hostname from the SNI extension data.
// Format: server_name_list_length(2) + [name_type(1) + name_length(2) + name]...
func parseSNIExtension(data []byte) (string, error) {
	if len(data) < 2 {
		return "", ErrTruncated
	}
	listLen := int(data[0])<<8 | int(data[1])
	data = data[2:]
	if len(data) < listLen {
		return "", ErrTruncated
	}
	data = data[:listLen]

	for len(data) >= 3 {
		nameType := data[0]
		nameLen := int(data[1])<<8 | int(data[2])
		data = data[3:]
		if len(data) < nameLen {
			return "", ErrTruncated
		}

		if nameType == sniTypeHostname {
			return string(data[:nameLen]), nil
		}

		data = data[nameLen:]
	}

	return "", ErrNoSNI
}

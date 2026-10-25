package chaosmock

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"log"
	"math/big"
	"net"
	"net/http"
	"time"
)

// Bedrock is the one LLM format a client cannot reach at /s/<name>/llm: its
// clients take a bare host for each Bedrock endpoint and always use https. So
// besides the bedrock prefix on the LLM surface, the fleet can open a second,
// TLS listener that answers every path as one server's Bedrock surface. Its
// streams are the AWS event stream, binary frames with CRC32 checksums, encoded
// here by hand so the module needs no AWS dependency to serve them.

// ServeTLS starts a listener on addr that speaks HTTPS with a self-signed
// certificate made here, and answers every path as server's Bedrock surface:
// /model/<m>/converse is /s/<server>/llm/bedrock/model/<m>/converse. The
// server is looked up per request, so one that is reset or added later is
// served too. Clients must skip certificate verification.
func (f *Fleet) ServeTLS(addr, server string) (string, func(context.Context) error, error) {
	cert, err := selfSignedCert()
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, found := f.Get(server)
			if !found {
				http.Error(w, "no such server: "+server, http.StatusNotFound)
				return
			}
			s.ServeLLM(w, r, "/bedrock"+r.URL.Path)
		}),
	}
	go func() {
		// A closed listener is the normal way this ends, so it is not news.
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("chaosmock: tls serve stopped: %v", err)
		}
	}()
	return "https://" + ln.Addr().String(), srv.Shutdown, nil
}

// selfSignedCert makes a throwaway certificate for the loopback addresses. A
// client of the TLS listener skips verification, so it only has to parse.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mcpchaos"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// converseBlocks are the turn's content blocks in Bedrock form. Arguments were
// checked by renderable.
func (rp llmReply) converseBlocks() []any {
	blocks := []any{}
	if rp.turn.Text != "" {
		blocks = append(blocks, map[string]any{"text": rp.turn.Text})
	}
	for i, c := range rp.turn.ToolCalls {
		args, _ := c.argsObject()
		blocks = append(blocks, map[string]any{"toolUse": map[string]any{"toolUseId": c.callID(rp.ordinal, i), "name": c.Name, "input": args}})
	}
	return blocks
}

// converseUsage is the turn's usage in Bedrock form.
func (rp llmReply) converseUsage() map[string]any {
	u := rp.turn.usage()
	return map[string]any{"inputTokens": u.Input, "outputTokens": u.Output, "totalTokens": u.Input + u.Output}
}

// converseBody is a Bedrock Converse response.
func (rp llmReply) converseBody() map[string]any {
	return map[string]any{
		"output":     map[string]any{"message": map[string]any{"role": "assistant", "content": rp.converseBlocks()}},
		"stopReason": anthropicStop(rp.turn.finish()),
		"usage":      rp.converseUsage(),
		"metrics":    map[string]any{"latencyMs": 1},
	}
}

// streamConverse writes a Bedrock ConverseStream: messageStart, the text's
// deltas, each tool call opened by contentBlockStart with its input deltas,
// messageStop with the stop reason, and metadata with usage. Text has no
// contentBlockStart, as on the real stream.
func (rp llmReply) streamConverse(ew *eventStreamWriter) {
	ew.send("messageStart", map[string]any{"role": "assistant"})
	index := 0
	if rp.turn.Text != "" {
		for _, part := range halves(rp.turn.Text) {
			ew.send("contentBlockDelta", map[string]any{"contentBlockIndex": index, "delta": map[string]any{"text": part}})
		}
		ew.send("contentBlockStop", map[string]any{"contentBlockIndex": index})
		index++
	}
	for i, c := range rp.turn.ToolCalls {
		args, _ := c.argsObject()
		ew.send("contentBlockStart", map[string]any{"contentBlockIndex": index, "start": map[string]any{"toolUse": map[string]any{"toolUseId": c.callID(rp.ordinal, i), "name": c.Name}}})
		for _, part := range halves(string(args)) {
			ew.send("contentBlockDelta", map[string]any{"contentBlockIndex": index, "delta": map[string]any{"toolUse": map[string]any{"input": part}}})
		}
		ew.send("contentBlockStop", map[string]any{"contentBlockIndex": index})
		index++
	}
	ew.send("messageStop", map[string]any{"stopReason": anthropicStop(rp.turn.finish())})
	ew.send("metadata", map[string]any{"usage": rp.converseUsage(), "metrics": map[string]any{"latencyMs": 1}})
}

// eventStreamWriter writes AWS event stream messages, flushing each and pacing
// them as sseWriter does.
type eventStreamWriter struct {
	w     http.ResponseWriter
	r     *http.Request
	gap   time.Duration
	first bool
	ok    bool
}

// newEventStream starts an AWS event stream.
func newEventStream(w http.ResponseWriter, r *http.Request, gap time.Duration) *eventStreamWriter {
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	return &eventStreamWriter{w: w, r: r, gap: gap, first: true, ok: true}
}

// send writes one event message of the given event type with a JSON payload.
// It stops writing once the client has hung up.
func (ew *eventStreamWriter) send(event string, payload any) {
	if !ew.ok {
		return
	}
	if !ew.first && ew.gap > 0 && !sleepOrDone(ew.r, ew.gap) {
		ew.ok = false
		return
	}
	ew.first = false
	data, _ := json.Marshal(payload)
	frame := eventStreamFrame([][2]string{{":event-type", event}, {":content-type", "application/json"}, {":message-type", "event"}}, data)
	if _, err := ew.w.Write(frame); err != nil {
		ew.ok = false
		return
	}
	if fl, isFlusher := ew.w.(http.Flusher); isFlusher {
		fl.Flush()
	}
}

// eventStreamFrame encodes one event stream message: the total and headers
// lengths with a CRC32 of those eight bytes, string-valued headers, the
// payload, and a CRC32 of everything before it. A decoder checks both CRCs.
func eventStreamFrame(headers [][2]string, payload []byte) []byte {
	var hdr []byte
	for _, h := range headers {
		hdr = append(hdr, byte(len(h[0])))
		hdr = append(hdr, h[0]...)
		hdr = append(hdr, 7) // the string value type
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(len(h[1])))
		hdr = append(hdr, h[1]...)
	}
	total := 12 + len(hdr) + len(payload) + 4
	buf := make([]byte, 0, total)
	buf = binary.BigEndian.AppendUint32(buf, uint32(total))
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(hdr)))
	buf = binary.BigEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf))
	buf = append(buf, hdr...)
	buf = append(buf, payload...)
	return binary.BigEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf))
}

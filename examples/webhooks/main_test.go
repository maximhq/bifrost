package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// referenceTime is the fixed "now" used by the vector tests so a delivery
// signed at the vector's timestamp is always within tolerance.
var referenceTime = time.Unix(1614265330, 0)

// TestSignReferenceVector pins the canonical Standard Webhooks example. This is
// the SAME vector Bifrost's own signer test pins, so a match here proves this
// receiver verifies exactly what Bifrost signs.
func TestSignReferenceVector(t *testing.T) {
	got, err := sign(
		"whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
		"msg_p5jXN8AQM9LWM0D4loKWxJek",
		1614265330,
		[]byte(`{"test": 2432232314}`),
	)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	const want = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
	if got != want {
		t.Fatalf("signature mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestVerifyAcceptsValidDelivery(t *testing.T) {
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	body := []byte(`{"event":"async_job.completed"}`)
	sig, err := sign(secret, "msg_1", referenceTime.Unix(), body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := verify(secret, "msg_1", "1614265330", sig, body, referenceTime); err != nil {
		t.Fatalf("valid delivery rejected: %v", err)
	}
}

func TestVerifyAcceptsOneOfMultipleSignatures(t *testing.T) {
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	body := []byte(`{}`)
	valid, err := sign(secret, "msg_1", referenceTime.Unix(), body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// During secret rotation the header can carry several signatures; any match
	// must be accepted.
	header := strings.Join([]string{"v1,AAAA", valid, "v1,BBBB"}, " ")
	if err := verify(secret, "msg_1", "1614265330", header, body, referenceTime); err != nil {
		t.Fatalf("multi-signature delivery rejected: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	body := []byte(`{"a":1}`)
	sig, err := sign(secret, "msg_1", referenceTime.Unix(), body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	cases := []struct {
		name        string
		id, ts, sig string
		body        []byte
		now         time.Time
	}{
		{"tampered body", "msg_1", "1614265330", sig, []byte(`{"a":2}`), referenceTime},
		{"wrong id", "msg_2", "1614265330", sig, body, referenceTime},
		{"forged signature", "msg_1", "1614265330", "v1,deadbeef", body, referenceTime},
		{"stale timestamp", "msg_1", "1614265330", sig, body, referenceTime.Add(10 * time.Minute)},
		{"missing headers", "", "", "", body, referenceTime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := verify(secret, tc.id, tc.ts, tc.sig, tc.body, tc.now); err == nil {
				t.Fatalf("expected rejection, got nil")
			}
		})
	}
}

func TestParseRequiredHeaders(t *testing.T) {
	got, err := parseRequiredHeaders("Authorization=Bearer s3cret, X-Env=prod")
	if err != nil {
		t.Fatalf("parseRequiredHeaders: %v", err)
	}
	want := map[string]string{"Authorization": "Bearer s3cret", "X-Env": "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed headers mismatch:\n got %v\nwant %v", got, want)
	}

	if _, err := parseRequiredHeaders("no-equals-sign"); err == nil {
		t.Fatal("expected error for pair without '='")
	}
	if got, err := parseRequiredHeaders(""); err != nil || len(got) != 0 {
		t.Fatalf("empty input: got %v, %v; want empty map, nil", got, err)
	}
}

func TestCheckRequiredHeaders(t *testing.T) {
	required := map[string]string{"Authorization": "Bearer s3cret"}

	h := http.Header{}
	h.Set("Authorization", "Bearer s3cret")
	if err := checkRequiredHeaders(h, required); err != nil {
		t.Fatalf("matching headers rejected: %v", err)
	}

	if err := checkRequiredHeaders(http.Header{}, required); err == nil {
		t.Fatal("expected rejection when required header is missing")
	}

	h.Set("Authorization", "Bearer wrong")
	if err := checkRequiredHeaders(h, required); err == nil {
		t.Fatal("expected rejection when required header value mismatches")
	}

	if err := checkRequiredHeaders(http.Header{}, nil); err != nil {
		t.Fatalf("nil required set must accept everything: %v", err)
	}
}

package lib

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

// A 1x1 transparent PNG, small enough to inline and a genuinely valid image.
const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// mockBrandingStore stands in for the enterprise-owned branding table. It is
// deliberately not part of MockConfigStore: the blob is not in the OSS schema,
// so an OSS config store has no way to produce one.
type mockBrandingStore struct {
	raw *string
}

func (m *mockBrandingStore) GetBranding(ctx context.Context) (*string, error) {
	return m.raw, nil
}

func (m *mockBrandingStore) UpdateBranding(ctx context.Context, branding *string) error {
	m.raw = branding
	return nil
}

// seededStore returns a store already holding raw, the shape an enterprise
// deployment would leave behind.
func seededStore(raw string) *mockBrandingStore {
	return &mockBrandingStore{raw: &raw}
}

// storedBranding is a ready-to-persist blob with both slots populated, the
// shape an enterprise deployment would leave in the database.
func storedBranding(t *testing.T) *string {
	t.Helper()
	encoded, err := json.Marshal(&Branding{
		Logo:     testPNGBase64,
		LogoMIME: "image/png",
		Icon:     testPNGBase64,
		IconMIME: "image/png",
	})
	if err != nil {
		t.Fatalf("failed to encode test branding: %v", err)
	}
	raw := string(encoded)
	return &raw
}

// TestBrandingOSSIgnoresPopulatedStore is the load-bearing test for the
// enterprise gate. The blob lives in an enterprise-owned table, so an OSS
// process is normally constructed with a nil store and could not read one
// anyway. This covers the case that survives that: a populated store reaching
// an OSS process regardless — a future caller wiring one up, or a build that
// was enterprise before. In every such case OSS must still report default
// Bifrost branding.
func TestBrandingOSSIgnoresPopulatedStore(t *testing.T) {
	store := &mockBrandingStore{}
	if err := store.UpdateBranding(context.Background(), storedBranding(t)); err != nil {
		t.Fatalf("failed to seed branding: %v", err)
	}

	// Sanity check: the store really is populated, so a nil read below is the
	// gate doing its job rather than an empty fixture.
	if raw, err := store.GetBranding(context.Background()); err != nil || raw == nil {
		t.Fatalf("fixture not seeded: raw=%v err=%v", raw, err)
	}

	oss := NewBrandingService(store, false)
	if got := oss.Get(context.Background()); got != nil {
		t.Errorf("OSS Get returned branding %+v, want nil", got)
	}
	if got := oss.Asset(context.Background(), BrandingAssetLogo); got != nil {
		t.Error("OSS Asset returned a logo, want nil")
	}
	if got := oss.Asset(context.Background(), BrandingAssetIcon); got != nil {
		t.Error("OSS Asset returned an icon, want nil")
	}

	// The same store on the enterprise build must serve the blob, which proves
	// the nils above come from the gate and not from a broken fixture or an
	// unconditionally-nil read path.
	ent := NewBrandingService(store, true)
	if got := ent.Get(context.Background()); got == nil {
		t.Fatal("enterprise Get returned nil, want the seeded branding")
	}
	if got := ent.Asset(context.Background(), BrandingAssetLogo); got == nil {
		t.Error("enterprise Asset returned no logo, want the seeded one")
	}
}

// TestBrandingNilStore covers how OSS is actually constructed: with no store at
// all, because the branding table is enterprise-owned and an OSS binary has no
// schema for it. Reads must report default branding rather than failing, and a
// write must not panic on the nil store.
func TestBrandingNilStore(t *testing.T) {
	oss := NewBrandingService(nil, false)
	if got := oss.Get(context.Background()); got != nil {
		t.Errorf("Get returned %+v, want nil", got)
	}
	if got := oss.Asset(context.Background(), BrandingAssetLogo); got != nil {
		t.Error("Asset returned a logo, want nil")
	}
	if err := oss.Update(context.Background(), &Branding{Logo: testPNGBase64, LogoMIME: "image/png"}); !errors.Is(err, ErrBrandingEnterpriseOnly) {
		t.Errorf("Update returned %v, want ErrBrandingEnterpriseOnly", err)
	}

	// An enterprise build whose store failed to wire up must degrade the same
	// way on reads, and report the failure rather than silently dropping a
	// write.
	ent := NewBrandingService(nil, true)
	if got := ent.Get(context.Background()); got != nil {
		t.Errorf("enterprise Get with nil store returned %+v, want nil", got)
	}
	if err := ent.Update(context.Background(), &Branding{Logo: testPNGBase64, LogoMIME: "image/png"}); err == nil {
		t.Error("enterprise Update with nil store succeeded, want an error")
	}
}

// TestBrandingOSSRefusesWrite covers the write half of the gate. No OSS route
// reaches Update today, so this guards against a future caller wiring the
// service up somewhere that is not enterprise-gated.
func TestBrandingOSSRefusesWrite(t *testing.T) {
	store := &mockBrandingStore{}
	oss := NewBrandingService(store, false)

	err := oss.Update(context.Background(), &Branding{Logo: testPNGBase64, LogoMIME: "image/png"})
	if !errors.Is(err, ErrBrandingEnterpriseOnly) {
		t.Fatalf("OSS Update returned %v, want ErrBrandingEnterpriseOnly", err)
	}

	// The refusal must happen before the store is touched: a rejected write
	// that still cleared or wrote the column would be worse than no gate.
	if raw, err := store.GetBranding(context.Background()); err != nil || raw != nil {
		t.Errorf("OSS Update wrote to the store: raw=%v err=%v", raw, err)
	}

	// Reset is the same code path with a nil blob, and must be refused too —
	// otherwise OSS could wipe an enterprise deployment's stored logo.
	if err := oss.Update(context.Background(), nil); !errors.Is(err, ErrBrandingEnterpriseOnly) {
		t.Fatalf("OSS reset returned %v, want ErrBrandingEnterpriseOnly", err)
	}
}

// TestBrandingRejectsSVG pins the format allowlist. An SVG is an active-content
// document: navigating directly to the asset URL would execute any script it
// carries, same-origin with the dashboard.
func TestBrandingRejectsSVG(t *testing.T) {
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	ent := NewBrandingService(&mockBrandingStore{}, true)

	err := ent.Update(context.Background(), &Branding{Logo: svg, LogoMIME: "image/svg+xml"})
	if !errors.Is(err, ErrBrandingInvalid) {
		t.Fatalf("SVG upload returned %v, want ErrBrandingInvalid", err)
	}
}

// TestBrandingRejectsMislabelledContent covers the gap the allowlist alone
// leaves open: the declared MIME is caller-supplied, so an SVG (or any other
// non-raster payload) can be smuggled past it simply by labelling it
// "image/png". The bytes themselves must be sniffed, not just the label.
func TestBrandingRejectsMislabelledContent(t *testing.T) {
	cases := map[string]string{
		"svg as png":        `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"html as png":       `<!DOCTYPE html><html><body><script>alert(1)</script></body></html>`,
		"plain text as png": "definitely not an image",
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			encoded := base64.StdEncoding.EncodeToString([]byte(payload))
			ent := NewBrandingService(&mockBrandingStore{}, true)
			err := ent.Update(context.Background(), &Branding{Logo: encoded, LogoMIME: "image/png"})
			if !errors.Is(err, ErrBrandingInvalid) {
				t.Fatalf("mislabelled upload returned %v, want ErrBrandingInvalid", err)
			}
		})
	}

	// A genuine PNG declared as a different allowed raster type is a mismatch
	// too — the check is "bytes match label", not merely "bytes are an image".
	t.Run("png declared as jpeg", func(t *testing.T) {
		ent := NewBrandingService(&mockBrandingStore{}, true)
		err := ent.Update(context.Background(), &Branding{Logo: testPNGBase64, LogoMIME: "image/jpeg"})
		if !errors.Is(err, ErrBrandingInvalid) {
			t.Fatalf("cross-labelled upload returned %v, want ErrBrandingInvalid", err)
		}
	})

	// The honest case must still pass, so the check above cannot be satisfied by
	// rejecting everything.
	t.Run("matching png is accepted", func(t *testing.T) {
		ent := NewBrandingService(&mockBrandingStore{}, true)
		if err := ent.Update(context.Background(), &Branding{Logo: testPNGBase64, LogoMIME: "image/png"}); err != nil {
			t.Fatalf("valid PNG upload returned %v, want nil", err)
		}
	})
}

// TestBrandingCorruptBlobFallsBackToDefault covers the other half of the
// fallback invariant: unreadable data in the column must degrade to default
// branding, never to an error or a broken image.
func TestBrandingCorruptBlobFallsBackToDefault(t *testing.T) {
	cases := map[string]string{
		"not json":          "{{{",
		"empty object":      "{}",
		"undecodable image": `{"logo":"!!!not base64!!!","logo_mime":"image/png"}`,
		"disallowed mime":   `{"logo":"` + testPNGBase64 + `","logo_mime":"image/svg+xml"}`,
		// Hand-edited into the database, bypassing the write path's validation.
		"mislabelled bytes": `{"logo":"` + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)) + `","logo_mime":"image/png"}`,
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			ent := NewBrandingService(seededStore(raw), true)
			if got := ent.Get(context.Background()); got != nil {
				t.Errorf("Get returned %+v, want nil (default branding)", got)
			}
			if got := ent.Asset(context.Background(), BrandingAssetLogo); got != nil {
				t.Error("Asset returned a logo, want nil (default branding)")
			}
		})
	}
}

// TestBrandingPerSlotFallback checks that the two slots are independent: a
// deployment that uploads only a logo keeps the default Bifrost icon, and a
// corrupt icon does not cost the customer their valid logo.
func TestBrandingPerSlotFallback(t *testing.T) {
	t.Run("logo only", func(t *testing.T) {
		ent := NewBrandingService(&mockBrandingStore{}, true)
		if err := ent.Update(context.Background(), &Branding{Logo: testPNGBase64, LogoMIME: "image/png"}); err != nil {
			t.Fatalf("Update failed: %v", err)
		}
		if ent.Asset(context.Background(), BrandingAssetLogo) == nil {
			t.Error("logo asset is nil, want the uploaded one")
		}
		if ent.Asset(context.Background(), BrandingAssetIcon) != nil {
			t.Error("icon asset is set, want nil so the default is used")
		}
	})

	t.Run("corrupt icon keeps logo", func(t *testing.T) {
		raw := `{"logo":"` + testPNGBase64 + `","logo_mime":"image/png","icon":"!!!","icon_mime":"image/png"}`
		ent := NewBrandingService(seededStore(raw), true)
		if ent.Asset(context.Background(), BrandingAssetLogo) == nil {
			t.Error("logo asset is nil, want it preserved despite the corrupt icon")
		}
		if ent.Asset(context.Background(), BrandingAssetIcon) != nil {
			t.Error("icon asset is set, want nil so the default is used")
		}
	})
}

package lib

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// White-label branding.
//
// Enterprise deployments can replace the Bifrost logo and icon in the dashboard
// with their own. The images are stored base64-encoded in a single JSON blob,
// persisted through the BrandingStore below.
//
// White-labelling is enterprise-only, enforced in three independent places so
// no single mistake can leak it into OSS. The blob lives in an enterprise-owned
// table, so an OSS binary has no schema for it at all; OSS registers no write
// route; and this service is constructed inert on OSS, which makes it refuse
// writes and report default branding no matter what a store handed to it might
// return. The last of the three is what survives an operator editing the
// database by hand.
//
// The remaining invariant is fallback: a nil, empty, or malformed blob must
// always degrade to default branding rather than producing a broken image.

const (
	// MaxBrandingAssetBytes caps each decoded image. Both assets are read into
	// memory and inlined into the pre-hydration HTML shell, so this bound keeps
	// the shell small and the request cheap. Matches the 256KB cap the
	// user-agent mapping logo upload already enforces in the UI.
	MaxBrandingAssetBytes = 256 * 1024

	// BrandingAssetLogo and BrandingAssetIcon are the two brandable slots: the
	// full wordmark shown in the expanded sidebar and on the login screen, and
	// the square mark shown when the sidebar is collapsed.
	BrandingAssetLogo = "logo"
	BrandingAssetIcon = "icon"
)

// allowedBrandingMIMEs is deliberately raster-only. An uploaded SVG is an
// active-content document: anyone who navigates directly to the asset URL (as
// opposed to loading it through an <img> tag) would execute script embedded in
// it, same-origin with the dashboard. Sanitizing SVG correctly is far more
// work than the feature warrants, so the format is simply not accepted.
var allowedBrandingMIMEs = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/webp": {},
	"image/gif":  {},
}

// ErrBrandingInvalid marks every rejection that is the caller's fault, so
// handlers can map the whole class to 400 without matching on message text.
var ErrBrandingInvalid = errors.New("invalid branding")

// warnBranding logs a degradation notice, tolerating an unset package logger.
//
// Every call site is on the fallback-to-default path, whose whole purpose is to
// keep the dashboard up when branding is unusable. Panicking there on a nil
// logger would invert that, so the nil check is part of the guarantee rather
// than defensive noise.
func warnBranding(format string, args ...any) {
	if logger == nil {
		return
	}
	logger.Warn(format, args...)
}

// ErrBrandingEnterpriseOnly is returned when an OSS process attempts to write
// branding. It should never be reachable through the HTTP surface — OSS
// registers no write route — so it exists as a backstop against a future caller
// wiring the service up somewhere new.
var ErrBrandingEnterpriseOnly = errors.New("branding is an enterprise feature")

// BrandingStore is the persistence BrandingService needs.
//
// It is a narrow interface rather than configstore.ConfigStore because the blob
// is not part of the OSS schema: it lives in a table the enterprise repo owns
// and migrates. OSS therefore has no implementation and passes nil, and the
// enterprise build supplies one backed by its own config store.
//
// Keeping the table out of framework_configs also removes a sharp edge that the
// shared-column design had: UpdateFrameworkConfig deletes and recreates the
// singleton row, so a column OSS did not know how to carry forward would have
// been wiped on any restart that triggered a config backfill.
type BrandingStore interface {
	// GetBranding returns the stored blob, or nil when none is configured.
	GetBranding(ctx context.Context) (*string, error)
	// UpdateBranding replaces the stored blob. Pass nil to clear it and
	// restore the default Bifrost branding.
	UpdateBranding(ctx context.Context, branding *string) error
}

// Branding is the JSON blob persisted by BrandingStore.
//
// Each asset is optional and independent: a deployment may override only the
// logo, only the icon, or both. An absent asset falls back to the Bifrost
// default for that slot alone.
type Branding struct {
	Logo      string `json:"logo,omitempty"`      // base64-encoded image bytes
	LogoMIME  string `json:"logo_mime,omitempty"` // e.g. "image/png"
	Icon      string `json:"icon,omitempty"`
	IconMIME  string `json:"icon_mime,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"` // RFC3339
}

// IsEmpty reports whether the blob carries no usable asset, in which case
// callers must serve default Bifrost branding.
func (b *Branding) IsEmpty() bool {
	return b == nil || (b.Logo == "" && b.Icon == "")
}

// HasLogo and HasIcon report per-slot availability. Both the MIME type and the
// data must be present for an asset to be servable.
func (b *Branding) HasLogo() bool { return b != nil && b.Logo != "" && b.LogoMIME != "" }
func (b *Branding) HasIcon() bool { return b != nil && b.Icon != "" && b.IconMIME != "" }

// BrandingAsset is a decoded, ready-to-serve image.
type BrandingAsset struct {
	Data []byte
	MIME string
	ETag string
}

// BrandingService owns the branding blob: it validates writes, persists them,
// and reads them back.
//
// It is deliberately stateless — nothing is held in memory between calls. Every
// read goes to the database and decodes on demand, so a deployment carrying two
// 256KB logos costs no steady-state memory, and every node in a cluster sees a
// change immediately without any cache-invalidation machinery.
//
// The cost is a row read plus a base64 decode per call, which lands on two
// paths: the branding asset endpoint (rare in practice — responses carry a
// content-versioned URL, a long max-age, and an ETag, so browsers refetch only
// when the logo actually changes) and the pre-hydration HTML shell, which
// consults it once per dashboard document request. On OSS every read
// short-circuits on the enterprise check below, before touching the database.
type BrandingService struct {
	store BrandingStore
	// enterprise gates the whole feature. When false the service behaves as if
	// branding were never configured: reads return nil and writes are refused,
	// regardless of what the store actually holds.
	enterprise bool
}

// NewBrandingService builds the service. store is nil on OSS, which has no
// branding table, and nil is also the outcome when no config store is
// configured at all; either way branding is permanently absent and every read
// reports default branding rather than failing.
//
// enterprise comes from schemas.BifrostContextKeyIsEnterprise on the bootstrap
// context. Passing false makes the service inert independently of the store, so
// the guarantee holds even if a future caller wires a populated store into an
// OSS process.
func NewBrandingService(store BrandingStore, enterprise bool) *BrandingService {
	return &BrandingService{store: store, enterprise: enterprise}
}

// Get returns the current branding blob, read fresh from the store. A nil
// result means default Bifrost branding.
//
// A malformed blob in the database is treated as absent rather than as an
// error: corrupt branding must never take the dashboard down.
func (s *BrandingService) Get(ctx context.Context) *Branding {
	blob, _, _ := s.load(ctx)
	return blob
}

// Asset returns the decoded image for a slot, or nil when that slot is unset
// and the default should be served. kind is BrandingAssetLogo or
// BrandingAssetIcon.
func (s *BrandingService) Asset(ctx context.Context, kind string) *BrandingAsset {
	_, logo, icon := s.load(ctx)
	switch kind {
	case BrandingAssetLogo:
		return logo
	case BrandingAssetIcon:
		return icon
	default:
		return nil
	}
}

// load reads the blob from the store and decodes each populated slot. It
// returns (nil, nil, nil) whenever branding is absent, unreadable, or
// unusable — the fallback-to-default invariant — and never returns an error,
// because no caller can do anything useful with one beyond serving the default.
func (s *BrandingService) load(ctx context.Context) (*Branding, *BrandingAsset, *BrandingAsset) {
	// Enterprise gate first, before the store read. An OSS process must serve
	// default branding even if a store were to hand it a valid blob — written
	// by a direct database edit, or left behind by a downgrade from enterprise.
	if !s.enterprise || s.store == nil {
		return nil, nil, nil
	}
	raw, err := s.store.GetBranding(ctx)
	if err != nil {
		warnBranding("branding: failed to load from store, serving default branding: %v", err)
		return nil, nil, nil
	}
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil, nil
	}
	var blob Branding
	if err := json.Unmarshal([]byte(*raw), &blob); err != nil {
		warnBranding("branding: stored blob is not valid JSON, serving default branding: %v", err)
		return nil, nil, nil
	}
	if blob.IsEmpty() {
		return nil, nil, nil
	}

	// A slot that fails to decode is dropped individually — a corrupt icon
	// should not cost the customer their logo.
	var logo, icon *BrandingAsset
	if blob.HasLogo() {
		if asset, err := decodeBrandingAsset(blob.Logo, blob.LogoMIME); err != nil {
			warnBranding("branding: stored logo is unusable, falling back to default: %v", err)
			blob.Logo, blob.LogoMIME = "", ""
		} else {
			logo = asset
		}
	}
	if blob.HasIcon() {
		if asset, err := decodeBrandingAsset(blob.Icon, blob.IconMIME); err != nil {
			warnBranding("branding: stored icon is unusable, falling back to default: %v", err)
			blob.Icon, blob.IconMIME = "", ""
		} else {
			icon = asset
		}
	}
	if blob.IsEmpty() {
		return nil, nil, nil
	}
	return &blob, logo, icon
}

// Update validates and persists a new branding blob. Passing nil (or a blob
// with no assets) clears branding and restores the Bifrost defaults.
//
// Validation errors wrap ErrBrandingInvalid so callers can answer 400.
func (s *BrandingService) Update(ctx context.Context, blob *Branding) error {
	if !s.enterprise {
		return ErrBrandingEnterpriseOnly
	}
	if s.store == nil {
		return errors.New("config store not available")
	}

	var toStore *string
	if !blob.IsEmpty() {
		if err := validateBranding(blob); err != nil {
			return err
		}
		blob.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		encoded, err := json.Marshal(blob)
		if err != nil {
			return fmt.Errorf("failed to encode branding: %w", err)
		}
		raw := string(encoded)
		toStore = &raw
	}

	if err := s.store.UpdateBranding(ctx, toStore); err != nil {
		return fmt.Errorf("failed to persist branding: %w", err)
	}
	// Nothing to invalidate: reads go straight to the database, so the next one
	// sees this write — on this node and on every other node in a cluster.
	return nil
}

// validateBranding enforces the MIME allowlist and size cap on each populated
// slot, and verifies the payload is decodable base64. An asset supplied without
// its MIME type (or vice versa) is rejected rather than silently dropped, since
// that almost always signals a malformed client request.
func validateBranding(blob *Branding) error {
	if (blob.Logo == "") != (blob.LogoMIME == "") {
		return fmt.Errorf("%w: logo and logo_mime must be provided together", ErrBrandingInvalid)
	}
	if (blob.Icon == "") != (blob.IconMIME == "") {
		return fmt.Errorf("%w: icon and icon_mime must be provided together", ErrBrandingInvalid)
	}
	if blob.Logo != "" {
		if _, err := decodeBrandingAsset(blob.Logo, blob.LogoMIME); err != nil {
			return fmt.Errorf("%w: logo: %s", ErrBrandingInvalid, err)
		}
	}
	if blob.Icon != "" {
		if _, err := decodeBrandingAsset(blob.Icon, blob.IconMIME); err != nil {
			return fmt.Errorf("%w: icon: %s", ErrBrandingInvalid, err)
		}
	}
	return nil
}

// decodeBrandingAsset checks the MIME type, decodes the base64 payload,
// enforces the size cap on the decoded bytes, and verifies the bytes actually
// are the image type they were declared as.
//
// The size check runs against the encoded length first so an oversized upload
// is rejected before allocating the decode buffer, then against the decoded
// length for the authoritative bound.
func decodeBrandingAsset(encoded, mimeType string) (*BrandingAsset, error) {
	normalizedMIME := strings.ToLower(strings.TrimSpace(mimeType))
	// Drop any parameters, e.g. "image/png; charset=binary".
	if idx := strings.IndexByte(normalizedMIME, ';'); idx >= 0 {
		normalizedMIME = strings.TrimSpace(normalizedMIME[:idx])
	}
	if _, ok := allowedBrandingMIMEs[normalizedMIME]; !ok {
		return nil, fmt.Errorf("unsupported image type %q (allowed: image/png, image/jpeg, image/webp, image/gif)", mimeType)
	}

	// base64 expands by 4/3; anything beyond that bound cannot decode within
	// the cap, so reject before doing the work.
	if len(encoded) > base64.StdEncoding.EncodedLen(MaxBrandingAssetBytes) {
		return nil, fmt.Errorf("image exceeds the %dKB limit", MaxBrandingAssetBytes/1024)
	}

	// Accept a full data URI ("data:image/png;base64,....") as well as bare
	// base64, since browser FileReader output carries the prefix.
	if idx := strings.Index(encoded, ";base64,"); idx >= 0 {
		encoded = encoded[idx+len(";base64,"):]
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("image is not valid base64: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("image is empty")
	}
	if len(data) > MaxBrandingAssetBytes {
		return nil, fmt.Errorf("image exceeds the %dKB limit", MaxBrandingAssetBytes/1024)
	}

	// The MIME check above only vetted what the caller *claimed*; sniff the bytes
	// so a payload cannot be smuggled past the raster-only allowlist by mislabelling
	// it (SVG or any other active-content document declared as image/png). The
	// allowlist is a subset of the signatures the sniffer recognises — PNG, JPEG,
	// GIF and WebP all have exact signatures in the MIME Sniffing Standard — so a
	// genuine upload of an allowed type always round-trips.
	if detected := http.DetectContentType(data); detected != normalizedMIME {
		return nil, fmt.Errorf("image content is %q but was declared as %q", detected, normalizedMIME)
	}

	sum := sha256.Sum256(data)
	return &BrandingAsset{
		Data: data,
		MIME: normalizedMIME,
		ETag: `"` + hex.EncodeToString(sum[:16]) + `"`,
	}, nil
}

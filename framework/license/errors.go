package license

import "errors"

// ErrLicenseExpired is returned when a license JWT's exp claim is in the past.
var ErrLicenseExpired = errors.New("license expired")

// ErrInvalidLicense is returned when the JWT is malformed.
var ErrInvalidLicense = errors.New("invalid license format")

// ErrInvalidSignature is returned when the JWT signature does not verify.
var ErrInvalidSignature = errors.New("invalid license signature")

// ErrFeatureNotLicensed is the sentinel returned by handlers when a feature
// is disabled by the license. HTTP layer SHOULD translate this to HTTP 402.
var ErrFeatureNotLicensed = errors.New("feature not included in current license plan")

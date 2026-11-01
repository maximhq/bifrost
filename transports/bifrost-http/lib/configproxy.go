package lib

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// GlobalProxyFileConfig is the global outbound proxy as config.json declares it: the same settings
// the dashboard's proxy page edits, including which components use it (SCIM directory sync,
// inference, API). The URL and credentials take env./vault references like other file secrets.
type GlobalProxyFileConfig struct {
	Enabled            bool                    `json:"enabled"`
	Type               network.GlobalProxyType `json:"type,omitempty"`
	URL                *schemas.SecretVar      `json:"url,omitempty"`
	Username           *schemas.SecretVar      `json:"username,omitempty"`
	Password           *schemas.SecretVar      `json:"password,omitempty"`
	NoProxy            string                  `json:"no_proxy,omitempty"`
	Timeout            int                     `json:"timeout,omitempty"`
	SkipTLSVerify      bool                    `json:"skip_tls_verify,omitempty"`
	EnableForSCIM      bool                    `json:"enable_for_scim,omitempty"`
	EnableForInference bool                    `json:"enable_for_inference,omitempty"`
	EnableForAPI       bool                    `json:"enable_for_api,omitempty"`
}

// resolved is the declaration with its references resolved, in the shape the store keeps. An
// omitted type reads as http, the only type the proxy supports today.
func (c *GlobalProxyFileConfig) resolved() configstoreTables.GlobalProxyConfig {
	proxyType := c.Type
	if proxyType == "" {
		proxyType = network.GlobalProxyTypeHTTP
	}
	return configstoreTables.GlobalProxyConfig{
		Enabled:            c.Enabled,
		Type:               proxyType,
		URL:                secretValue(c.URL),
		Username:           secretValue(c.Username),
		Password:           secretValue(c.Password),
		NoProxy:            c.NoProxy,
		Timeout:            c.Timeout,
		SkipTLSVerify:      c.SkipTLSVerify,
		EnableForSCIM:      c.EnableForSCIM,
		EnableForInference: c.EnableForInference,
		EnableForAPI:       c.EnableForAPI,
	}
}

// secretValue resolves an optional reference, "" when it is absent.
func secretValue(v *schemas.SecretVar) string {
	if v == nil {
		return ""
	}
	return v.GetValue()
}

// validateGlobalProxyConfig applies the rules the proxy settings endpoint applies, so config.json
// cannot store a proxy the dashboard would refuse.
func validateGlobalProxyConfig(c configstoreTables.GlobalProxyConfig) error {
	if !c.Enabled {
		return nil
	}
	switch c.Type {
	case network.GlobalProxyTypeHTTP, network.GlobalProxyTypeSOCKS5:
	case network.GlobalProxyTypeTCP:
		return fmt.Errorf("proxy type %s is not yet supported", c.Type)
	default:
		return fmt.Errorf("invalid proxy type: %s", c.Type)
	}
	if c.URL == "" {
		return errors.New("proxy url is required when the proxy is enabled")
	}
	if _, err := ValidateGlobalProxyURL(c.Type, c.URL); err != nil {
		return err
	}
	if c.Timeout < 0 {
		return errors.New("proxy timeout must be non-negative")
	}
	return nil
}

// ValidateGlobalProxyURL checks a global proxy URL the way the HTTP client factory will
// read it (HTTPClientFactory.proxyURLForPurpose): see parseGlobalProxyURL. A socks5 proxy
// must also be named by a socks5:// or socks5h:// URL and an http proxy by an http:// or
// https:// one, compared case-insensitively as the parser lowercases schemes, because the
// dialer follows the URL's scheme: it would otherwise speak a protocol other than the
// one the type names.
func ValidateGlobalProxyURL(proxyType network.GlobalProxyType, raw string) (*url.URL, error) {
	parsed, err := parseGlobalProxyURL(proxyType, raw)
	if err != nil {
		return nil, err
	}
	if proxyType == network.GlobalProxyTypeSOCKS5 && parsed.Scheme != "socks5" && parsed.Scheme != "socks5h" {
		return nil, fmt.Errorf("a socks5 proxy URL must use the socks5:// or socks5h:// scheme")
	}
	if proxyType == network.GlobalProxyTypeHTTP && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("an http proxy URL must use the http:// or https:// scheme")
	}
	return parsed, nil
}

// parseGlobalProxyURL reads a global proxy URL: a bare host:port takes the type's scheme,
// and the URL must parse with a host and a valid port. The factory treats a URL it cannot
// parse as no proxy at all, so accepting one would send traffic direct.
func parseGlobalProxyURL(proxyType network.GlobalProxyType, raw string) (*url.URL, error) {
	effective := raw
	if !strings.Contains(raw, "://") {
		scheme := "http"
		if proxyType == network.GlobalProxyTypeSOCKS5 {
			scheme = "socks5"
		}
		effective = scheme + "://" + raw
	}
	// The errors reach a 400 body and the config.json load's log, so they never quote the
	// URL's password: a *url.Error quotes the whole input, and its inner error does not.
	parsed, err := url.Parse(effective)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("invalid proxy URL %q: %v", RedactProxyURLPassword(raw), err)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("proxy URL %q names no host", RedactProxyURLPassword(raw))
	}
	if port := parsed.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("proxy URL %q has an invalid port", RedactProxyURLPassword(raw))
		}
	}
	return parsed, nil
}

// RedactedProxyURLPassword stands in for a proxy URL's userinfo password in API responses.
const RedactedProxyURLPassword = "<redacted>"

// proxyURLPasswordSpan returns the bounds of the password in raw's userinfo, with or without a
// scheme. It works on the text, not a parsed URL, so a redacted URL keeps the form it was saved in.
func proxyURLPasswordSpan(raw string) (start, end int, ok bool) {
	authority := 0
	if i := strings.Index(raw, "://"); i >= 0 {
		authority = i + len("://")
	}
	authorityEnd := len(raw)
	if i := strings.IndexAny(raw[authority:], "/?#"); i >= 0 {
		authorityEnd = authority + i
	}
	at := strings.LastIndex(raw[authority:authorityEnd], "@")
	if at < 0 {
		return 0, 0, false
	}
	colon := strings.Index(raw[authority:authority+at], ":")
	if colon < 0 {
		return 0, 0, false
	}
	return authority + colon + 1, authority + at, true
}

// RedactProxyURLPassword masks the password in a proxy URL's userinfo. The dialers use URL
// userinfo as proxy credentials, so it is as secret as the separate password field.
func RedactProxyURLPassword(raw string) string {
	start, end, ok := proxyURLPasswordSpan(raw)
	if !ok || start == end {
		return raw
	}
	return raw[:start] + RedactedProxyURLPassword + raw[end:]
}

// RestoreProxyURLPassword puts the stored URL's password back into a URL saved with the redacted
// one, as the settings page sends back what GET returned. With no stored password the placeholder
// is dropped rather than saved.
func RestoreProxyURLPassword(raw, stored string) string {
	start, end, ok := proxyURLPasswordSpan(raw)
	if !ok || raw[start:end] != RedactedProxyURLPassword {
		return raw
	}
	storedStart, storedEnd, storedOK := proxyURLPasswordSpan(stored)
	if !storedOK {
		return raw[:start-1] + raw[end:]
	}
	return raw[:start] + stored[storedStart:storedEnd] + raw[end:]
}

// generateGlobalProxyConfigHash hashes a resolved declaration, so a rotated credential behind an
// env. reference reads as a change.
func generateGlobalProxyConfigHash(c configstoreTables.GlobalProxyConfig) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// loadProxyConfig applies config.json's proxy_config to the stored global proxy, the way the other
// file sections are reconciled:
//
//   - split (the default): the declaration is applied when it is new or changed since the hash
//     recorded at the last sync, and a dashboard edit to an unchanged declaration is kept
//   - config.json source of truth: the declaration is applied on every boot
//
// The hash lives in its own row (ConfigProxyHashKey) rather than inside the proxy row, because the
// dashboard saves that row whole: a hash inside it would be wiped by the first edit, and the next
// boot would re-apply the file over the edit. An absent proxy_config leaves the stored proxy alone.
//
// Config.ProxyConfig ends up holding the proxy in effect: the stored one, or the file's once applied.
func loadProxyConfig(ctx context.Context, config *Config, configData *ConfigData) {
	var stored *configstoreTables.GlobalProxyConfig
	if config.ConfigStore != nil {
		var err error
		stored, err = config.ConfigStore.GetProxyConfig(ctx)
		if err != nil {
			logger.Warn("failed to read the stored proxy config: %v", err)
			return
		}
		config.ProxyConfig = stored
	}
	if configData == nil || configData.ProxyConfig == nil {
		return
	}
	declared := configData.ProxyConfig.resolved()
	if err := validateGlobalProxyConfig(declared); err != nil {
		logger.Warn("invalid proxy_config in config.json, not applied: %v", err)
		return
	}
	if config.ConfigStore == nil {
		config.ProxyConfig = &declared
		return
	}
	fileHash, err := generateGlobalProxyConfigHash(declared)
	if err != nil {
		logger.Warn("failed to hash proxy_config from config.json: %v", err)
		return
	}
	if stored != nil && !configData.isConfigJSONSourceOfTruth() {
		storedHash, err := config.ConfigStore.GetConfig(ctx, configstoreTables.ConfigProxyHashKey)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			logger.Warn("failed to read the proxy config hash: %v", err)
			return
		}
		if storedHash != nil && storedHash.Value == fileHash {
			logger.Debug("proxy config hash matches, keeping DB config")
			return
		}
	}
	// The hash is cleared before the proxy is written and set after it, so a failure between the
	// writes leaves no hash rather than the previous declaration's: the next boot then re-applies the
	// file instead of matching a hash that no longer describes the stored proxy.
	if err := config.ConfigStore.UpdateConfig(ctx, &configstoreTables.TableGovernanceConfig{
		Key: configstoreTables.ConfigProxyHashKey,
	}); err != nil {
		logger.Warn("failed to clear the proxy config hash, proxy_config from config.json not applied: %v", err)
		return
	}
	if err := config.ConfigStore.UpdateProxyConfig(ctx, &declared); err != nil {
		logger.Warn("failed to save proxy_config from config.json: %v", err)
		return
	}
	if err := config.ConfigStore.UpdateConfig(ctx, &configstoreTables.TableGovernanceConfig{
		Key:   configstoreTables.ConfigProxyHashKey,
		Value: fileHash,
	}); err != nil {
		logger.Warn("failed to record the proxy config hash: %v", err)
	}
	logger.Info("proxy config synced from config.json (enabled=%t)", declared.Enabled)
	config.ProxyConfig = &declared
}

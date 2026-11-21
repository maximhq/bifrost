//go:build harness

package handlers

import (
	"fmt"
	"os"
	"strings"

	"github.com/maximhq/bifrost/core/network"
)

// harnessClientMetadataAllowlistEnv names the private hosts a harness build fetches Client ID Metadata
// Documents from, comma-separated (the MCP harness sets 127.0.0.1 for its fleet). Release builds lack
// this file, so they can never fetch documents from a private host.
const harnessClientMetadataAllowlistEnv = "BIFROST_HARNESS_CLIENT_METADATA_PRIVATE_ALLOWLIST"

func init() {
	raw := strings.TrimSpace(os.Getenv(harnessClientMetadataAllowlistEnv))
	if raw == "" {
		return
	}
	allow, err := network.ValidatePrivateTargetAllowlist(strings.Split(raw, ","))
	if err != nil {
		panic(fmt.Sprintf("%s: %v", harnessClientMetadataAllowlistEnv, err))
	}
	setClientMetadataPrivateAllowlist(allow)
}

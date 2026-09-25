package tables

import (
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/network"
)

// TestGlobalProxyConfigToNetwork pins that every stored field reaches the HTTP client
// factory. A field added to one struct and not the other would silently drop a setting.
func TestGlobalProxyConfigToNetwork(t *testing.T) {
	if (*GlobalProxyConfig)(nil).ToNetwork() != nil {
		t.Error("nil must convert to nil")
	}
	stored := GlobalProxyConfig{
		Enabled:            true,
		Type:               network.GlobalProxyTypeSOCKS5,
		URL:                "socks5://10.0.0.9:1080",
		Username:           "svc",
		Password:           "s3cret",
		NoProxy:            ".internal",
		Timeout:            7,
		SkipTLSVerify:      true,
		EnableForSCIM:      true,
		EnableForInference: true,
		EnableForAPI:       true,
	}
	got := stored.ToNetwork()
	want := &network.GlobalProxyConfig{
		Enabled:            true,
		Type:               network.GlobalProxyTypeSOCKS5,
		URL:                "socks5://10.0.0.9:1080",
		Username:           "svc",
		Password:           "s3cret",
		NoProxy:            ".internal",
		Timeout:            7,
		SkipTLSVerify:      true,
		EnableForSCIM:      true,
		EnableForInference: true,
		EnableForAPI:       true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToNetwork() = %+v, want %+v", got, want)
	}
	if n, m := reflect.TypeOf(GlobalProxyConfig{}).NumField(), reflect.TypeOf(network.GlobalProxyConfig{}).NumField(); n != m {
		t.Errorf("GlobalProxyConfig has %d fields, network.GlobalProxyConfig has %d: update ToNetwork", n, m)
	}
}

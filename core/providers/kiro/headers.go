package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"runtime"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const (
	amzTarget      = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	sdkVersion     = "1.0.27"
	nodeVersion    = "22.21.1"
	kiroIDEVersion = "1.0.0"
)

// fingerprint is the stable machine fingerprint the Kiro IDE appends to its user agent:
// sha256("<hostname>-<username>-kiro") in hex.
var fingerprint = sync.OnceValue(func() string {
	host, hostErr := os.Hostname()
	current, userErr := user.Current()
	seed := "default-kiro"
	if hostErr == nil && userErr == nil && host != "" && current.Username != "" {
		seed = host + "-" + current.Username + "-kiro"
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
})

// ideOSTag is the os/ component of the Kiro IDE (aws-sdk-js) user agent.
func ideOSTag() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos#24.0.0"
	case "windows":
		return "win32#10.0.26100"
	default:
		return "linux#6.8.0"
	}
}

// cliPlatform is the os/ component of the kiro-cli (aws-sdk-rust) user agent.
func cliPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

func cliUserAgent(includeAppVersion bool) string {
	parts := []string{
		"aws-sdk-rust/1.3.15",
		"ua/2.1",
		"api/codewhispererstreaming/0.1.17975",
		"os/" + cliPlatform(),
		"lang/rust/1.92.0",
	}
	if includeAppVersion {
		parts = append(parts, "md/appVersion-2.14.2")
	}
	parts = append(parts, "m/F", "app/AmazonQ-For-CLI")
	return strings.Join(parts, " ")
}

// runtimeHeaders returns the GenerateAssistantResponse headers for the wire client. The IDE
// profile serves accounts with their own profile ARN; the CLI profile serves Builder ID accounts
// and accounts without a profile.
func runtimeHeaders(accessToken, profileArn string, client kiroWireClient) map[string]string {
	headers := map[string]string{
		"Authorization":               "Bearer " + accessToken,
		"Content-Type":                "application/x-amz-json-1.0",
		"X-Amz-Target":                amzTarget,
		"X-Amzn-Codewhisperer-Optout": "true",
		"Amz-Sdk-Invocation-Id":       uuid.NewString(),
	}
	if client == wireClientCLI {
		headers["Accept"] = "*/*"
		headers["User-Agent"] = cliUserAgent(true)
		headers["X-Amz-User-Agent"] = cliUserAgent(false)
		headers["Amz-Sdk-Request"] = "attempt=1; max=3"
	} else {
		ideAgent := "KiroIDE-" + kiroIDEVersion + "-" + fingerprint()
		headers["Accept"] = "application/vnd.amazon.eventstream"
		headers["User-Agent"] = "aws-sdk-js/" + sdkVersion + " ua/2.1 os/" + ideOSTag() + " lang/js md/nodejs#" + nodeVersion +
			" api/codewhispererstreaming#" + sdkVersion + " m/E " + ideAgent
		headers["X-Amz-User-Agent"] = "aws-sdk-js/" + sdkVersion + " " + ideAgent
		headers["X-Amzn-Kiro-Agent-Mode"] = "vibe"
	}
	if profileArn != "" {
		headers["X-Amzn-Kiro-Profile-Arn"] = profileArn
	}
	return headers
}

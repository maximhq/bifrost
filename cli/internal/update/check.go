package update

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/cli/internal/config"
)

const (
	baseURL        = "https://downloads.getmaxim.ai"
	versionURL     = baseURL + "/bifrost-cli/latest/version.txt"
	checkInterval  = 24 * time.Hour
	requestTimeout = 3 * time.Second
)

// CheckResult holds the outcome of a version check.
type CheckResult struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	CheckedAt       int64
}

// CheckInBackground starts a goroutine that checks for a newer CLI version
// and returns the result on the returned channel. The channel receives nil
// if no check was performed or if any error occurred (silent fail).
func CheckInBackground(currentVersion, statePath string) <-chan *CheckResult {
	ch := make(chan *CheckResult, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- nil
			}
		}()

		result := doCheck(currentVersion, statePath)
		ch <- result
	}()

	return ch
}

func doCheck(currentVersion, statePath string) *CheckResult {
	if currentVersion == "dev" || strings.TrimSpace(currentVersion) == "" {
		return nil
	}
	if envSet("BIFROST_NO_UPDATE_CHECK") {
		return nil
	}

	// Try cached version from state
	state, err := config.LoadState(statePath)
	if err == nil && state.LastKnownVersion != "" {
		if time.Since(time.Unix(state.LastVersionCheck, 0)) < checkInterval {
			return compareVersions(currentVersion, state.LastKnownVersion)
		}
	}

	// Fetch latest version
	latest, err := fetchLatestVersion()
	if err != nil {
		return nil
	}

	return compareVersions(currentVersion, latest)
}

func fetchLatestVersion() (string, error) {
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Get(versionURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version check: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}

	version := strings.TrimSpace(string(body))
	if version == "" {
		return "", fmt.Errorf("version check: empty response")
	}
	return version, nil
}

func compareVersions(current, latest string) *CheckResult {
	result := &CheckResult{
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: isNewer(latest, current),
		CheckedAt:       time.Now().Unix(),
	}
	return result
}

type parsedVersion struct {
	nums       [3]int
	prerelease string // empty for stable releases
}

// isNewer returns true if a is newer than b. Both should be "vX.Y.Z" or
// "vX.Y.Z-prerelease" format. Per SemVer, a stable release is newer than a
// prerelease with the same major.minor.patch.
func isNewer(a, b string) bool {
	av := parseVersion(a)
	bv := parseVersion(b)
	if av == nil || bv == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if av.nums[i] > bv.nums[i] {
			return true
		}
		if av.nums[i] < bv.nums[i] {
			return false
		}
	}
	// Same major.minor.patch: stable > prerelease
	if av.prerelease == "" && bv.prerelease != "" {
		return true
	}
	return false
}

func parseVersion(v string) *parsedVersion {
	v = strings.TrimPrefix(v, "v")
	var pre string
	if idx := strings.Index(v, "-"); idx != -1 {
		pre = v[idx+1:]
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		nums[i] = n
	}
	return &parsedVersion{nums: nums, prerelease: pre}
}

func envSet(key string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	return v == "1" || v == "true"
}

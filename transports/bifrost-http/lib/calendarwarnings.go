package lib

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// warnMultiUnitCalendarConfig reports rolling windows in file-owned configuration,
// including enterprise sections that the OSS ConfigData does not deserialize.
// It runs only at config load, never on inference requests.
func warnMultiUnitCalendarConfig(data []byte, warn func(string, ...any)) {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return
	}
	governance, _ := root["governance"].(map[string]any)
	budgets := make(map[string]map[string]any)
	rates := make(map[string]map[string]any)
	for key, index := range map[string]map[string]map[string]any{"budgets": budgets, "rate_limits": rates} {
		rows, _ := governance[key].([]any)
		for _, row := range rows {
			if object, ok := row.(map[string]any); ok {
				id, _ := object["id"].(string)
				index[id] = map[string]any{
					"reset_duration":         object["reset_duration"],
					"token_reset_duration":   object["token_reset_duration"],
					"request_reset_duration": object["request_reset_duration"],
				}
			}
		}
	}
	// Owner-linked top-level budgets inherit alignment even without nesting.
	alignedOwners := make(map[string]bool)
	var collectOwners func(any, bool)
	collectOwners = func(value any, inherited bool) {
		switch object := value.(type) {
		case []any:
			for _, child := range object {
				collectOwners(child, inherited)
			}
		case map[string]any:
			enabled, _ := object["calendar_aligned"].(bool)
			aligned := inherited || enabled
			if id, ok := object["id"].(string); ok && aligned {
				alignedOwners[id] = true
			}
			for _, child := range object {
				collectOwners(child, aligned)
			}
		}
	}
	collectOwners(governance, false)
	collectOwners(root["access_profiles"], false)
	seen := make(map[string]bool)
	var visit func(any, string, bool)
	visit = func(value any, path string, inherited bool) {
		switch object := value.(type) {
		case []any:
			for i, child := range object {
				visit(child, fmt.Sprintf("%s[%d]", path, i), inherited)
			}
		case map[string]any:
			aligned := inherited
			for _, key := range []string{"virtual_key_id", "team_id", "customer_id", "model_config_id", "provider_config_id", "access_profile_id", "project_id"} {
				if id, ok := object[key].(string); ok && alignedOwners[id] {
					aligned = true
				}
			}
			if enabled, ok := object["calendar_aligned"].(bool); ok {
				aligned = aligned || enabled
			}
			for key, child := range object {
				fieldPath := path + "." + key
				if aligned && (key == "reset_duration" || key == "token_reset_duration" || key == "request_reset_duration") {
					duration, ok := child.(string)
					if ok && isMultiUnitConfigDuration(duration) && !seen[fieldPath] {
						seen[fieldPath] = true
						warn("config.json: %s=%q has calendar_aligned enabled; this window uses its full rolling duration. Calendar alignment only applies to 1d, 1w, 1M, 1Q and 1Y", fieldPath, duration)
					}
				}
				visit(child, fieldPath, aligned)
			}
			if aligned {
				// Older and current file formats can reference separately declared windows.
				if id, ok := object["budget_id"].(string); ok {
					visit(budgets[id], path+".budget", true)
				}
				if ids, ok := object["budget_ids"].([]any); ok {
					for i, id := range ids {
						if name, ok := id.(string); ok {
							visit(budgets[name], fmt.Sprintf("%s.budgets[%d]", path, i), true)
						}
					}
				}
				if id, ok := object["rate_limit_id"].(string); ok {
					visit(rates[id], path+".rate_limit", true)
				}
			}
		}
	}
	visit(governance, "governance", false)
	visit(root["access_profiles"], "access_profiles", false)
}

// isMultiUnitConfigDuration recognizes integer multiples without treating months
// as minutes or accepting fractional/compound durations as integer windows.
func isMultiUnitConfigDuration(duration string) bool {
	if len(duration) < 2 || !strings.ContainsRune("smhdwMQY", rune(duration[len(duration)-1])) {
		return false
	}
	prefix := duration[:len(duration)-1]
	for _, digit := range prefix {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	count, err := strconv.ParseUint(prefix, 10, 64)
	return err == nil && count > 1
}

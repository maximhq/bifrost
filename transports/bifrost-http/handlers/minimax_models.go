package handlers

import "strings"

// currentMinimaxSuggestions orders current text models without mutating the
// manager's cached list. Non-text and custom IDs keep their existing order.
func currentMinimaxSuggestions(models []string) []string {
	result := make([]string, 0, len(models))
	for _, preferred := range []string{"MiniMax-M3.1-Flash", "MiniMax-M3"} {
		for _, model := range models {
			if strings.TrimPrefix(model, "minimax/") == preferred {
				result = append(result, model)
			}
		}
	}
	for _, model := range models {
		id := strings.TrimPrefix(model, "minimax/")
		version := strings.TrimPrefix(id, "MiniMax-M")
		isTextFamily := version != id && len(version) > 0 && version[0] >= '0' && version[0] <= '9'
		if isTextFamily || id == "MiniMax-Text-01" || (strings.HasPrefix(id, "abab") && strings.Contains(id, "chat")) {
			continue
		}
		result = append(result, model)
	}
	return result
}

package logging

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// storeLogEntry stores a log entry in BadgerDB with optional indexing
func (p *LoggerPlugin) storeLogEntry(entry *LogEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Serialize the log entry
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal log entry: %w", err)
	}

	return p.db.Update(func(txn *badger.Txn) error {
		// Store the main log entry
		logKey := LogPrefix + entry.ID
		if err := txn.Set([]byte(logKey), data); err != nil {
			return err
		}

		// Create indexes if enabled
		if p.config.IndexingEnabled {
			if err := p.createIndexes(txn, entry); err != nil {
				return err
			}
		}

		return nil
	})
}

// createIndexes creates various indexes for efficient searching
func (p *LoggerPlugin) createIndexes(txn *badger.Txn, entry *LogEntry) error {
	timestamp := entry.Timestamp.Unix()

	// Provider index
	if entry.Provider != "" {
		providerKey := fmt.Sprintf("%s%s%s:%d:%s", IndexPrefix, ProviderIndex, entry.Provider, timestamp, entry.ID)
		if err := txn.Set([]byte(providerKey), []byte(entry.ID)); err != nil {
			return err
		}
	}

	// Model index
	if entry.Model != "" {
		modelKey := fmt.Sprintf("%s%s%s:%d:%s", IndexPrefix, ModelIndex, entry.Model, timestamp, entry.ID)
		if err := txn.Set([]byte(modelKey), []byte(entry.ID)); err != nil {
			return err
		}
	}

	// Timestamp index
	timestampKey := fmt.Sprintf("%s%s%d:%s", IndexPrefix, TimestampIndex, timestamp, entry.ID)
	if err := txn.Set([]byte(timestampKey), []byte(entry.ID)); err != nil {
		return err
	}

	// Status index
	statusKey := fmt.Sprintf("%s%s%s:%d:%s", IndexPrefix, StatusIndex, entry.Status, timestamp, entry.ID)
	if err := txn.Set([]byte(statusKey), []byte(entry.ID)); err != nil {
		return err
	}

	// Latency index (if available)
	if entry.Latency != nil {
		latencyBucket := int(*entry.Latency/100) * 100 // Group by 100ms buckets
		latencyKey := fmt.Sprintf("%s%s%d:%d:%s", IndexPrefix, LatencyIndex, latencyBucket, timestamp, entry.ID)
		if err := txn.Set([]byte(latencyKey), []byte(entry.ID)); err != nil {
			return err
		}
	}

	// Token count index (if available)
	if entry.TokenUsage != nil {
		tokenBucket := entry.TokenUsage.TotalTokens / 100 * 100 // Group by 100 token buckets
		tokenKey := fmt.Sprintf("%s%s%d:%d:%s", IndexPrefix, TokenIndex, tokenBucket, timestamp, entry.ID)
		if err := txn.Set([]byte(tokenKey), []byte(entry.ID)); err != nil {
			return err
		}
	}

	return nil
}

// SearchLogs searches for log entries based on filters and pagination
func (p *LoggerPlugin) SearchLogs(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if pagination == nil {
		pagination = &PaginationOptions{
			Limit:  50,
			Offset: 0,
			SortBy: "timestamp",
			Order:  "desc",
		}
	}

	var matchingIDs []string
	var allLogs []LogEntry
	seenIDs := make(map[string]bool)

	// Statistics variables
	var successfulRequests int64
	var totalLatency float64
	var totalTokens int64
	var logsWithLatency int64

	err := p.db.View(func(txn *badger.Txn) error {
		if p.config.IndexingEnabled && filters != nil {
			// Use indexes for efficient filtering
			matchingIDs = p.searchWithIndexes(txn, filters)
		} else {
			// Fallback to full scan if indexing is disabled
			matchingIDs = p.searchFullScan(txn)
		}

		// Fetch all matching logs, deduplicating by ID
		for _, id := range matchingIDs {
			if !seenIDs[id] {
				if entry, err := p.getLogEntryByID(txn, id); err == nil && p.matchesFilters(entry, filters) {
					allLogs = append(allLogs, *entry)
					seenIDs[id] = true

					// Update statistics
					if entry.Status == "success" {
						successfulRequests++
					}
					if entry.Latency != nil {
						totalLatency += *entry.Latency
						logsWithLatency++
					}
					if entry.TokenUsage != nil {
						totalTokens += int64(entry.TokenUsage.TotalTokens)
					}
				}
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Sort logs based on pagination options
	p.sortLogs(allLogs, pagination.SortBy, pagination.Order)

	// Apply pagination
	total := len(allLogs)
	start := pagination.Offset
	end := min(pagination.Offset+pagination.Limit, total)
	if start > total {
		start = total
	}

	// Calculate final statistics
	var successRate float64
	if total > 0 {
		successRate = float64(successfulRequests) / float64(total) * 100
	}

	var averageLatency float64
	if logsWithLatency > 0 {
		averageLatency = totalLatency / float64(logsWithLatency)
	}

	return &SearchResult{
		Logs:       allLogs[start:end],
		Pagination: *pagination,
		Stats: struct {
			TotalRequests  int64   `json:"total_requests"`
			SuccessRate    float64 `json:"success_rate"`
			AverageLatency float64 `json:"average_latency"`
			TotalTokens    int64   `json:"total_tokens"`
		}{
			TotalRequests:  int64(total),
			SuccessRate:    successRate,
			AverageLatency: averageLatency,
			TotalTokens:    totalTokens,
		},
	}, nil
}

// searchWithIndexes uses indexes to find matching log IDs efficiently
func (p *LoggerPlugin) searchWithIndexes(txn *badger.Txn, filters *SearchFilters) []string {
	var candidateIDs []string
	var hasFilters bool

	// Start with timestamp range if specified
	if filters.StartTime != nil || filters.EndTime != nil {
		candidateIDs = p.searchByTimeRange(txn, filters.StartTime, filters.EndTime)
		hasFilters = true
	}

	// Intersect with other filters
	if len(filters.Providers) > 0 {
		providerIDs := p.searchByProviders(txn, filters.Providers)
		if !hasFilters {
			candidateIDs = providerIDs
			hasFilters = true
		} else {
			candidateIDs = p.intersectIDLists(candidateIDs, providerIDs)
		}
	}

	if len(filters.Models) > 0 {
		modelIDs := p.searchByModels(txn, filters.Models)
		if !hasFilters {
			candidateIDs = modelIDs
			hasFilters = true
		} else {
			candidateIDs = p.intersectIDLists(candidateIDs, modelIDs)
		}
	}

	if len(filters.Status) > 0 {
		statusIDs := p.searchByStatus(txn, filters.Status)
		if !hasFilters {
			candidateIDs = statusIDs
			hasFilters = true
		} else {
			candidateIDs = p.intersectIDLists(candidateIDs, statusIDs)
		}
	}

	// If no filters were applied, return all logs
	if !hasFilters {
		return p.searchFullScan(txn)
	}

	return candidateIDs
}

// searchFullScan performs a full database scan (fallback when indexes are disabled)
func (p *LoggerPlugin) searchFullScan(txn *badger.Txn) []string {
	var matchingIDs []string

	opts := badger.DefaultIteratorOptions
	opts.PrefetchValues = false
	it := txn.NewIterator(opts)
	defer it.Close()

	prefix := []byte(LogPrefix)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		item := it.Item()
		key := string(item.Key())
		id := strings.TrimPrefix(key, LogPrefix)
		matchingIDs = append(matchingIDs, id)
	}

	return matchingIDs
}

// Helper methods for index-based searching
func (p *LoggerPlugin) searchByTimeRange(txn *badger.Txn, startTime, endTime *time.Time) []string {
	var ids []string

	opts := badger.DefaultIteratorOptions
	opts.PrefetchValues = false
	it := txn.NewIterator(opts)
	defer it.Close()

	prefix := []byte(IndexPrefix + TimestampIndex)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		item := it.Item()
		key := string(item.Key())

		// Extract timestamp from key
		parts := strings.Split(strings.TrimPrefix(key, IndexPrefix+TimestampIndex), ":")
		if len(parts) >= 2 {
			if timestamp, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				logTime := time.Unix(timestamp, 0)
				if (startTime == nil || logTime.After(*startTime)) &&
					(endTime == nil || logTime.Before(*endTime)) {
					if err := item.Value(func(val []byte) error {
						ids = append(ids, string(val))
						return nil
					}); err == nil {
						// Continue to next item
					}
				}
			}
		}
	}

	return ids
}

func (p *LoggerPlugin) searchByProviders(txn *badger.Txn, providers []string) []string {
	idMap := make(map[string]bool)

	for _, provider := range providers {
		prefix := []byte(IndexPrefix + ProviderIndex + provider + ":")
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				idMap[string(val)] = true
				return nil
			}); err == nil {
				// Continue
			}
		}
		it.Close()
	}

	// Convert map to slice
	var ids []string
	for id := range idMap {
		ids = append(ids, id)
	}

	return ids
}

func (p *LoggerPlugin) searchByModels(txn *badger.Txn, models []string) []string {
	idMap := make(map[string]bool)

	for _, model := range models {
		prefix := []byte(IndexPrefix + ModelIndex + model + ":")
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				idMap[string(val)] = true
				return nil
			}); err == nil {
				// Continue
			}
		}
		it.Close()
	}

	// Convert map to slice
	var ids []string
	for id := range idMap {
		ids = append(ids, id)
	}

	return ids
}

func (p *LoggerPlugin) searchByStatus(txn *badger.Txn, statuses []string) []string {
	idMap := make(map[string]bool)

	for _, status := range statuses {
		prefix := []byte(IndexPrefix + StatusIndex + status + ":")
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				idMap[string(val)] = true
				return nil
			}); err == nil {
				// Continue
			}
		}
		it.Close()
	}

	// Convert map to slice
	var ids []string
	for id := range idMap {
		ids = append(ids, id)
	}

	return ids
}

// intersectIDLists returns the intersection of two ID lists
func (p *LoggerPlugin) intersectIDLists(list1, list2 []string) []string {
	if len(list1) == 0 {
		return list2
	}
	if len(list2) == 0 {
		return list1
	}

	idMap := make(map[string]bool)
	for _, id := range list1 {
		idMap[id] = true
	}

	var result []string
	for _, id := range list2 {
		if idMap[id] {
			result = append(result, id)
		}
	}

	return result
}

// getLogEntryByID retrieves a log entry by ID
func (p *LoggerPlugin) getLogEntryByID(txn *badger.Txn, id string) (*LogEntry, error) {
	key := LogPrefix + id
	item, err := txn.Get([]byte(key))
	if err != nil {
		return nil, err
	}

	var entry LogEntry
	err = item.Value(func(val []byte) error {
		return json.Unmarshal(val, &entry)
	})

	return &entry, err
}

// matchesFilters checks if a log entry matches the given filters
func (p *LoggerPlugin) matchesFilters(entry *LogEntry, filters *SearchFilters) bool {
	if filters == nil {
		return true
	}

	// Provider filter
	if len(filters.Providers) > 0 {
		found := slices.Contains(filters.Providers, entry.Provider)
		if !found {
			return false
		}
	}

	// Model filter
	if len(filters.Models) > 0 {
		found := false
		for _, model := range filters.Models {
			if entry.Model == model {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Status filter
	if len(filters.Status) > 0 {
		found := slices.Contains(filters.Status, entry.Status)
		if !found {
			return false
		}
	}

	// Object type filter
	if len(filters.Objects) > 0 {
		found := slices.Contains(filters.Objects, entry.Object)
		if !found {
			return false
		}
	}

	// Time range filter
	if filters.StartTime != nil && entry.Timestamp.Before(*filters.StartTime) {
		return false
	}
	if filters.EndTime != nil && entry.Timestamp.After(*filters.EndTime) {
		return false
	}

	// Latency filter
	if entry.Latency != nil {
		if filters.MinLatency != nil && *entry.Latency < *filters.MinLatency {
			return false
		}
		if filters.MaxLatency != nil && *entry.Latency > *filters.MaxLatency {
			return false
		}
	}

	// Token count filter
	if entry.TokenUsage != nil {
		if filters.MinTokens != nil && entry.TokenUsage.TotalTokens < *filters.MinTokens {
			return false
		}
		if filters.MaxTokens != nil && entry.TokenUsage.TotalTokens > *filters.MaxTokens {
			return false
		}
	}

	// Content search
	if filters.ContentSearch != "" {
		searchTerm := strings.ToLower(filters.ContentSearch)
		found := false

		// Search in input history
		for _, msg := range entry.InputHistory {
			if msg.Content.ContentStr != nil &&
				strings.Contains(strings.ToLower(*msg.Content.ContentStr), searchTerm) {
				found = true
				break
			}
		}

		// Search in input text
		if !found && entry.InputText != nil &&
			strings.Contains(strings.ToLower(*entry.InputText), searchTerm) {
			found = true
		}

		// Search in output message
		if !found && entry.OutputMessage != nil && entry.OutputMessage.Content.ContentStr != nil &&
			strings.Contains(strings.ToLower(*entry.OutputMessage.Content.ContentStr), searchTerm) {
			found = true
		}

		if !found {
			return false
		}
	}

	return true
}

// sortLogs sorts log entries based on the specified criteria
func (p *LoggerPlugin) sortLogs(logs []LogEntry, sortBy, order string) {
	sort.Slice(logs, func(i, j int) bool {
		var less bool

		switch sortBy {
		case "latency":
			latencyI := float64(0)
			latencyJ := float64(0)
			if logs[i].Latency != nil {
				latencyI = *logs[i].Latency
			}
			if logs[j].Latency != nil {
				latencyJ = *logs[j].Latency
			}
			less = latencyI < latencyJ
		case "tokens":
			tokensI := 0
			tokensJ := 0
			if logs[i].TokenUsage != nil {
				tokensI = logs[i].TokenUsage.TotalTokens
			}
			if logs[j].TokenUsage != nil {
				tokensJ = logs[j].TokenUsage.TotalTokens
			}
			less = tokensI < tokensJ
		default: // timestamp
			less = logs[i].Timestamp.Before(logs[j].Timestamp)
		}

		if order == "desc" {
			return !less
		}
		return less
	})
}

// updateStats updates the plugin statistics
func (p *LoggerPlugin) updateStats(entry *LogEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stats.TotalRequests++

	if entry.Status == "success" {
		p.stats.SuccessfulRequests++
	} else {
		p.stats.FailedRequests++
	}

	if entry.Provider != "" {
		p.stats.ProviderStats[entry.Provider]++
	}

	if entry.Model != "" {
		p.stats.ModelStats[entry.Model]++
	}

	if entry.Latency != nil {
		// Update average latency
		totalLatency := p.stats.AverageLatency * float64(p.stats.TotalRequests-1)
		p.stats.AverageLatency = (totalLatency + *entry.Latency) / float64(p.stats.TotalRequests)
	}

	if entry.TokenUsage != nil {
		p.stats.TotalTokens += int64(entry.TokenUsage.TotalTokens)
	}

	p.stats.LastUpdated = time.Now()

	// Persist stats
	p.saveStats()
}

// loadStats loads statistics from the database
func (p *LoggerPlugin) loadStats() {
	p.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(StatsPrefix + "general"))
		if err != nil {
			return err // Stats don't exist yet
		}

		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, p.stats)
		})
	})
}

// saveStats saves statistics to the database
func (p *LoggerPlugin) saveStats() {
	data, err := json.Marshal(p.stats)
	if err != nil {
		return
	}

	p.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(StatsPrefix+"general"), data)
	})
}

// GetStats returns the current statistics (public API)
func (p *LoggerPlugin) GetStats() *LogStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Return a copy to prevent external modification
	statsCopy := &LogStats{
		TotalRequests:      p.stats.TotalRequests,
		SuccessfulRequests: p.stats.SuccessfulRequests,
		FailedRequests:     p.stats.FailedRequests,
		ProviderStats:      make(map[string]int64),
		ModelStats:         make(map[string]int64),
		AverageLatency:     p.stats.AverageLatency,
		TotalTokens:        p.stats.TotalTokens,
		LastUpdated:        p.stats.LastUpdated,
	}

	maps.Copy(statsCopy.ProviderStats, p.stats.ProviderStats)
	maps.Copy(statsCopy.ModelStats, p.stats.ModelStats)

	return statsCopy
}

// LogManager defines the main interface that combines all logging functionality
type LogManager interface {
	// Search searches for log entries based on filters and pagination
	Search(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error)
	// GetStats returns current statistics
	GetStats() *LogStats
}

type PluginLogManager struct {
	plugin *LoggerPlugin
}

func (p *PluginLogManager) Search(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error) {
	return p.plugin.SearchLogs(filters, pagination)
}

func (p *PluginLogManager) GetStats() *LogStats {
	return p.plugin.GetStats()
}

func (p *LoggerPlugin) GetPluginLogManager() *PluginLogManager {
	return &PluginLogManager{
		plugin: p,
	}
}

// Package logstore provides a logs store for Bifrost.
package logstore

// Config represents the configuration for the logs store.
type Config struct {
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"`
	Config  any    `json:"config"`
}

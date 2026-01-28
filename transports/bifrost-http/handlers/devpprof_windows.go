//go:build windows
// +build windows

package handlers

import "time"

// getCPUSample returns a zeroed CPU sample on Windows
// Windows does not support syscall.Getrusage
func getCPUSample() cpuSample {
	return cpuSample{timestamp: time.Now()}
}

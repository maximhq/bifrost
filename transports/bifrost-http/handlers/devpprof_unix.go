//go:build dev && !windows

package handlers

import (
	"syscall"
	"time"
)

// getCPUSample gets the current CPU time sample using syscall
func getCPUSample() cpuSample {
	var rusage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &rusage); err != nil {
		return cpuSample{timestamp: time.Now()}
	}

	userTime := time.Duration(rusage.Utime.Sec)*time.Second + time.Duration(rusage.Utime.Usec)*time.Microsecond
	systemTime := time.Duration(rusage.Stime.Sec)*time.Second + time.Duration(rusage.Stime.Usec)*time.Microsecond

	return cpuSample{
		timestamp:  time.Now(),
		userTime:   userTime,
		systemTime: systemTime,
	}
}


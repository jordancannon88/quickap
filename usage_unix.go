//go:build unix

package main

import (
	"runtime"
	"syscall"
	"time"
)

// procUsage reports the process's total CPU time (user + system) and
// peak resident set size via getrusage.
func procUsage() (cpu time.Duration, peakRSS int64, ok bool) {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0, 0, false
	}
	cpu = time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	peakRSS = int64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		peakRSS *= 1024 // ru_maxrss is KB on Linux and the BSDs, bytes on macOS
	}
	return cpu, peakRSS, true
}

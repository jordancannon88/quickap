//go:build !unix

package main

import "time"

// procUsage is unavailable here; the -verbose footer falls back to the
// Go runtime's heap figure.
func procUsage() (cpu time.Duration, peakRSS int64, ok bool) {
	return 0, 0, false
}

//go:build !windows

package upstreamcacheguard

import "time"

var benchmarkClockStart = time.Now()

func benchmarkClockNS() int64 { return time.Since(benchmarkClockStart).Nanoseconds() }

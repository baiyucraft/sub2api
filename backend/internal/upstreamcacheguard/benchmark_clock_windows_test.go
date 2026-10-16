//go:build windows

package upstreamcacheguard

import (
	"syscall"
	"unsafe"
)

var benchmarkCounter = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceCounter")
var benchmarkFrequency = func() int64 {
	var value int64
	syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&value)))
	return value
}()

// Windows wall-clock resolution can round short measured operations to zero.
// QPC supplies a monotonic high-resolution counter for benchmark samples only.
func benchmarkClockNS() int64 {
	var value int64
	benchmarkCounter.Call(uintptr(unsafe.Pointer(&value)))
	return (value / benchmarkFrequency * 1e9) + (value % benchmarkFrequency * 1e9 / benchmarkFrequency)
}

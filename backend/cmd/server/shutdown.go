package main

import "github.com/Wei-Shaw/sub2api/internal/service"

// PrepareShutdownFunc is kept distinct from the general Cleanup func so Wire
// can inject both callbacks into Application without an ambiguous type.
type PrepareShutdownFunc func()

// providePrepareShutdown creates the pre-drain hook used by main before the
// HTTP server stops accepting requests. It only quiesces active probes; the
// regular cleanup function still performs the final worker drain.
func providePrepareShutdown(runner *service.ChannelMonitorRunner) PrepareShutdownFunc {
	return PrepareShutdownFunc(func() {
		if runner != nil {
			runner.Quiesce()
		}
	})
}

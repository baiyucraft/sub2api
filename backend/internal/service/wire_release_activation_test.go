package service

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStartReleaseActivatedTaskStartsImmediatelyWithoutGate(t *testing.T) {
	t.Setenv("SUB2API_BACKGROUND_ACTIVATION_FILE", "")
	t.Setenv("SUB2API_INSTANCE_ID", "")
	var calls atomic.Int32
	startReleaseActivatedTask(func() { calls.Add(1) })
	require.Equal(t, int32(1), calls.Load())
}

func TestStartReleaseActivatedTaskWaitsForMatchingInstance(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	t.Setenv("SUB2API_BACKGROUND_ACTIVATION_FILE", activationFile)
	t.Setenv("SUB2API_INSTANCE_ID", "candidate-1")
	var calls atomic.Int32
	startReleaseActivatedTask(func() { calls.Add(1) })
	require.NoError(t, os.WriteFile(activationFile, []byte("old-instance\n"), 0o600))
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, calls.Load())
	require.NoError(t, os.WriteFile(activationFile, []byte("candidate-1\n"), 0o600))
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, 20*time.Millisecond)
}

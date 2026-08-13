package service

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReleaseActivationControllerStartsImmediatelyWithoutGate(t *testing.T) {
	controller := &releaseActivationController{}
	var calls atomic.Int32
	controller.register(func() error { calls.Add(1); return nil })
	controller.closeRegistration()
	require.Equal(t, int32(1), calls.Load())
	require.True(t, controller.ready.Load())
}

func TestReleaseActivationControllerWaitsForRegistrationAndMatchingInstance(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	controller := &releaseActivationController{
		activationFile: activationFile,
		instanceID:     "candidate-1",
	}
	var calls atomic.Int32
	controller.register(func() error { calls.Add(1); return nil })
	require.NoError(t, os.WriteFile(activationFile, []byte("candidate-1\n"), 0o600))
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, calls.Load())
	require.False(t, controller.ready.Load())
	controller.closeRegistration()
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, 20*time.Millisecond)
	require.True(t, controller.ready.Load())
}

func TestReleaseActivationControllerIgnoresOtherInstance(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	controller := &releaseActivationController{
		activationFile: activationFile,
		instanceID:     "candidate-1",
	}
	var calls atomic.Int32
	controller.register(func() error { calls.Add(1); return nil })
	controller.closeRegistration()
	require.NoError(t, os.WriteFile(activationFile, []byte("old-instance\n"), 0o600))
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, calls.Load())
	require.False(t, controller.ready.Load())
}

func TestReleaseActivationControllerDoesNotReportReadyOnStartupFailure(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	controller := &releaseActivationController{
		activationFile: activationFile,
		instanceID:     "candidate-1",
	}
	controller.register(func() error { return errors.New("startup failed") })
	controller.closeRegistration()
	require.NoError(t, os.WriteFile(activationFile, []byte("candidate-1\n"), 0o600))
	time.Sleep(1100 * time.Millisecond)
	require.False(t, controller.ready.Load())
	require.Len(t, controller.tasks, 1)
}

func TestReleaseActivationControllerDoesNotReportReadyOnStartupPanic(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	controller := &releaseActivationController{
		activationFile: activationFile,
		instanceID:     "candidate-1",
	}
	controller.register(func() error { panic("startup panic") })
	controller.closeRegistration()
	require.NoError(t, os.WriteFile(activationFile, []byte("candidate-1\n"), 0o600))
	require.Eventually(t, controller.failed.Load, 2*time.Second, 20*time.Millisecond)
	require.False(t, controller.ready.Load())
}

func TestReleaseActivationControllerUngatedFailureStaysNotReady(t *testing.T) {
	controller := &releaseActivationController{}
	controller.register(func() error { return errors.New("startup failed") })
	controller.closeRegistration()
	require.True(t, controller.failed.Load())
	require.False(t, controller.ready.Load())
}

func TestReleaseActivationControllerStartsPreactivationTaskWhileGateIsClosed(t *testing.T) {
	controller := &releaseActivationController{
		activationFile: filepath.Join(t.TempDir(), "active-instance"),
		instanceID:     "candidate-1",
	}
	var calls atomic.Int32
	controller.startBeforeActivation(func() error {
		calls.Add(1)
		return nil
	})
	controller.closeRegistration()

	require.Equal(t, int32(1), calls.Load())
	require.False(t, controller.ready.Load(), "pre-activation work must not activate the remaining background tasks")
}

func TestReleaseActivationControllerPreactivationFailureBlocksReadiness(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	controller := &releaseActivationController{
		activationFile: activationFile,
		instanceID:     "candidate-1",
	}
	controller.startBeforeActivation(func() error { return errors.New("snapshot failed") })
	controller.closeRegistration()
	require.NoError(t, os.WriteFile(activationFile, []byte("candidate-1\n"), 0o600))

	require.Eventually(t, func() bool { return controller.failed.Load() }, 2*time.Second, 20*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.False(t, controller.ready.Load() && !controller.failed.Load())
}

func TestReleaseActivationControllerBlockingAsyncTaskDoesNotBlockCheckedReadiness(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	controller := &releaseActivationController{
		activationFile: activationFile,
		instanceID:     "candidate-1",
	}
	blockingStarted := make(chan struct{})
	checkedCalled := make(chan struct{})
	controller.registerAsync(func() error {
		close(blockingStarted)
		select {}
	})
	controller.register(func() error {
		close(checkedCalled)
		return nil
	})
	controller.closeRegistration()
	require.NoError(t, os.WriteFile(activationFile, []byte("candidate-1\n"), 0o600))
	require.Eventually(t, func() bool { return controller.ready.Load() }, 2*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		select {
		case <-blockingStarted:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
	select {
	case <-checkedCalled:
	default:
		t.Fatal("checked activation task was not called")
	}
}

func TestReleaseActivationControllerAsyncPanicRevokesReadiness(t *testing.T) {
	activationFile := filepath.Join(t.TempDir(), "active-instance")
	controller := &releaseActivationController{
		activationFile: activationFile,
		instanceID:     "candidate-1",
	}
	controller.registerAsync(func() error { panic("startup panic") })
	controller.closeRegistration()
	require.NoError(t, os.WriteFile(activationFile, []byte("candidate-1\n"), 0o600))
	require.Eventually(t, controller.failed.Load, 2*time.Second, 20*time.Millisecond)
	require.False(t, controller.ready.Load() && !controller.failed.Load())
}

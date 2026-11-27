package spread

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mockDirectSSHProbe(t *testing.T, probe func(context.Context, string, time.Duration) bool) {
	t.Helper()
	oldProbe := probeDirectSSH
	probeDirectSSH = probe
	t.Cleanup(func() {
		probeDirectSSH = oldProbe
	})
}

func TestWaitReachableDirectSSHImmediateSuccess(t *testing.T) {
	mockDirectSSHProbe(t, func(context.Context, string, time.Duration) bool {
		return true
	})

	reachable, err := waitReachableDirectSSH(context.Background(), "192.0.2.1", time.Second, time.Millisecond)
	if err != nil || !reachable {
		t.Fatalf("expected direct SSH to be reachable, got reachable=%v err=%v", reachable, err)
	}
}

func TestWaitReachableDirectSSHRetriesUntilReady(t *testing.T) {
	attempts := 0
	mockDirectSSHProbe(t, func(context.Context, string, time.Duration) bool {
		attempts++
		return attempts == 3
	})

	reachable, err := waitReachableDirectSSH(context.Background(), "192.0.2.1", time.Second, time.Millisecond)
	if err != nil || !reachable {
		t.Fatalf("expected direct SSH to become reachable, got reachable=%v err=%v", reachable, err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 probes, got %d", attempts)
	}
}

func TestWaitReachableDirectSSHTimeout(t *testing.T) {
	mockDirectSSHProbe(t, func(context.Context, string, time.Duration) bool {
		return false
	})

	reachable, err := waitReachableDirectSSH(context.Background(), "192.0.2.1", 10*time.Millisecond, time.Millisecond)
	if err != nil || reachable {
		t.Fatalf("expected direct SSH timeout, got reachable=%v err=%v", reachable, err)
	}
}

func TestWaitReachableDirectSSHCancellation(t *testing.T) {
	mockDirectSSHProbe(t, func(context.Context, string, time.Duration) bool {
		return false
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reachable, err := waitReachableDirectSSH(ctx, "192.0.2.1", time.Second, time.Millisecond)
	if reachable || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got reachable=%v err=%v", reachable, err)
	}
}

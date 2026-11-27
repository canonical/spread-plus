package main

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunExecWithoutSocket(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(workingDir); err != nil {
			t.Errorf("cannot restore working directory: %v", err)
		}
	}()

	socket := ".spread-reuse.missing.machine.socket"
	err = runExec([]string{socket, "--", "pwd"})
	if err == nil {
		t.Fatal("expected an error when the exec socket does not exist")
	}
	if !strings.Contains(err.Error(), "cannot connect to debug socket") {
		t.Fatalf("unexpected error %q", err)
	}
	if err := runContinue([]string{socket}); err == nil || !strings.Contains(err.Error(), "cannot connect to debug socket") {
		t.Fatalf("expected spread continue to report a missing debug socket, got %v", err)
	}
}

func TestRunExecRequiresCommand(t *testing.T) {
	err := runExec([]string{"pwd"})
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if got, want := err.Error(), "usage: spread exec SOCKET -- COMMAND"; got != want {
		t.Fatalf("unexpected error %q, want %q", got, want)
	}
}

func TestRunContinueRequiresSocket(t *testing.T) {
	err := runContinue(nil)
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if got, want := err.Error(), "usage: spread continue SOCKET"; got != want {
		t.Fatalf("unexpected error %q, want %q", got, want)
	}
}

func TestStopOnSignal(t *testing.T) {
	t.Run("SIGTERM starts cleanup", func(t *testing.T) {
		stopped := false
		exitCode := stopOnSignal(syscall.SIGTERM, nil, func() error {
			stopped = true
			return nil
		}, time.Second)
		if exitCode != 0 {
			t.Fatalf("unexpected exit code: %d", exitCode)
		}
		if !stopped {
			t.Fatal("runner stop was not called for SIGTERM")
		}
	})

	t.Run("completed stop", func(t *testing.T) {
		exitCode := stopOnSignal(os.Interrupt, nil, func() error { return nil }, time.Second)
		if exitCode != 0 {
			t.Fatalf("unexpected exit code: %d", exitCode)
		}
	})

	for _, test := range []struct {
		name     string
		signal   os.Signal
		wantCode int
	}{
		{name: "second SIGINT", signal: os.Interrupt, wantCode: 130},
		{name: "SIGTERM during stop", signal: syscall.SIGTERM, wantCode: 128 + int(syscall.SIGTERM)},
	} {
		t.Run(test.name, func(t *testing.T) {
			signals := make(chan os.Signal, 1)
			stopStarted := make(chan struct{})
			releaseStop := make(chan struct{})
			done := make(chan int, 1)
			go func() {
				done <- stopOnSignal(os.Interrupt, signals, func() error {
					close(stopStarted)
					<-releaseStop
					return nil
				}, time.Second)
			}()

			<-stopStarted
			signals <- test.signal
			if exitCode := <-done; exitCode != test.wantCode {
				t.Fatalf("unexpected exit code: %d", exitCode)
			}
			close(releaseStop)
		})
	}

	t.Run("cleanup timeout", func(t *testing.T) {
		releaseStop := make(chan struct{})
		defer close(releaseStop)
		exitCode := stopOnSignal(os.Interrupt, nil, func() error {
			<-releaseStop
			return nil
		}, 10*time.Millisecond)
		if exitCode != 130 {
			t.Fatalf("unexpected exit code: %d", exitCode)
		}
	})

	t.Run("SIGTERM cleanup timeout", func(t *testing.T) {
		releaseStop := make(chan struct{})
		defer close(releaseStop)
		exitCode := stopOnSignal(syscall.SIGTERM, nil, func() error {
			<-releaseStop
			return nil
		}, 10*time.Millisecond)
		if exitCode != 128+int(syscall.SIGTERM) {
			t.Fatalf("unexpected exit code: %d", exitCode)
		}
	})
}

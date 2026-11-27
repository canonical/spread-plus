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
	t.Run("initial SIGTERM exits without cleanup", func(t *testing.T) {
		cleanupStarted := false
		exitCode := stopOnSignal(syscall.SIGTERM, nil, nil, func() error {
			cleanupStarted = true
			return nil
		})
		if exitCode != 128+int(syscall.SIGTERM) {
			t.Fatalf("unexpected exit code: %d", exitCode)
		}
		if cleanupStarted {
			t.Fatal("cleanup started after initial SIGTERM")
		}
	})

	t.Run("completed stop", func(t *testing.T) {
		exitCode := stopOnSignal(os.Interrupt, nil, nil, func() error { return nil })
		if exitCode != 0 {
			t.Fatalf("unexpected exit code: %d", exitCode)
		}
	})

	for _, test := range []struct {
		name     string
		signal   os.Signal
		wantCode int
	}{
		{name: "repeated SIGINT forces exit during cleanup", signal: os.Interrupt, wantCode: 130},
		{name: "SIGTERM forces exit during cleanup", signal: syscall.SIGTERM, wantCode: 128 + int(syscall.SIGTERM)},
	} {
		t.Run(test.name, func(t *testing.T) {
			interrupts := make(chan os.Signal, 1)
			terminations := make(chan os.Signal, 1)
			stopStarted := make(chan struct{})
			releaseStop := make(chan struct{})
			defer func() {
				select {
				case <-releaseStop:
				default:
					close(releaseStop)
				}
			}()
			done := make(chan int, 1)
			go func() {
				done <- stopOnSignal(os.Interrupt, interrupts, terminations, func() error {
					close(stopStarted)
					<-releaseStop
					return nil
				})
			}()

			<-stopStarted
			if test.signal == syscall.SIGTERM {
				terminations <- test.signal
			} else {
				interrupts <- test.signal
			}
			if exitCode := <-done; exitCode != test.wantCode {
				t.Fatalf("unexpected exit code: %d", exitCode)
			}
		})
	}

	t.Run("SIGINT waits until cleanup completes", func(t *testing.T) {
		interrupts := make(chan os.Signal, 1)
		terminations := make(chan os.Signal, 1)
		releaseStop := make(chan struct{})
		release := func() {
			select {
			case <-releaseStop:
			default:
				close(releaseStop)
			}
		}
		defer release()
		stopStarted := make(chan struct{})
		done := make(chan int, 1)
		go func() {
			done <- stopOnSignal(os.Interrupt, interrupts, terminations, func() error {
				close(stopStarted)
				<-releaseStop
				return nil
			})
		}()

		<-stopStarted
		select {
		case exitCode := <-done:
			t.Fatalf("stop returned before cleanup completed: %d", exitCode)
		case <-time.After(20 * time.Millisecond):
		}

		release()
		if exitCode := <-done; exitCode != 0 {
			t.Fatalf("unexpected exit code: %d", exitCode)
		}
	})
}

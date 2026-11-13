package main

import (
	"os"
	"strings"
	"testing"
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

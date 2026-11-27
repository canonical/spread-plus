package spread

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalScriptStopKillsProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	stop := make(chan struct{})
	stopped := false
	defer func() {
		if !stopped {
			close(stop)
		}
	}()

	script := fmt.Sprintf("echo $$ > %q\nwhile :; do sleep 0.05; done", pidFile)
	local := localScript{
		script: script,
		dir:    dir,
		env:    NewEnvironment(),
		mode:   traceOutput,
		stop:   stop,
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := local.run()
		done <- err
	}()

	var pid int
	deadline := time.After(time.Second)
	for pid == 0 {
		select {
		case <-deadline:
			t.Fatal("local script did not start")
		case <-time.After(10 * time.Millisecond):
			data, err := os.ReadFile(pidFile)
			if err == nil {
				pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil {
					t.Fatalf("invalid local script PID: %v", err)
				}
			}
		}
	}

	close(stop)
	stopped = true
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected interrupted script to return an error")
		}
	case <-time.After(time.Second):
		t.Fatal("local script did not stop promptly")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("local script process %d still exists: %v", pid, err)
	}
}

package spread

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAttachCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attach.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	requests := make(chan attachRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request attachRequest
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		requests <- request
		json.NewEncoder(conn).Encode(attachResponse{Output: []byte("command output"), ExitCode: 7})
	}()

	output, exitCode, err := AttachCommand(path, "journalctl -u snapd")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "command output" {
		t.Fatalf("unexpected output %q", output)
	}
	if exitCode != 7 {
		t.Fatalf("unexpected exit status %d", exitCode)
	}
	if request := <-requests; request.Command != "journalctl -u snapd" {
		t.Fatalf("unexpected command %q", request.Command)
	}
}

func TestAttachBrokerSocketPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attach.sock")
	broker, err := StartDebugSocketBroker(path, nil, "", NewEnvironment())
	if err != nil {
		t.Fatal(err)
	}

	socketInfo, err := os.Stat(broker.Path())
	if err != nil {
		t.Fatal(err)
	}
	if socketInfo.Mode().Perm() != 0600 {
		t.Fatalf("unexpected socket permissions %o", socketInfo.Mode().Perm())
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected attach socket to be removed, got %v", err)
	}
}

func TestStopDebugSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.sock")
	broker, err := StartDebugSocketBroker(path, nil, "", NewEnvironment())
	if err != nil {
		t.Fatal(err)
	}

	if err := StopDebugSocket(path); err != nil {
		t.Fatal(err)
	}
	select {
	case <-broker.StopRequested():
	default:
		t.Fatal("expected debug socket stop request")
	}

	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected debug socket to be removed, got %v", err)
	}
}

func TestDebugSocketSessionsWaitForPreviousSession(t *testing.T) {
	runner := &Runner{project: &Project{Path: t.TempDir()}}
	path := runner.attachPath()
	done := make(chan struct{}, 2)

	startSession := func() {
		go func() {
			runner.runDebugSocket(nil, "", NewEnvironment())
			done <- struct{}{}
		}()
	}
	waitForSocket := func() {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("debug socket %q was not created", path)
	}

	startSession()
	waitForSocket()
	startSession()
	select {
	case <-done:
		t.Fatal("second debug session returned before the first session stopped")
	case <-time.After(100 * time.Millisecond):
	}

	if err := StopDebugSocket(path); err != nil {
		t.Fatal(err)
	}
	<-done
	waitForSocket()
	if err := StopDebugSocket(path); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestStartRejectsDebugSocketWithInteractiveModes(t *testing.T) {
	tests := []struct {
		name    string
		options *Options
	}{
		{name: "debug", options: &Options{DebugSocket: true, Debug: true}},
		{name: "shell", options: &Options{DebugSocket: true, Shell: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Start(nil, test.options); err == nil {
				t.Fatal("expected incompatible debug-socket mode error")
			}
		})
	}
}

func TestWorkerCountsIncludesConcurrentSystems(t *testing.T) {
	firstSystem := &System{Name: "first", Workers: 1}
	secondSystem := &System{Name: "second", Workers: 1}
	backend := &Backend{
		Name: "backend",
		Systems: map[string]*System{
			"first":  firstSystem,
			"second": secondSystem,
		},
	}
	runner := &Runner{
		project: &Project{Backends: map[string]*Backend{"backend": backend}},
		pending: []*Job{
			{Backend: backend, System: firstSystem},
			{Backend: backend, System: secondSystem},
		},
	}

	_, count := runner.workerCounts()
	if count != 2 {
		t.Fatalf("unexpected worker count %d, want 2", count)
	}
}

func TestWorkerCountsCapsAtSelectedJobs(t *testing.T) {
	system := &System{Name: "system", Workers: 4}
	backend := &Backend{Name: "backend", Systems: map[string]*System{"system": system}}
	runner := &Runner{
		project: &Project{Backends: map[string]*Backend{"backend": backend}},
		pending: []*Job{{Backend: backend, System: system}},
	}

	_, count := runner.workerCounts()
	if count != 1 {
		t.Fatalf("unexpected worker count %d, want 1", count)
	}
}

func TestParseAgentCommandOutput(t *testing.T) {
	output, exitCode, err := parseAgentCommandOutput(
		[]byte("command output\n\n__SPREAD_ATTACH_EXIT_token__=7\n"),
		"__SPREAD_ATTACH_EXIT_token__=",
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "command output\n" {
		t.Fatalf("unexpected output %q", output)
	}
	if exitCode != 7 {
		t.Fatalf("unexpected exit status %d", exitCode)
	}
}

func TestParseAgentCommandOutputRejectsInvalidStatus(t *testing.T) {
	_, _, err := parseAgentCommandOutput(
		[]byte("__SPREAD_ATTACH_EXIT_token__=256\n"),
		"__SPREAD_ATTACH_EXIT_token__=",
	)
	if err == nil {
		t.Fatal("expected invalid exit status error")
	}
}

func TestParseAgentCommandOutputWithoutTrailingNewline(t *testing.T) {
	output, exitCode, err := parseAgentCommandOutput(
		[]byte("command output\n\n__SPREAD_ATTACH_EXIT_token__=0"),
		"__SPREAD_ATTACH_EXIT_token__=",
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "command output\n" {
		t.Fatalf("unexpected output %q", output)
	}
	if exitCode != 0 {
		t.Fatalf("unexpected exit status %d", exitCode)
	}
}

func TestAttachPathIsFixedInProjectRoot(t *testing.T) {
	projectPath := t.TempDir()
	runner := &Runner{project: &Project{Path: projectPath}}
	if got, want := runner.attachPath(), filepath.Join(projectPath, AttachSocketName); got != want {
		t.Fatalf("unexpected attach path %q, want %q", got, want)
	}
}

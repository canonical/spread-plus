package spread

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type blockingAgentCommandRunner struct {
	started  chan struct{}
	finished chan struct{}
}

func (r *blockingAgentCommandRunner) AgentCommandContext(ctx context.Context, _, _ string, _ *Environment) ([]byte, int, error) {
	close(r.started)
	defer close(r.finished)
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

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
	broker.listener.SetUnlinkOnClose(false)

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

func TestDebugSocketCloseReportsRemovalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.sock")
	broker, err := StartDebugSocketBroker(path, nil, "", NewEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	broker.listener.SetUnlinkOnClose(false)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	err = broker.Close()
	if err == nil || !strings.Contains(err.Error(), "cannot remove debug socket") {
		t.Fatalf("expected socket removal error, got %v", err)
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

func TestDebugSocketSessionsUseSeparateSockets(t *testing.T) {
	projectPath, err := os.MkdirTemp(".", "debug-socket-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(projectPath); err != nil {
			t.Errorf("cannot remove temporary project directory: %v", err)
		}
	})
	runner := &Runner{runID: "test-run", project: &Project{Path: projectPath}}
	secondRun := &Runner{runID: "other-run", project: runner.project}
	firstPath, err := runner.attachPath("machine one")
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := runner.attachPath("machine two")
	if err != nil {
		t.Fatal(err)
	}
	thirdPath, err := secondRun.attachPath("machine one")
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath || firstPath == thirdPath || secondPath == thirdPath {
		t.Fatal("separate debug sessions produced the same attach socket path")
	}
	done := make(chan struct{}, 3)

	startSession := func(runner *Runner, machineName, path string) {
		go func() {
			runner.runDebugSocketAtPath(path, nil, machineName, "", NewEnvironment())
			done <- struct{}{}
		}()
	}
	waitForSocket := func(path string) {
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

	startSession(runner, "machine one", firstPath)
	startSession(runner, "machine two", secondPath)
	startSession(secondRun, "machine one", thirdPath)
	waitForSocket(firstPath)
	waitForSocket(secondPath)
	waitForSocket(thirdPath)

	if err := StopDebugSocket(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := StopDebugSocket(secondPath); err != nil {
		t.Fatal(err)
	}
	if err := StopDebugSocket(thirdPath); err != nil {
		t.Fatal(err)
	}
	<-done
	<-done
	<-done
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("expected first debug socket to be removed, got %v", err)
	}
	if _, err := os.Stat(secondPath); !os.IsNotExist(err) {
		t.Fatalf("expected second debug socket to be removed, got %v", err)
	}
	if _, err := os.Stat(thirdPath); !os.IsNotExist(err) {
		t.Fatalf("expected third debug socket to be removed, got %v", err)
	}
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

func TestBrokerCloseCancelsInFlightCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.sock")
	broker, err := StartDebugSocketBroker(path, nil, "", NewEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	commandRunner := &blockingAgentCommandRunner{
		started:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	broker.client = commandRunner

	commandDone := make(chan error, 1)
	go func() {
		_, _, err := AttachCommand(path, "long-running-command")
		commandDone <- err
	}()
	select {
	case <-commandRunner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent command did not start")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- broker.Close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker close waited for the in-flight command")
	}
	select {
	case <-commandRunner.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("broker close did not cancel the in-flight command")
	}
	select {
	case <-commandDone:
	case <-time.After(2 * time.Second):
		t.Fatal("exec client did not return after broker close")
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

func TestAttachPathUsesRunMachineAndSocketIDs(t *testing.T) {
	projectPath := t.TempDir()
	runner := &Runner{runID: "test-run", project: &Project{Path: projectPath}}
	firstPath, err := runner.attachPath("machine one")
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := runner.attachPath("machine one")
	if err != nil {
		t.Fatal(err)
	}
	firstName := filepath.Base(firstPath)
	if !strings.HasPrefix(firstName, ".spread-") || !strings.HasSuffix(firstName, ".socket") {
		t.Fatalf("unexpected attach socket path %q", firstPath)
	}
	if !strings.Contains(firstName, "test-run-machine_one-") {
		t.Fatalf("attach socket path does not include the run and machine names: %q", firstPath)
	}
	if firstPath == secondPath {
		t.Fatal("separate debug sessions produced the same attach socket path")
	}
}

func TestNewSocketIDIsUnique(t *testing.T) {
	first, err := newSocketID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSocketID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("separate runs received the same ID")
	}
	if len(first) != 16 || len(second) != 16 {
		t.Fatalf("unexpected socket ID lengths: %d and %d", len(first), len(second))
	}
}

func TestAttachPathBoundsLongProjectAndMachineNames(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), strings.Repeat("p", maxUnixSocketPathLen*2))
	if err := os.MkdirAll(projectPath, 0700); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{runID: "test-run", project: &Project{Path: projectPath}}
	firstPath, err := runner.attachPath(strings.Repeat("machine", 100) + "-one")
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := runner.attachPath(strings.Repeat("machine", 100) + "-two")
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPath) >= maxUnixSocketPathLen {
		t.Fatalf("attach socket path is too long: %d bytes", len(firstPath))
	}
	if firstPath == secondPath {
		t.Fatal("long machine labels produced the same attach socket path")
	}

	broker, err := StartDebugSocketBroker(firstPath, nil, "", NewEnvironment())
	if err != nil {
		t.Fatalf("cannot create bounded attach socket: %v", err)
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
}

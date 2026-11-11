package spread

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
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
	broker, err := StartAttachBroker(path, nil, "", NewEnvironment())
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

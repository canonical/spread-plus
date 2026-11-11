package spread

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxAttachRequestSize = 1 << 20

// AttachSocketName is the local socket used by spread exec.
const AttachSocketName = ".spread.socket"

type attachRequest struct {
	Command string
	Stop    bool
}

type attachResponse struct {
	Output   []byte
	ExitCode int
	Error    string
}

// AttachBroker serves agent commands over a local socket using the worker's SSH credentials.
type AttachBroker struct {
	client    *Client
	dir       string
	env       *Environment
	path      string
	listener  *net.UnixListener
	done      chan struct{}
	stop      chan struct{}
	stopOnce  sync.Once
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	handlers  sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// StartDebugSocketBroker opens a permission-restricted socket that can resume a paused run.
func StartDebugSocketBroker(path string, client *Client, dir string, env *Environment) (*AttachBroker, error) {
	if path == "" {
		return nil, fmt.Errorf("attach socket path cannot be empty")
	}
	if env == nil {
		env = NewEnvironment()
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("cannot access attach socket directory: %v", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("attach socket parent is not a directory: %s", filepath.Dir(path))
	}
	address := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, fmt.Errorf("cannot create attach socket %s: %v", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		closeErr := listener.Close()
		return nil, errors.Join(
			fmt.Errorf("cannot restrict attach socket permissions: %v", err),
			closeErr,
		)
	}

	broker := &AttachBroker{
		client:   client,
		dir:      dir,
		env:      env.Copy(),
		path:     path,
		listener: listener,
		done:     make(chan struct{}),
		stop:     make(chan struct{}),
		conns:    make(map[net.Conn]struct{}),
	}
	go broker.serve()
	return broker, nil
}

// Path returns the local socket path used by attach clients.
func (b *AttachBroker) Path() string {
	return b.path
}

// StopRequested is closed when a client asks to resume the paused debug run.
func (b *AttachBroker) StopRequested() <-chan struct{} {
	return b.stop
}

// Close stops accepting commands, waits for active handlers, and removes the socket.
func (b *AttachBroker) Close() error {
	b.closeOnce.Do(func() {
		if err := b.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			b.closeErr = fmt.Errorf("cannot close attach socket: %v", err)
		}
		<-b.done

		b.mu.Lock()
		for conn := range b.conns {
			conn.Close()
		}
		b.mu.Unlock()
		b.handlers.Wait()

	})
	return b.closeErr
}

// serve accepts local attach requests and dispatches each one to a handler.
func (b *AttachBroker) serve() {
	defer close(b.done)
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				printf("Error accepting agent attach connection: %v", err)
			}
			return
		}
		b.mu.Lock()
		b.conns[conn] = struct{}{}
		b.handlers.Add(1)
		b.mu.Unlock()
		go b.handle(conn)
	}
}

// handle runs one requested command on the worker over a separate SSH connection.
func (b *AttachBroker) handle(conn net.Conn) {
	defer b.handlers.Done()
	defer func() {
		b.mu.Lock()
		delete(b.conns, conn)
		b.mu.Unlock()
		conn.Close()
	}()

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		printf("Error setting agent attach request deadline: %v", err)
		return
	}
	var request attachRequest
	if err := json.NewDecoder(io.LimitReader(conn, maxAttachRequestSize)).Decode(&request); err != nil {
		writeAttachResponse(conn, attachResponse{Error: fmt.Sprintf("invalid attach request: %v", err)})
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		printf("Error clearing agent attach request deadline: %v", err)
		return
	}
	if request.Stop {
		writeAttachResponse(conn, attachResponse{})
		b.stopOnce.Do(func() { close(b.stop) })
		return
	}
	if request.Command == "" {
		writeAttachResponse(conn, attachResponse{Error: "debug socket command cannot be empty"})
		return
	}

	output, exitCode, err := b.client.AgentCommand(request.Command, b.dir, b.env.Copy())
	response := attachResponse{Output: output, ExitCode: exitCode}
	if err != nil {
		response.Error = err.Error()
	}
	writeAttachResponse(conn, response)
}

// writeAttachResponse sends a command result to the local attach client.
func writeAttachResponse(conn net.Conn, response attachResponse) {
	if err := json.NewEncoder(conn).Encode(response); err != nil {
		printf("Error writing agent attach response: %v", err)
	}
}

// AttachCommand submits one command to the broker and returns its output and status.
func AttachCommand(path, command string) ([]byte, int, error) {
	response, err := sendAttachRequest(path, attachRequest{Command: command})
	return response.Output, response.ExitCode, err
}

// StopDebugSocket asks a paused debug-socket session to resume.
func StopDebugSocket(path string) error {
	_, err := sendAttachRequest(path, attachRequest{Stop: true})
	return err
}

func sendAttachRequest(path string, request attachRequest) (attachResponse, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return attachResponse{}, fmt.Errorf("cannot connect to debug socket: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return attachResponse{}, fmt.Errorf("cannot send debug socket request: %v", err)
	}
	var response attachResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return attachResponse{}, fmt.Errorf("cannot read debug socket response: %v", err)
	}
	if response.Error != "" {
		return response, fmt.Errorf("%s", response.Error)
	}
	return response, nil
}

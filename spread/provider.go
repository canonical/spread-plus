package spread

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"regexp"

	"golang.org/x/net/context"
)

var rnd = rand.New(rand.NewSource(time.Now().UnixNano()))

type Provider interface {
	Backend() *Backend
	Allocate(ctx context.Context, system *System) (Server, error)
	Reuse(ctx context.Context, rsystem *ReuseSystem, system *System) (Server, error)
	GarbageCollect() error
}

type Server interface {
	Provider() Provider
	Address() string
	Discard(ctx context.Context) error
	ReuseData() interface{}
	SerialOutput() (string, error)
	System() *System
	Label() string
	String() string
}

// FatalError represents an error that cannot be fixed by just retrying.
type FatalError struct{ error }

var (
	labelTimeLayout = "15:04Jan2"
	labelTimeExp    = regexp.MustCompile("[0-9]{1,2}:[0-5][0-9][A-Z][a-z][a-z][0-9]{1,2}")
)

func SystemLabel(system *System, note string) string {
	if note != "" {
		note = " (" + note + ")"
	}
	tstr := time.Now().UTC().Format(labelTimeLayout)
	return fmt.Sprintf("%s %s%s", system.Name, tstr, note)
}

func ParseLabelTime(s string) (time.Time, error) {
	t, err := time.Parse(labelTimeLayout, labelTimeExp.FindString(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot find timestamp in label: %s", s)
	}

	now := time.Now()
	t = t.AddDate(now.Year(), 0, 0)
	if t.After(now) {
		t = t.AddDate(-1, 0, 0)
	}
	return t, nil
}

type UnknownServer struct {
	Addr string
}

func removedSystem(backend *Backend, sysname string) *System {
	return &System{
		Backend: backend.Name,
		Name:    sysname,
		Image:   sysname,
	}
}

func saveLog(dir string, filename string, output []byte) error {
	path := filename
	if dir != "" {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			err := os.MkdirAll(dir, 0755)
			if err != nil {
				return fmt.Errorf("failed to create logs dir: %v", err)
			}
		}
		path = filepath.Join(dir, filename)
	}

	// create the log file.
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cannot create log file: %v", err)
	}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()

	// Build the output to write the log file
	var buffer bytes.Buffer
	buffer.Write(output)

	// write the output into the log file.
	_, err = f.Write(buffer.Bytes())
	if err != nil {
		return fmt.Errorf("cannot write output to file: %v", err)
	}

	return nil
}

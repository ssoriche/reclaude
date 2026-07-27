// Package exec is the single place reclaude shells out to external binaries.
package exec

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Runner runs an external command and returns its stdout.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OSRunner is the production Runner backed by os/exec.
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

// ErrNoFakeResponse is returned by FakeRunner for an unregistered command.
var ErrNoFakeResponse = errors.New("exec: no fake response registered for command")

// FakeResponse is the canned result for one command line.
type FakeResponse struct {
	Stdout []byte
	Err    error
}

// FakeRunner is a test double keyed by the full "name arg arg ..." command line.
type FakeRunner struct {
	Responses map[string]FakeResponse
	Calls     []string
}

func (f *FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	// Join without trimming: payloads like send-text end in "\n" and must be
	// matched verbatim. No trailing space is produced when args is empty.
	key := strings.Join(append([]string{name}, args...), " ")
	f.Calls = append(f.Calls, key)
	resp, ok := f.Responses[key]
	if !ok {
		return nil, ErrNoFakeResponse
	}
	return resp.Stdout, resp.Err
}

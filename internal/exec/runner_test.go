package exec

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRunnerReturnsCannedOutput(t *testing.T) {
	f := &FakeRunner{
		Responses: map[string]FakeResponse{
			"wezterm cli list --format json": {Stdout: []byte("[]")},
		},
	}
	out, err := f.Run(context.Background(), "wezterm", "cli", "list", "--format", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "[]" {
		t.Fatalf("got %q, want %q", out, "[]")
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	f := &FakeRunner{Responses: map[string]FakeResponse{"ps -axo pid,tty,command": {}}}
	_, _ = f.Run(context.Background(), "ps", "-axo", "pid,tty,command")
	if len(f.Calls) != 1 || f.Calls[0] != "ps -axo pid,tty,command" {
		t.Fatalf("calls not recorded: %v", f.Calls)
	}
}

func TestFakeRunnerUnknownCommandErrors(t *testing.T) {
	f := &FakeRunner{Responses: map[string]FakeResponse{}}
	_, err := f.Run(context.Background(), "nope")
	if !errors.Is(err, ErrNoFakeResponse) {
		t.Fatalf("want ErrNoFakeResponse, got %v", err)
	}
}

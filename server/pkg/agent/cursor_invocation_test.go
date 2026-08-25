package agent

import (
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
)

type invocationChooser func(string, string, []string, *slog.Logger) (string, []string)

func assertInvocationPassthrough(t *testing.T, execName string, args []string, choose invocationChooser) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lookedUp := filepath.Join(t.TempDir(), execName)
	gotExec, gotArgs := choose(execName, lookedUp, args, logger)

	if gotExec != execName {
		t.Errorf("argv0 changed unexpectedly: got %q want %q", gotExec, execName)
	}
	if !reflect.DeepEqual(gotArgs, args) {
		t.Errorf("argv changed unexpectedly:\n got  %#v\n want %#v", gotArgs, args)
	}
}

func TestChooseCursorInvocation_PassthroughForNonLauncher(t *testing.T) {
	assertInvocationPassthrough(t, "cursor-agent", []string{
		"-p", "hello\nworld", "--output-format", "stream-json", "--yolo",
	}, chooseCursorInvocation)
}

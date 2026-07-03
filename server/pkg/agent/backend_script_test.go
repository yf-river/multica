package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func executeBackendScript(t *testing.T, provider, executableName, script string, opts ExecOptions, configure ...func(*Config)) Result {
	t.Helper()

	fakePath := filepath.Join(t.TempDir(), executableName)
	writeTestExecutable(t, fakePath, []byte(script))

	cfg := Config{ExecutablePath: fakePath, Logger: slog.Default()}
	for _, fn := range configure {
		fn(&cfg)
	}
	backend, err := New(provider, cfg)
	if err != nil {
		t.Fatalf("new %s backend: %v", provider, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := backend.Execute(ctx, "prompt-ignored", opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()

	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
		return Result{}
	}
}

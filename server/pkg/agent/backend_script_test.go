package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func assertFilteredArgsPreserveLayerOrder(t *testing.T, args []string) {
	t.Helper()

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--output-format text") || strings.Contains(joined, "--permission-mode plan") {
		t.Fatalf("blocked args should be filtered from both layers: %v", args)
	}

	extraIdx, customIdx := -1, -1
	for i := 0; i+1 < len(args); i++ {
		switch {
		case args[i] == "--max-budget-usd" && args[i+1] == "1.00":
			extraIdx = i
		case args[i] == "--max-budget-usd" && args[i+1] == "2.00":
			customIdx = i
		}
	}
	if extraIdx == -1 || customIdx == -1 || extraIdx > customIdx {
		t.Fatalf("expected extra args before custom args, got %v", args)
	}
}

func assertACPModelFailure(t *testing.T, result Result, sessionID string) {
	t.Helper()

	if result.Status != "failed" {
		t.Fatalf("expected status=failed, got %q (error=%q)", result.Status, result.Error)
	}
	if !strings.Contains(result.Error, `could not switch to model "bogus-model"`) {
		t.Errorf("expected error to name the requested model, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "model not available") {
		t.Errorf("expected error to surface upstream message, got %q", result.Error)
	}
	if result.SessionID != sessionID {
		t.Errorf("expected session id %q to be preserved on failure, got %q", sessionID, result.SessionID)
	}
}

func readTestLines(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return splitTestLines(string(raw))
}

func splitTestLines(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func argIndex(args []string, target string) int {
	for i, arg := range args {
		if arg == target {
			return i
		}
	}
	return -1
}

func argValue(args []string, flag string) string {
	i := argIndex(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func argCount(args []string, target string) int {
	count := 0
	for _, arg := range args {
		if arg == target {
			count++
		}
	}
	return count
}

func assertStaleSessionModelFailure(t *testing.T, result Result, model string) {
	t.Helper()

	if result.Status != "failed" {
		t.Fatalf("expected status=failed, got %q (error=%q)", result.Status, result.Error)
	}
	if !strings.Contains(result.Error, `could not switch to model "`+model+`"`) {
		t.Errorf("expected error to name model %q, got %q", model, result.Error)
	}
	if result.SessionID != "" {
		t.Errorf("expected empty session id so the fresh-session retry can run, got %q", result.SessionID)
	}
}

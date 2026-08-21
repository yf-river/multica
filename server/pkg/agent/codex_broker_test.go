package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexBrokerReusesAppServerForSequentialTurns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	dir := t.TempDir()
	countFile := filepath.Join(dir, "starts.txt")
	fakePath := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		`echo start >> "$COUNT_FILE"` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'` + "\n" +
		`read line` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thr-one"}}}'` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":3,"result":{}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"thr-one","turn":{"id":"turn-one"}}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-one","item":{"type":"agentMessage","id":"msg-one","text":"first"}}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr-one","turn":{"id":"turn-one","status":"completed"}}}'` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":4,"result":{"thread":{"id":"thr-two"}}}'` + "\n" +
		`read line` + "\n" +
		`echo '{"jsonrpc":"2.0","id":5,"result":{}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"thr-two","turn":{"id":"turn-two"}}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr-two","item":{"type":"agentMessage","id":"msg-two","text":"second"}}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr-two","turn":{"id":"turn-two","status":"completed"}}}'` + "\n" +
		`while read line; do :; done` + "\n"
	if err := os.WriteFile(fakePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	backend := NewCodexBrokerBackend(Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"COUNT_FILE": countFile},
	})
	t.Cleanup(func() { backend.restartProcess("test cleanup") })

	res1 := runCodexBrokerTestTurn(t, backend, "one")
	if res1.Status != "completed" || res1.Output != "first" || res1.SessionID != "thr-one" {
		t.Fatalf("first result = %+v", res1)
	}
	res2 := runCodexBrokerTestTurn(t, backend, "two")
	if res2.Status != "completed" || res2.Output != "second" || res2.SessionID != "thr-two" {
		t.Fatalf("second result = %+v", res2)
	}

	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read count file: %v", err)
	}
	if got := strings.Count(string(data), "start"); got != 1 {
		t.Fatalf("app-server starts = %d, want 1; file=%q", got, string(data))
	}
}

func runCodexBrokerTestTurn(t *testing.T, backend *CodexBrokerBackend, prompt string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, prompt, ExecOptions{SemanticInactivityTimeout: time.Second})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range session.Messages {
	}
	select {
	case result := <-session.Result:
		return result
	case <-ctx.Done():
		t.Fatalf("result timeout: %v", ctx.Err())
		return Result{}
	}
}

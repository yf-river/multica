package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexExecutePreservesBurstToolResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	const count = 700
	output := strings.Repeat("evidence中文", 15000)
	var events strings.Builder
	for i := 0; i < count; i++ {
		value := "ok"
		if i == count-1 {
			value = output
		}
		line, err := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thr-burst", "item": map[string]any{"type": "commandExecution", "id": fmt.Sprint(i), "command": "test", "aggregatedOutput": value, "exitCode": 0, "status": "completed"}}})
		if err != nil {
			t.Fatal(err)
		}
		events.Write(line)
		events.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(events.String()), 0600); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeCodexAppServer(t, "read line\necho '{\"id\":1,\"result\":{}}'\nread line\nread line\necho '{\"id\":2,\"result\":{\"thread\":{\"id\":\"thr-burst\"}}}'\nread line\necho '{\"id\":3,\"result\":{}}'\ncat '"+path+"'\necho '{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thr-burst\",\"turn\":{\"id\":\"turn-1\",\"status\":\"completed\"}}}'\n")
	backend, err := New("codex", Config{ExecutablePath: fake, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(context.Background(), "test", ExecOptions{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// 消费者稍后接收，覆盖两层有界通道都已装满的情况。
	time.Sleep(200 * time.Millisecond)
	results := 0
	var last string
	for msg := range session.Messages {
		if msg.Type == MessageToolResult {
			results++
			last = msg.Output
		}
	}
	result := <-session.Result
	if result.Status != "completed" || results != count || last != output {
		t.Fatalf("status=%s results=%d output bytes=%d want=%d", result.Status, results, len(last), len(output))
	}
}

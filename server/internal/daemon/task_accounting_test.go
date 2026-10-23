package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// 看门狗测试观察发出取消的时刻，独立于 backend 末结果和消息收尾预算。
type cancellationObservedBackend struct {
	inner       agent.Backend
	cancelledAt chan<- time.Time
}

func (b cancellationObservedBackend) Execute(ctx context.Context, prompt string, opts agent.ExecOptions) (*agent.Session, error) {
	go func() { <-ctx.Done(); b.cancelledAt <- time.Now() }()
	return b.inner.Execute(ctx, prompt, opts)
}

type cancellationTailBackend struct {
	started chan struct{}
	stop    <-chan struct{}
}

func (b cancellationTailBackend) Execute(ctx context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	messages := make(chan agent.Message)
	results := make(chan agent.Result, 1)
	close(b.started)
	go func() {
		<-ctx.Done()
		time.Sleep(15 * time.Millisecond)
		// 无缓冲尾消息必须仍被消费，backend才能发送末Result。
		for n := 0; n < 32; n++ {
			select {
			case messages <- agent.Message{Type: agent.MessageText, Content: "cancel tail\n"}:
			case <-b.stop:
				close(messages)
				close(results)
				return
			}
		}
		close(messages)
		results <- agent.Result{Status: "completed", SessionID: "observed-session", ProviderTraceID: "observed-trace", Usage: map[string]agent.TokenUsage{"observed-model": {InputTokens: 123, OutputTokens: 7}}}
		close(results)
	}()
	return &agent.Session{Messages: messages, Result: results}, nil
}

func TestExecuteAndDrainCancellationRetainsTailAccounting(t *testing.T) {
	t.Parallel()
	d, rec := newTranscriptRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	backend := cancellationTailBackend{started: make(chan struct{}), stop: stop}
	type reply struct {
		result agent.Result
		err    error
	}
	returned := make(chan reply, 1)
	go func() {
		r, _, err := d.executeAndDrain(ctx, backend, "p", agent.ExecOptions{}, slog.Default(), "cancel-accounting", "", new(atomic.Int32))
		returned <- reply{r, err}
	}()
	<-backend.started
	cancel()
	select {
	case got := <-returned:
		if got.err != nil || got.result.Status != "cancelled" || got.result.SessionID != "observed-session" || got.result.ProviderTraceID != "observed-trace" || got.result.Usage["observed-model"].InputTokens != 123 {
			t.Fatalf("取消丢失已观测结果或错误复活: %+v err=%v", got.result, got.err)
		}
		var text strings.Builder
		for _, m := range rec.snapshot() {
			text.WriteString(m.Content)
		}
		if strings.Count(text.String(), "cancel tail") != 32 {
			t.Fatalf("取消收尾消息未完整冲刷: %q", text.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("配合取消的backend未及时退出；可能被尾消息堵塞")
	}
	if d.runningTasks.Load() != 0 {
		t.Fatal("取消后未释放执行计数")
	}
}

func TestExecuteAndDrainCancellationResultRaceKeepsAccounting(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"cancelled", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			d, _ := newTranscriptRecorder(t)
			var ctx context.Context
			var cancel context.CancelFunc
			if kind == "cancelled" {
				ctx, cancel = context.WithCancel(context.Background())
			} else {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			for n := 0; n < 12; n++ {
				messages := make(chan agent.Message)
				close(messages)
				results := make(chan agent.Result, 1)
				results <- agent.Result{Status: "completed", SessionID: "race-session", ProviderTraceID: "race-trace", Usage: map[string]agent.TokenUsage{"race": {InputTokens: 9}}}
				close(results)
				got, _, err := d.executeAndDrain(ctx, sessionBackend{&agent.Session{Messages: messages, Result: results}}, "p", agent.ExecOptions{}, slog.Default(), "race", "", new(atomic.Int32))
				if err != nil || got.Status != kind || got.SessionID != "race-session" || got.ProviderTraceID != "race-trace" || got.Usage["race"].InputTokens != 9 {
					t.Fatalf("竞争丢结果/错误成功: %+v %v", got, err)
				}
			}
		})
	}
}

func TestExecuteAndDrainCancellationUncooperativeBackendIsBounded(t *testing.T) {
	t.Parallel()
	d, _ := newTranscriptRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	got, _, err := d.executeAndDrain(ctx, sessionBackend{&agent.Session{Messages: make(chan agent.Message), Result: make(chan agent.Result)}}, "p", agent.ExecOptions{}, slog.Default(), "uncooperative", "", new(atomic.Int32))
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("%+v %v", got, err)
	}
	if elapsed := time.Since(start); elapsed > interruptedResultGrace+time.Second {
		t.Fatalf("无末结果取消超时: %v", elapsed)
	}
	if d.runningTasks.Load() != 0 {
		t.Fatal("未释放执行计数")
	}
}

func TestReportTaskAccountingSurvivesCancelledExecutionContext(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer daemon-accounting-fixture" {
			t.Error("未使用daemon原鉴权")
		}
		if r.Context().Err() != nil {
			t.Error("请求错误继承取消")
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if strings.HasSuffix(r.URL.Path, "/usage") {
			var usage []TaskUsageEntry
			_ = json.Unmarshal(payload["usage"], &usage)
			if len(usage) != 1 || usage[0].InputTokens != 123 {
				t.Errorf("错误用量: %+v", usage)
			}
		} else {
			var traces []TaskTraceEntry
			_ = json.Unmarshal(payload["traces"], &traces)
			if len(traces) != 1 || traces[0].SessionID != "observed-session" || traces[0].ProviderTraceID != "observed-trace" {
				t.Errorf("错误session/trace: %+v", traces)
			}
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	d := &Daemon{client: NewClient(srv.URL)}
	d.client.token = "daemon-accounting-fixture"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.reportTaskAccounting(ctx, "actual-cancelled-task", "codebuddy", TaskResult{Usage: []TaskUsageEntry{{Provider: "codebuddy", Model: "observed-model", InputTokens: 123}}, ProviderTraceIDs: []ProviderTrace{{SessionID: "observed-session", TraceID: "observed-trace"}}}, slog.Default())
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != "/api/daemon/tasks/actual-cancelled-task/usage" || paths[1] != "/api/daemon/tasks/actual-cancelled-task/traces" {
		t.Fatalf("取消ctx导致记账丢失: %v", paths)
	}
}

func TestReportTaskAccountingSharedDeadlineBoundsHungHTTP(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 必须先读完body，才能观察客户端关闭的HTTP请求上下文。
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		<-r.Context().Done()
	}))
	defer srv.Close()
	d := &Daemon{client: NewClient(srv.URL)}
	start := time.Now()
	d.reportTaskAccounting(context.Background(), "hung-task", "codebuddy", TaskResult{Usage: []TaskUsageEntry{{Model: "m", InputTokens: 1}}, ProviderTraceIDs: []ProviderTrace{{TraceID: "t"}}}, slog.Default())
	if elapsed := time.Since(start); elapsed > taskAccountingTimeout+time.Second {
		t.Fatalf("两类上报未共用总预算: %v", elapsed)
	}
}

func TestExecuteAndDrainCancellationClosedResultChannel(t *testing.T) {
	t.Parallel()
	d, _ := newTranscriptRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	messages := make(chan agent.Message)
	close(messages)
	results := make(chan agent.Result)
	close(results)
	started := time.Now()
	result, _, err := d.executeAndDrain(ctx, sessionBackend{&agent.Session{Messages: messages, Result: results}}, "p", agent.ExecOptions{}, slog.Default(), "closed", "", new(atomic.Int32))
	if err != nil || result.Status != "cancelled" || result.SessionID != "" || len(result.Usage) != 0 || time.Since(started) > time.Second {
		t.Fatalf("无末结果provider取消未正确退出: %+v %v", result, err)
	}
}

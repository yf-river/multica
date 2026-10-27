//go:build agentintegration

package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCodebuddyRealResidentPreparationComparison(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_STARTUP_BENCHMARK") != "1" {
		t.Skip("five cold and three warm real model calls are opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	executable, home, destination := runtime.Path, runtime.Home, os.Getenv("MULTICA_CODEBUDDY_POOL_BENCH_RESULT")
	actualModel := os.Getenv("MULTICA_CODEBUDDY_BENCH_ACTUAL_MODEL")
	if executable == "" || home == "" || destination == "" || actualModel == "" {
		t.Fatal("explicit CLI, isolated account home, actual model ID and private result path required")
	}
	type sample struct {
		Cold          bool                  `json:"cold"`
		PID           int                   `json:"pid"`
		Reused        bool                  `json:"reused"`
		PreparationMs int64                 `json:"preparation_and_handshake_ms"`
		DurationMs    int64                 `json:"duration_ms"`
		WallMs        int64                 `json:"wall_ms"`
		ReadyMs       int64                 `json:"wall_to_ready_ms"`
		Tokens        int64                 `json:"observed_tokens"`
		Usage         map[string]TokenUsage `json:"usage"`
		SessionID     string                `json:"session_id"`
	}
	var samples []sample
	var pool *CodebuddyProcessPool
	defer func() {
		if pool != nil {
			pool.Close()
		}
	}()
	work, sid, warmPID := t.TempDir(), "", 0
	for index := 0; index < 8; index++ {
		cold := index < 5
		if cold {
			if pool != nil {
				pool.Close()
			}
			var err error
			pool, err = NewCodebuddyProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			sid = ""
		}
		marker, err := newCodebuddyGeneration()
		if err != nil {
			t.Fatal(err)
		}
		cfg := Config{ExecutablePath: executable, BuiltinRuntime: true, CLIVersion: runtime.Version, Logger: slog.Default(), CodebuddyPool: pool, ResidentSessionKey: "owned-startup-comparison", TaskID: fmt.Sprintf("startup-%d", index), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": filepath.Join(home, "codebuddy"), "MULTICA_TASK_ID": fmt.Sprintf("startup-%d", index)}}
		started := time.Now()
		session, err := (&codebuddyBackend{cfg: cfg}).Execute(t.Context(), "Reply with exactly this marker: "+marker, ExecOptions{Cwd: work, Model: "deepseek-v4.1-flash", ResumeSessionID: sid, SystemPrompt: "Reply only with the marker requested in the current prompt. Do not use tools or delegate.", Timeout: 80 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		entry := sample{Cold: cold, ReadyMs: -1}
		observedReady := false
		tools := 0
		for msg := range session.Messages {
			if msg.Type == MessageToolUse {
				tools++
			}
			if msg.Type == MessageStatus && msg.Status == "running" && entry.ReadyMs < 0 {
				entry.ReadyMs = time.Since(started).Milliseconds()
			}
			if msg.Type != MessageRuntime {
				continue
			}
			var ready struct {
				Kind        string `json:"kind"`
				Phase       string `json:"phase"`
				PID         int    `json:"pid"`
				Reused      bool   `json:"reused"`
				Preparation int64  `json:"preparation_and_handshake_ms"`
			}
			if json.Unmarshal([]byte(msg.Content), &ready) == nil && ready.Kind == "process" && ready.Phase == "ready" {
				entry.PID, entry.Reused, entry.PreparationMs = ready.PID, ready.Reused, ready.Preparation
				observedReady = true
			}
		}
		result := <-session.Result
		entry.WallMs, entry.Usage, entry.SessionID = time.Since(started).Milliseconds(), result.Usage, result.SessionID
		_, expectedModel := result.Usage[actualModel]
		if result.Status != "completed" || strings.TrimSpace(result.Output) != marker || !expectedModel || !observedReady || tools != 0 || entry.ReadyMs < 0 {
			t.Fatalf("startup sample %d failed: status=%s error=%s output_matches=%v actual_model_matches=%v usage=%v ready=%v tools=%d", index, result.Status, result.Error, strings.TrimSpace(result.Output) == marker, expectedModel, result.Usage, observedReady, tools)
		}
		if result.SessionID == "" || !cold && (entry.PID != warmPID || !entry.Reused || result.SessionID != sid) || cold && entry.Reused {
			t.Fatal("startup comparison did not prove actual native process identity")
		}
		actualExecutable, err := os.Stat(fmt.Sprintf("/proc/%d/exe", entry.PID))
		if err != nil {
			t.Fatal(err)
		}
		expectedExecutable, err := os.Stat(executable)
		if err != nil || !os.SameFile(actualExecutable, expectedExecutable) {
			t.Fatal("native process executable differs from the pinned CLI")
		}
		sid, warmPID, entry.DurationMs = result.SessionID, entry.PID, result.DurationMs
		for _, usage := range result.Usage {
			entry.Tokens += usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		}
		samples = append(samples, entry)
		raw, _ := json.MarshalIndent(map[string]any{"runtime": "codebuddy", "cli": runtime.Version, "executable_sha256": runtime.SHA256, "model": "deepseek-v4.1-flash", "expected_actual_model": actualModel, "case": "text marker; actual model inference", "samples": samples}, "", "  ")
		if err := os.WriteFile(destination, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("sample=%d cold=%v pid=%d reused=%v prep_ms=%d duration_ms=%d observed_tokens=%d", index, cold, entry.PID, entry.Reused, entry.PreparationMs, entry.DurationMs, entry.Tokens)
	}
	median := func(cold bool, value func(sample) int64) float64 {
		var values []int64
		for _, entry := range samples {
			if entry.Cold == cold {
				values = append(values, value(entry))
			}
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		return float64(values[len(values)/2])
	}
	prepRatio := median(false, func(s sample) int64 { return s.PreparationMs }) / median(true, func(s sample) int64 { return s.PreparationMs })
	timeRatio := median(false, func(s sample) int64 { return s.DurationMs }) / median(true, func(s sample) int64 { return s.DurationMs })
	tokenRatio := median(false, func(s sample) int64 { return s.Tokens }) / median(true, func(s sample) int64 { return s.Tokens })
	wallRatio := median(false, func(s sample) int64 { return s.WallMs }) / median(true, func(s sample) int64 { return s.WallMs })
	t.Logf("warm/cold median: preparation=%.3f duration=%.3f wall=%.3f observed_tokens=%.3f", prepRatio, timeRatio, wallRatio, tokenRatio)
	if prepRatio > .8 || timeRatio > 1.1 || wallRatio > 1.1 || tokenRatio > 1.1 {
		t.Fatalf("startup comparison thresholds not met: preparation=%.3f duration=%.3f wall=%.3f tokens=%.3f", prepRatio, timeRatio, wallRatio, tokenRatio)
	}
}

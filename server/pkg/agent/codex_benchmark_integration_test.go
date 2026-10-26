//go:build agentintegration && linux

package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 分别记录初始化握手和完整线程准备，不用某个更快的子步骤替代整体启动时间。
func TestCodexRealResidentPreparationComparison(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_STARTUP_BENCHMARK") != "1" {
		t.Skip("five cold and three warm model calls are opt-in")
	}
	executable, account, destination := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME"), os.Getenv("MULTICA_CODEX_POOL_BENCH_RESULT")
	nativePath := os.Getenv("MULTICA_CODEX_BENCH_NATIVE_PATH")
	if executable == "" || account == "" || destination == "" || nativePath == "" {
		t.Fatal("explicit CLI, native executable, isolated account metadata and private result path required")
	}
	nativeFile, err := os.Stat(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	version, err := DetectVersion(t.Context(), NewCommand(executable, nil))
	if err != nil || !CodexResidentSupported(version) {
		t.Fatalf("native runtime version: %s %v", version, err)
	}
	nativeVersion, err := DetectVersion(t.Context(), NewCommand(nativePath, nil))
	if err != nil || nativeVersion != version {
		t.Fatalf("native executable version differs from CLI launcher: native=%s launcher=%s error=%v", nativeVersion, version, err)
	}
	// 原生会话记录随结果保留，便于核查逐轮上下文和 usage；账号材料仅写入私有目录。
	artifacts, err := os.MkdirTemp(filepath.Dir(destination), "native-")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(artifacts, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	// 工作目录在仓库之外，避免把仓库的 Agent 指令计入启动探针。
	work, err := os.MkdirTemp("", "multica-owned-codex-benchmark-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "work-path.txt"), []byte(work), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.toml", "auth.json", "models_cache.json", "config.json", "instructions.md"} {
		raw, err := os.ReadFile(filepath.Join(account, name))
		if os.IsNotExist(err) && (name == "config.json" || name == "instructions.md") {
			continue
		}
		if err != nil {
			t.Fatalf("read isolated metadata %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(home, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 同一账号的已安装插件和技能必须相同；空插件目录会触发异步安装，使热样本
	// 额外收到目录更新。复制而非链接，避免测试写回原账号缓存。
	for _, name := range []string{"skills", "plugins", "rules"} {
		source := filepath.Join(account, name)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(home, name), os.DirFS(source)); err != nil {
			t.Fatal(err)
		}
	}
	type sample struct {
		Cold          bool                  `json:"cold"`
		PID           int                   `json:"pid"`
		NativePID     int                   `json:"native_pid"`
		Reused        bool                  `json:"reused"`
		InitializeMs  int64                 `json:"initialize_ms"`
		ThreadMs      int64                 `json:"thread_setup_ms"`
		PreparationMs int64                 `json:"preparation_and_handshake_ms"`
		DurationMs    int64                 `json:"duration_ms"`
		WallMs        int64                 `json:"wall_ms"`
		Tokens        int64                 `json:"observed_tokens"`
		Usage         map[string]TokenUsage `json:"usage"`
		ThreadID      string                `json:"thread_id"`
	}
	var samples []sample
	var pool *CodexProcessPool
	defer func() {
		if pool != nil {
			pool.Close()
		}
	}()
	sid, warmPID, warmNativePID, executableSHA := "", 0, 0, ""
	for index := 0; index < 8; index++ {
		cold := index < 5
		if cold {
			if pool != nil {
				pool.Close()
			}
			pool, err = NewCodexProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			sid = ""
		}
		logPath := filepath.Join(filepath.Dir(destination), fmt.Sprintf("codex-startup-%d.jsonl", index))
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer logFile.Close()
		logger := slog.New(slog.NewJSONHandler(logFile, nil))
		cfg := Config{ExecutablePath: executable, BuiltinRuntime: true, CodexVersion: version, Logger: logger, TaskID: fmt.Sprintf("owned-startup-%d", index), CodexPool: pool, ResidentSessionKey: "owned-startup-comparison", Env: map[string]string{"CODEX_HOME": home, "MULTICA_TASK_ID": fmt.Sprintf("owned-startup-%d", index)}}
		marker := rand.Text()
		started := time.Now()
		session, err := (&codexBackend{cfg: cfg}).Execute(t.Context(), "Do not use tools or delegate. Output exactly the following marker, with no punctuation or explanation:\n"+marker, ExecOptions{Cwd: work, Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default", ResumeSessionID: sid, Timeout: 80 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		tools := 0
		for message := range session.Messages {
			if message.Type == MessageToolUse {
				tools++
			}
		}
		result := <-session.Result
		_, expectedModel := result.Usage["gpt-6.1-sol"]
		if result.Status != "completed" || strings.TrimSpace(result.Output) != marker || !expectedModel || tools != 0 {
			t.Fatalf("sample=%d status=%s error=%s output_matches=%v tools=%d models=%d", index, result.Status, result.Error, strings.TrimSpace(result.Output) == marker, tools, len(result.Usage))
		}
		entry := sample{Cold: cold, DurationMs: result.DurationMs, WallMs: time.Since(started).Milliseconds(), Usage: result.Usage, ThreadID: result.SessionID, InitializeMs: -1, ThreadMs: -1, PreparationMs: -1}
		if _, err := logFile.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(logFile)
		for {
			var event struct {
				Time      time.Time `json:"time"`
				Message   string    `json:"msg"`
				Phase     string    `json:"phase"`
				PID       int       `json:"launcher_pid"`
				Reused    bool      `json:"reused"`
				Latency   string    `json:"latency"`
				LatencyMs int64     `json:"latency_ms"`
			}
			if err := decoder.Decode(&event); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if event.Message == "codex resident lease" {
				entry.PID, entry.Reused = event.PID, event.Reused
			}
			if event.Phase == "initialize_response" {
				latency, err := time.ParseDuration(event.Latency)
				if err != nil {
					t.Fatal(err)
				}
				entry.InitializeMs = latency.Milliseconds()
			}
			if event.Phase == "thread_start_response" || event.Phase == "thread_resume_response" {
				entry.ThreadMs = event.LatencyMs
			}
			if event.Message == "codex turn/start sent" {
				entry.PreparationMs = event.Time.Sub(started).Milliseconds()
			}
		}
		if entry.PID == 0 || entry.InitializeMs < 0 || entry.ThreadMs < 0 || entry.PreparationMs < 0 {
			t.Fatal("native identity or preparation evidence missing")
		}
		if cold && entry.Reused || !cold && (!entry.Reused || entry.PID != warmPID || result.SessionID != sid) {
			t.Fatal("warm sample did not retain the actual process and thread")
		}
		entry.NativePID = codexBenchmarkNativePID(t, entry.PID, nativeFile)
		if !cold && entry.NativePID != warmNativePID {
			t.Fatal("warm sample replaced the native executable behind its launcher")
		}
		binary, err := os.Open(fmt.Sprintf("/proc/%d/exe", entry.NativePID))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.New()
		_, hashErr := io.Copy(digest, binary)
		closeErr := binary.Close()
		if hashErr != nil || closeErr != nil {
			t.Fatalf("hash native executable: %v %v", hashErr, closeErr)
		}
		actualSHA := hex.EncodeToString(digest.Sum(nil))
		if executableSHA != "" && executableSHA != actualSHA {
			t.Fatal("actual native binary changed across samples")
		}
		executableSHA = actualSHA
		for _, usage := range result.Usage {
			entry.Tokens += usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		}
		sid, warmPID, warmNativePID = result.SessionID, entry.PID, entry.NativePID
		samples = append(samples, entry)
		raw, _ := json.MarshalIndent(map[string]any{"runtime": "codex", "cli": version, "executable_sha256": executableSHA, "model": "gpt-6.1-sol", "thinking_level": "low", "service_tier": "default", "case": "candidate five new conversations then three warm turns; actual model inference", "samples": samples}, "", "  ")
		if err := os.WriteFile(destination, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("sample=%d cold=%v launcher_pid=%d native_pid=%d reused=%v initialize_ms=%d thread_ms=%d prep_ms=%d duration_ms=%d observed_tokens=%d", index, cold, entry.PID, entry.NativePID, entry.Reused, entry.InitializeMs, entry.ThreadMs, entry.PreparationMs, entry.DurationMs, entry.Tokens)
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

// npm 启动器的 PID 对应 Node；身份检查需要匹配本次拥有的进程树中的 CLI 正文。
func codexBenchmarkNativePID(t *testing.T, root int, executable os.FileInfo) int {
	t.Helper()
	queue, visited, matched := []int{root}, make(map[int]bool), 0
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if visited[pid] {
			continue
		}
		visited[pid] = true
		if len(visited) > 256 {
			t.Fatal("owned benchmark process tree exceeded inspection limit")
		}
		if actual, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid)); err == nil && os.SameFile(actual, executable) {
			if matched != 0 {
				t.Fatal("owned process tree contains multiple matching native executables")
			}
			matched = pid
		}
		threads, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range threads {
			raw, err := os.ReadFile(thread)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range strings.Fields(string(raw)) {
				child, err := strconv.Atoi(value)
				if err != nil || child <= 0 {
					t.Fatal("invalid owned child process identity")
				}
				queue = append(queue, child)
			}
		}
	}
	if matched == 0 {
		t.Fatal("native executable not found in the owned launcher process tree")
	}
	return matched
}

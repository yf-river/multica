package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 只在显式原生验收入口运行，模型固定为本地确定性 provider。
func TestDshNativeInteractionSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("explicit native smoke only")
	}
	executable, home, module := os.Getenv("MULTICA_DSH_NATIVE_SMOKE_EXECUTABLE"), os.Getenv("MULTICA_DSH_NATIVE_SMOKE_HOME"), os.Getenv("MULTICA_DSH_HOST_LLM_MODULE")
	if executable == "" || home == "" || module == "" {
		t.Fatal("isolated native fixture paths required")
	}
	if _, err := os.Stat(filepath.Join(home, ".credentials.yaml")); !os.IsNotExist(err) {
		t.Fatal("native fixture home must not contain credentials")
	}
	work := t.TempDir()
	requests := filepath.Join(work, "requests.jsonl")
	run := func(phase, command, prompt, prior, decision string) Result {
		backend, err := New("dsh", Config{ExecutablePath: executable, TaskID: "native-adapter-" + phase, Env: map[string]string{
			"DSH_HOME": home, "DSH_TELEMETRY_DISABLED": "1", "MULTICA_DSH_PROFILE": "multica",
			"MULTICA_RUN_REAL_AGENT_SMOKE": "1", "MULTICA_DSH_HOST_LLM_MODULE": module,
			"MULTICA_DSH_FIXTURE_REQUESTS": requests, "MULTICA_DSH_FIXTURE_PHASE": phase,
		}})
		if err != nil {
			t.Fatal(err)
		}
		session, err := backend.Execute(context.Background(), prompt, ExecOptions{Cwd: work, Model: "multica-fixture/native-interaction", HumanInteractions: true, Command: command, ResumeSessionID: prior, PlanDecision: decision, Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		t.Logf("phase=%s status=%s session=%s", phase, result.Status, result.SessionID)
		return result
	}
	plan := run("plan", "/plan", "Plan a write to approved.txt. Wait for approval.", "", "")
	if plan.Status != "waiting_plan" || plan.PendingInteraction == nil || plan.PendingInteraction.PlanMarkdown == "" {
		t.Fatalf("plan not suspended: %#v", plan)
	}
	if _, err := os.Stat(filepath.Join(work, "approved.txt")); !os.IsNotExist(err) {
		t.Fatal("plan wrote before approval")
	}
	keep := run("keep-planning", "/plan", "继续规划，暂不执行。补充回滚步骤。", plan.SessionID, "keep_planning")
	if keep.Status != "waiting_plan" || keep.PendingInteraction == nil || keep.SessionID != plan.SessionID {
		t.Fatalf("keep planning lost session: %#v", keep)
	}
	if _, err := os.Stat(filepath.Join(work, "approved.txt")); !os.IsNotExist(err) {
		t.Fatal("keep planning wrote before approval")
	}
	approved := run("approved", "/plan off", "已批准本轮计划，请按计划执行。", keep.SessionID, "approved")
	if approved.Status != "completed" || approved.SessionID != plan.SessionID {
		t.Fatalf("approval did not resume: %#v", approved)
	}
	data, err := os.ReadFile(filepath.Join(work, "approved.txt"))
	if err != nil || string(data) != "DSH_APPROVED_WRITE" {
		t.Fatalf("native write failed: %q %v", data, err)
	}
}

// 无登录副本中的真实 DSH 必须在模型请求前终止，命令成功不能覆盖后续回合失败。
func TestDshNativeSignedOutSlashTurnDoesNotSucceed(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("explicit native smoke only")
	}
	executable, home := os.Getenv("MULTICA_DSH_NATIVE_SMOKE_EXECUTABLE"), os.Getenv("MULTICA_DSH_NATIVE_SMOKE_HOME")
	if executable == "" || home == "" {
		t.Fatal("isolated native fixture paths required")
	}
	if _, err := os.Stat(filepath.Join(home, ".credentials.yaml")); !os.IsNotExist(err) {
		t.Fatal("signed-out fixture must not contain credentials")
	}
	backend, err := New("dsh", Config{ExecutablePath: executable, TaskID: "native-signed-out", Env: map[string]string{
		"DSH_HOME": home, "DSH_TELEMETRY_DISABLED": "1", "MULTICA_DSH_PROFILE": "multica",
		"MULTICA_RUN_REAL_AGENT_SMOKE": "1", "MULTICA_DSH_HOST_LLM_MODULE": os.Getenv("MULTICA_DSH_HOST_LLM_MODULE"),
		"MULTICA_DSH_FIXTURE_REQUESTS": filepath.Join(t.TempDir(), "requests.jsonl"), "MULTICA_DSH_FIXTURE_PHASE": "canary",
	}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(context.Background(), "Say ready after leaving plan mode.", ExecOptions{Cwd: t.TempDir(), Command: "/plan off", Model: "deepseek-account/deepseek-flash", HumanInteractions: true, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status == "completed" {
		t.Fatalf("command masked signed-out turn: %#v", result)
	}
	if !strings.Contains(result.Error, "deepseek-account/signed-out") && !strings.Contains(result.Error, "ACCOUNT_SIGN_IN_REQUIRED") {
		t.Fatalf("missing native abort cause: %#v", result)
	}
	if len(result.Usage) != 0 {
		t.Fatalf("signed-out fixture unexpectedly called a model: %#v", result.Usage)
	}
	t.Logf("status=%s error=%s", result.Status, result.Error)
}

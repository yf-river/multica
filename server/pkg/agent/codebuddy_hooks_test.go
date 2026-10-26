package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 真实 CLI 的 hook 子进程重新执行当前测试程序，走与发布 CLI 相同的 helper。
func init() {
	if len(os.Args) > 1 && os.Args[1] == CodebuddyHookHelperArg {
		if err := RunCodebuddyHookHelper(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func testCodebuddyHooks(t *testing.T, env map[string]string, opts ExecOptions) *codebuddyHooks {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native hook transport uses a Unix socket")
	}
	hooks, err := newCodebuddyHooks(t.Context(), env, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hooks.close)
	hooks.bind("session")
	hooks.expectPrompt("root")
	return hooks
}

func TestCodebuddyHooksRejectUnownedGenerations(t *testing.T) {
	hooks := testCodebuddyHooks(t, nil, ExecOptions{})
	base := codebuddyHookInput{Event: "PreToolUse", SessionID: "session", GenerationID: "root", Tool: "Bash", ToolID: "bash", Input: map[string]any{"command": "true"}}
	for _, change := range []func(*codebuddyHookInput){
		func(in *codebuddyHookInput) { in.SessionID = "foreign" },
		func(in *codebuddyHookInput) { in.GenerationID = "" },
		func(in *codebuddyHookInput) { in.GenerationID = "old" },
		func(in *codebuddyHookInput) { in.AgentID = "unregistered-child" },
		func(in *codebuddyHookInput) { in.ToolID = "" },
	} {
		input := base
		change(&input)
		if _, err := hooks.handle(input); err == nil {
			t.Fatalf("unowned hook was accepted: %+v", input)
		}
	}
	if _, err := hooks.handle(base); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	hooks.ctx = ctx
	cancel()
	if _, err := hooks.handle(base); err == nil {
		t.Fatal("cancelled task retained tool authority")
	}
}

func TestCodebuddyHooksGoalAndForegroundChildOwnership(t *testing.T) {
	hooks := testCodebuddyHooks(t, nil, ExecOptions{})
	call := func(in codebuddyHookInput, want bool) json.RawMessage {
		t.Helper()
		in.SessionID = "session"
		raw, err := hooks.handle(in)
		if (err == nil) != want {
			t.Fatalf("hook %s cid=%s accepted=%v want=%v: %v", in.Event, in.GenerationID, err == nil, want, err)
		}
		return raw
	}
	call(codebuddyHookInput{Event: "UserPromptSubmit", GenerationID: "root"}, true)
	call(codebuddyHookInput{Event: "SubagentStart", GenerationID: "child", AgentID: "agent"}, false)
	call(codebuddyHookInput{Event: "SubagentStart", GenerationID: "another-child", AgentID: "agent"}, false)
	call(codebuddyHookInput{Event: "PreToolUse", GenerationID: "root", Tool: "Agent", ToolID: "call"}, true)
	call(codebuddyHookInput{Event: "SubagentStart", GenerationID: "child", AgentID: "agent"}, true)
	call(codebuddyHookInput{Event: "PreToolUse", GenerationID: "child", AgentID: "wrong-agent", Tool: "Bash", ToolID: "child-bash"}, false)
	call(codebuddyHookInput{Event: "PreToolUse", GenerationID: "child", AgentID: "agent", Tool: "Bash", ToolID: "child-bash", Input: map[string]any{"command": "true"}}, true)
	call(codebuddyHookInput{Event: "SubagentStop", GenerationID: "child", AgentID: "agent"}, true)
	call(codebuddyHookInput{Event: "PreToolUse", GenerationID: "child", AgentID: "agent", Tool: "Bash", ToolID: "late-child"}, false)
	call(codebuddyHookInput{Event: "SubagentStart", GenerationID: "child", AgentID: "agent"}, false)
	call(codebuddyHookInput{Event: "PostToolUse", GenerationID: "root", Tool: "Agent", ToolID: "call"}, true)
	call(codebuddyHookInput{Event: "PreToolUse", GenerationID: "root", Tool: "Agent", ToolID: "next-call"}, true)
	call(codebuddyHookInput{Event: "SubagentStart", GenerationID: "new-cid", AgentID: "agent"}, false)
	call(codebuddyHookInput{Event: "PostToolUseFailure", GenerationID: "root", Tool: "Agent", ToolID: "next-call"}, true)
	call(codebuddyHookInput{Event: "SubagentStart", GenerationID: "orphan", AgentID: "orphan-agent"}, false)
	call(codebuddyHookInput{Event: "UserPromptSubmit", GenerationID: "goal", Internal: true}, false)
	hooks.allowGoal(true)
	call(codebuddyHookInput{Event: "UserPromptSubmit", GenerationID: "human"}, false)
	call(codebuddyHookInput{Event: "UserPromptSubmit", GenerationID: "goal", Internal: true}, true)
	call(codebuddyHookInput{Event: "UserPromptSubmit", GenerationID: "root", Internal: true}, false)
	call(codebuddyHookInput{Event: "PreToolUse", GenerationID: "root", Tool: "Agent", ToolID: "late-root"}, false)
	hooks.allowGoal(false)
	call(codebuddyHookInput{Event: "UserPromptSubmit", GenerationID: "another-goal", Internal: true}, false)
}

func TestCodebuddyHooksPreserveSettingsAndApplyEnvironment(t *testing.T) {
	t.Setenv("MULTICA_PREVIOUS_TASK", "old")
	source := filepath.Join(t.TempDir(), "source.json")
	original := `{"skillOverrides":{"private":"off"},"permissions":{"deny":["Skill(private)"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"human-hook"}]}]}}`
	if err := os.WriteFile(source, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	value := "a'$()`\nnext line"
	hooks := testCodebuddyHooks(t, map[string]string{"MULTICA_CURRENT_TASK": value}, ExecOptions{CodebuddySettingsPath: source})
	settings, err := os.ReadFile(hooks.settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{"human-hook", `"private":"off"`, "Skill(private)", CodebuddyHookHelperArg} {
		if !bytes.Contains(settings, []byte(retained)) {
			t.Fatalf("settings lost %s", retained)
		}
	}
	unchanged, _ := os.ReadFile(source)
	if string(unchanged) != original {
		t.Fatal("source settings were mutated")
	}
	command := `printf '%s' "$MULTICA_CURRENT_TASK"; test "${MULTICA_PREVIOUS_TASK+x}" != x`
	input := codebuddyHookInput{Event: "PreToolUse", SessionID: "session", GenerationID: "root", Tool: "Bash", ToolID: "bash", Input: map[string]any{"command": command, "timeout": 4000}}
	raw, err := hooks.handle(input)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Specific struct {
			Updated map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("permissionDecision")) || output.Specific.Updated["timeout"] != float64(4000) || input.Input["command"] != command {
		t.Fatal("hook changed tool options, input ownership or approval")
	}
	proof, err := exec.Command("bash", "-c", output.Specific.Updated["command"].(string)).Output()
	if err != nil || string(proof) != value {
		t.Fatal("shell snapshot did not preserve literal values or clear the previous task")
	}
	for _, path := range []string{hooks.envFile, hooks.settings} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("hook artifact is not private")
		}
	}
}

func TestCodebuddyHookHelperRequiresLiveOwner(t *testing.T) {
	hooks := testCodebuddyHooks(t, nil, ExecOptions{})
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"session","generation_id":"root"}`
	var output bytes.Buffer
	if err := RunCodebuddyHookHelper([]string{hooks.socket, hooks.nonce}, strings.NewReader(payload), &output); err != nil || output.String() != "{}" {
		t.Fatalf("helper did not reach owner: %v", err)
	}
	output.Reset()
	if err := RunCodebuddyHookHelper([]string{hooks.socket, strings.Repeat("0", 64)}, strings.NewReader(payload), &output); err == nil || output.Len() != 0 {
		t.Fatal("helper accepted another owner")
	}
	hooks.close()
	if err := RunCodebuddyHookHelper([]string{hooks.socket, hooks.nonce}, strings.NewReader(payload), &output); err == nil {
		t.Fatal("ended task hook remained available")
	}
	if _, err := os.Stat(hooks.dir); !os.IsNotExist(err) {
		t.Fatal("ended task retained hook environment")
	}
}

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRuntimeNeverPolicyOverridesHumanResponder(t *testing.T) {
	opts := ExecOptions{ApprovalPolicy: "never", RequestInteraction: func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		t.Fatal("background role reached the human responder")
		return protocol.InteractionAnswer{Decision: "allow"}, nil
	}}
	for _, kind := range []string{"approval", "question", "plan"} {
		answer, err := runtimeInteractionRequest(opts)(context.Background(), protocol.InteractionPrompt{Kind: kind})
		if err != nil || answer.Decision != "deny" {
			t.Fatalf("%s: %+v, %v", kind, answer, err)
		}
	}
}

func TestCodexNeverPolicyDeniesNativeRequests(t *testing.T) {
	for method, want := range map[string]string{
		"item/commandExecution/requestApproval": `"decision":"decline"`,
		"execCommandApproval":                   `"decision":"decline"`,
		"item/fileChange/requestApproval":       `"decision":"decline"`,
		"applyPatchApproval":                    `"decision":"decline"`,
		"item/permissions/requestApproval":      `"permissions":{}`,
		"item/tool/requestUserInput":            `"error":`,
		"tool/requestUserInput":                 `"error":`,
		"mcpServer/elicitation/request":         `"action":"decline"`,
	} {
		t.Run(method, func(t *testing.T) {
			c, stdin, _ := newTestCodexClient(t)
			c.approvalPolicy = "never"
			c.handleLine(`{"id":17,"method":"` + method + `","params":{"permissions":{"network":{"enabled":true}}}}`)
			lines := stdin.Lines()
			if len(lines) != 1 || !strings.Contains(lines[0], want) {
				t.Fatalf("native denial: %v", lines)
			}
		})
	}
}

func TestCodexNeverPolicyAppliesToNewAndResumedThreads(t *testing.T) {
	for _, resume := range []string{"", "thread"} {
		c, stdin, _ := newTestCodexClient(t)
		c.cfg.CodexVersion = "0.164.0"
		method := "thread/start"
		if resume != "" {
			method = "thread/resume"
		}
		wait := drainRPCScript(t, c, stdin, []rpcResponse{{method: method, result: json.RawMessage(`{"thread":{"id":"thread"}}`), assertFn: func(t *testing.T, params map[string]any) {
			config, ok := params["config"].(map[string]any)
			if !ok || config["features.multi_agent"] != false || config["features.multi_agent_v2"] != false || config["agents.enabled"] != false {
				t.Errorf("%s did not enforce subagent policy: %v", method, config)
			}
			if params["approvalPolicy"] != "never" {
				t.Errorf("%s did not override stored approval policy: %v", method, params)
			}
		}}})
		_, _, err := c.startOrResumeThread(context.Background(), ExecOptions{ResumeSessionID: resume, ApprovalPolicy: "never"}, slog.Default())
		wait()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodebuddyNeverPolicyDeniesACPApproval(t *testing.T) {
	stdin := &fakeStdinWithHook{}
	client := &hermesClient{stdin: stdin}
	broker := newInteractionBroker("", context.Background(), runtimeInteractionRequest(ExecOptions{ApprovalPolicy: "never"}), nil)
	defer broker.stop()
	if !handleCodebuddyACPInteraction(client, broker, "session", "session/request_permission", json.RawMessage(`1`), json.RawMessage(`{"sessionId":"session","toolCall":{"title":"Write file"},"options":[{"kind":"allow_once","optionId":"allow"},{"kind":"reject_once","optionId":"deny"}]}`)) {
		t.Fatal("native permission request was not handled")
	}
	broker.wg.Wait()
	lines := stdin.Lines()
	if len(lines) != 1 || !strings.Contains(lines[0], `"optionId":"deny"`) {
		t.Fatalf("ACP policy denial: %v", lines)
	}
}

func TestCodebuddyNeverPolicyCannotBeOverriddenByCustomArgs(t *testing.T) {
	for _, plan := range []bool{false, true} {
		args := buildCodebuddyArgs(ExecOptions{ApprovalPolicy: "never", PlanMode: &plan,
			ExtraArgs:  []string{"--dangerously-skip-permissions", "--permission-mode-before-plan", "bypassPermissions"},
			CustomArgs: []string{"-y", "--subagent-permission-mode", "bypassPermissions", "--permission-mode", "bypassPermissions"},
		}, slog.Default())
		for _, arg := range args {
			if arg == "bypassPermissions" || arg == "-y" || arg == "--dangerously-skip-permissions" {
				t.Fatalf("native denial bypass: %v", args)
			}
		}
		want := "dontAsk"
		if plan {
			want = "plan"
		}
		if !strings.Contains(strings.Join(args, " "), "--permission-mode "+want) {
			t.Fatalf("permission mode: %v", args)
		}
	}
}

func TestCodebuddyNeverPolicyDeniesUnrecognizedControlRequest(t *testing.T) {
	b := &codebuddyBackend{cfg: Config{Logger: slog.Default()}}
	var written bytes.Buffer
	b.handleControlRequest(codebuddySDKMessage{RequestID: "request", Request: json.RawMessage(`{"subtype":"future_permission","tool_name":"Bash","input":{"command":"touch file"}}`)}, &written, "never")
	var reply struct {
		Response struct {
			Response struct {
				Allowed  bool   `json:"allowed"`
				Behavior string `json:"behavior"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(written.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Response.Response.Allowed || reply.Response.Response.Behavior != "deny" {
		t.Fatalf("unexpected approval: %s", written.String())
	}
}

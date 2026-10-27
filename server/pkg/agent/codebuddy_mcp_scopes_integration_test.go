//go:build agentintegration && linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 原生作用域由 CLI 自己登记，避免用猜测的配置文件格式证明合并行为。
func TestCodebuddyRealNativeMCPScopeUnion(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real scope preservation model calls are opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	destination := os.Getenv("MULTICA_CODEBUDDY_SCOPE_SMOKE_RESULT")
	if destination == "" {
		t.Fatal("private scope verification result path required")
	}
	root, err := os.MkdirTemp(filepath.Dir(destination), "mcp-scopes-")
	if err != nil {
		t.Fatal(err)
	}
	home, config := filepath.Join(root, "home"), filepath.Join(root, "home", "codebuddy")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(runtime.Home, "codebuddy")
	for _, name := range []string{"settings.json", "user-state.json", "models.json", ".policy-cache.json"} {
		raw, err := os.ReadFile(filepath.Join(source, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.CopyFS(filepath.Join(config, "local_storage"), os.DirFS(filepath.Join(source, "local_storage"))); err != nil {
		t.Fatal(err)
	}
	authRelative := filepath.Join(".local", "share", "CodeBuddyExtension", "Data", "Public", "auth")
	if err := os.CopyFS(filepath.Join(home, authRelative), os.DirFS(filepath.Join(runtime.Home, authRelative))); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp("", "multica-owned-mcp-scopes-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "work-path.txt"), []byte(work), 0600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(root, "mcp.py")
	const script = `import os,sys,json
scope,secret,audit=sys.argv[1:]
for line in sys.stdin:
 r=json.loads(line)
 if 'id' not in r:continue
 method=r.get('method')
 if method=='initialize':result={'protocolVersion':'2024-11-05','capabilities':{'tools':{}},'serverInfo':{'name':'owned-scope-'+scope,'version':'1'}}
 elif method=='ping':result={}
 elif method=='tools/list':result={'tools':[{'name':'read_marker','description':'Read the secret marker from the '+scope+' configuration scope. No side effects except a private test audit.','inputSchema':{'type':'object','properties':{},'additionalProperties':False},'annotations':{'readOnlyHint':True,'destructiveHint':False,'openWorldHint':False}}]}
 elif method=='tools/call' and r.get('params',{}).get('name')=='read_marker':
  value=os.environ.get('MULTICA_SCOPE_CANARY','UNSET')
  fd=os.open(audit,os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
  with os.fdopen(fd,'a') as f:f.write(json.dumps({'scope':scope,'canary':value,'pid':os.getpid()})+'\n')
  result={'content':[{'type':'text','text':secret+':'+value}]}
 else:
  print(json.dumps({'jsonrpc':'2.0','id':r['id'],'error':{'code':-32601,'message':'unknown method'}}),flush=True);continue
 print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':result}),flush=True)
`
	if err := os.WriteFile(probe, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(root, "calls.jsonl")
	secrets := make(map[string]string)
	for _, scope := range []string{"user", "project", "local", "managed"} {
		marker, err := newCodebuddyGeneration()
		if err != nil {
			t.Fatal(err)
		}
		secrets[scope] = marker
	}
	baseEnv := map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": config, "DISABLE_AUTOUPDATER": "1"}
	for _, scope := range []string{"user", "project", "local"} {
		setup, stop := context.WithTimeout(t.Context(), 15*time.Second)
		args := []string{"mcp", "add", "--scope", scope, "owned_" + scope, "--", "python3", probe, scope, secrets[scope], audit}
		cmd := NewCommand(runtime.Path, nil).exec(setup, args...)
		cmd.Env = buildEnv(baseEnv)
		cmd.Dir = work
		out, err := outputOwned(cmd, slog.Default())
		stop()
		if writeErr := os.WriteFile(filepath.Join(root, "setup-"+scope+".log"), out, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if err != nil {
			t.Fatalf("native MCP registration failed: scope=%s error=%v", scope, err)
		}
	}
	var observations []map[string]any
	for _, mode := range []string{"ordinary", "plan", "goal"} {
		t.Run(mode, func(t *testing.T) {
			canary, err := newCodebuddyGeneration()
			if err != nil {
				t.Fatal(err)
			}
			env := make(map[string]string)
			for key, value := range baseEnv {
				env[key] = value
			}
			env["MULTICA_SCOPE_CANARY"] = canary
			pool, err := NewCodebuddyProcessPool(filepath.Join(root, "pool-"+mode), 1, time.Minute, slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			cfg := Config{ExecutablePath: runtime.Path, BuiltinRuntime: true, CLIVersion: runtime.Version, Logger: slog.Default(), Env: env, CodebuddyPool: pool, ResidentSessionKey: "owned-scope-union", TaskID: "owned-scope-" + mode}
			if !codebuddyNeedsTaskProcess(cfg, work) {
				t.Fatal("native scopes did not select an isolated task process")
			}
			mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"owned_managed": map[string]any{"command": "python3", "args": []string{probe, "managed", secrets["managed"], audit}, "env": map[string]string{"MULTICA_SCOPE_CANARY": canary}}}})
			prompt := "Call each MCP tool exactly once: mcp__owned_user__read_marker, mcp__owned_project__read_marker, mcp__owned_local__read_marker, mcp__owned_managed__read_marker. Return a JSON object mapping user, project, local, managed to the exact returned texts. If a tool is unavailable, use UNAVAILABLE for that key. Do not run shell commands, read files or fabricate values."
			opts := ExecOptions{Cwd: work, Model: "deepseek-v4.1-flash", Timeout: 120 * time.Second, McpConfig: mcp, CustomArgs: []string{"--settings", `{"enabledMcpjsonServers":["owned_project"]}`}}
			var approvals atomic.Int32
			opts.RequestInteraction = func(_ context.Context, p protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
				if p.Kind == "question" {
					return protocol.InteractionAnswer{Decision: "deny"}, fmt.Errorf("unexpected question in scope canary")
				}
				approvals.Add(1)
				return protocol.InteractionAnswer{Decision: "allow"}, nil
			}
			if mode == "plan" {
				plan := true
				opts.PlanMode = &plan
			}
			if mode == "goal" {
				opts.NativeGoal = true
				opts.Command = "/goal set " + prompt
				prompt = ""
			}
			session, err := (&codebuddyBackend{cfg: cfg}).Execute(t.Context(), prompt, opts)
			if err != nil {
				t.Fatal(err)
			}
			var tools []string
			for msg := range session.Messages {
				if msg.Type == MessageToolUse {
					tools = append(tools, msg.Tool)
				}
			}
			result := <-session.Result
			raw, readErr := os.ReadFile(audit)
			if readErr != nil {
				raw = nil
			}
			seen := make(map[string]int)
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line == "" {
					continue
				}
				var call struct {
					Scope  string `json:"scope"`
					Canary string `json:"canary"`
				}
				if err := json.Unmarshal([]byte(line), &call); err != nil {
					t.Fatal(err)
				}
				if call.Canary == canary {
					seen[call.Scope]++
				}
			}
			allScopes := true
			for scope := range secrets {
				allScopes = allScopes && seen[scope] > 0
			}
			_, actualModel := result.Usage["deepseek-v4.1-flash-ioa"]
			observation := map[string]any{"mode": mode, "status": result.Status, "error": result.Error, "all_scopes_called": allScopes, "scope_calls": seen, "tool_names": tools, "model_matches": actualModel, "usage": result.Usage, "automated_approvals": approvals.Load(), "session_id": result.SessionID, "duration_ms": result.DurationMs, "pool_entries_after_result": len(pool.entries)}
			if mode == "goal" {
				observation["native_goal_evaluator_usage"] = "unknown"
			}
			observations = append(observations, observation)
			report, _ := json.MarshalIndent(map[string]any{"cli": runtime.Version, "executable_sha256": runtime.SHA256, "fixture_root": root, "observations": observations}, "", "  ")
			if err := os.WriteFile(destination, report, 0600); err != nil {
				t.Fatal(err)
			}
			for _, tool := range tools {
				switch strings.ToLower(tool) {
				case "bash", "read", "write", "edit", "exec_command":
					t.Errorf("scope canary used an unrelated file or shell tool: %s", tool)
				}
			}
			if result.Status != "completed" || !allScopes || !actualModel || len(pool.entries) != 0 {
				t.Fatalf("scope union failed: mode=%s status=%s error=%s scope_calls=%v model=%t pool_entries=%d", mode, result.Status, result.Error, seen, actualModel, len(pool.entries))
			}
			t.Logf("mode=%s all_native_and_managed_scopes_called=true isolated_task_process=true automated_approvals=%d", mode, approvals.Load())
		})
	}
}

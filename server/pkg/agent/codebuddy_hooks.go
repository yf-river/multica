package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CodebuddyHookHelperArg 由当前 daemon 二进制处理，不经过用户 CLI 配置或 HTTP API。
const CodebuddyHookHelperArg = "--internal-codebuddy-hook"

const codebuddyHookLimit = 1024 * 1024

type codebuddyHookInput struct {
	Event        string         `json:"hook_event_name"`
	SessionID    string         `json:"session_id"`
	GenerationID string         `json:"generation_id"`
	AgentID      string         `json:"agent_id"`
	ToolID       string         `json:"tool_use_id"`
	Tool         string         `json:"tool_name"`
	Input        map[string]any `json:"tool_input"`
	Internal     bool           `json:"is_internal_continuation"`
}

type codebuddyHookReply struct {
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type codebuddyHookAgent struct{ cid, agent string }

// 每次执行独占一份 hook 状态和不可变环境。只认原生会话、已登记根回合及
// 在已知 Agent 调用内启动的子代；同一个 session_id 不能独自换取任务凭据。
type codebuddyHooks struct {
	mu                 sync.Mutex
	ctx                context.Context
	dir, socket, nonce string
	settings, envFile  string
	sid, root          string
	brief              string
	goal, closed       bool
	active             bool
	envKeys            map[string]bool
	leaseCIDs          map[string]bool
	seen               map[string]bool
	seenAgents         map[string]bool
	children           map[string]string
	agents             map[string]codebuddyHookAgent
	server             *http.Server
	done               chan struct{}
}

func codebuddyManagedHooksSupported(cfg Config) bool {
	return cfg.BuiltinRuntime && CodebuddyResidentSupported(cfg.CLIVersion)
}

func CodebuddyResidentSupported(version string) bool {
	parsed, err := parseSemver(version)
	return runtime.GOOS == "linux" && err == nil && parsed.Major == 2 && !parsed.lessThan(semver{Major: 2, Minor: 176})
}

// CodebuddyNativeHooksEnabled 与执行分派共用判断，供 daemon 选择简报投递方式。
func CodebuddyNativeHooksEnabled(cfg Config, opts ExecOptions) bool {
	return codebuddyManagedHooksSupported(cfg) && (codebuddyNativeRequested(opts) || cfg.CodebuddyPool != nil)
}

func newCodebuddyHooks(ctx context.Context, env map[string]string, opts ExecOptions) (_ *codebuddyHooks, err error) {
	dir, err := os.MkdirTemp("", "multica-cb-hooks-")
	if err != nil {
		return nil, err
	}
	h := &codebuddyHooks{ctx: ctx, dir: dir, socket: filepath.Join(dir, "hook.sock"), settings: filepath.Join(dir, "settings.json"), envFile: filepath.Join(dir, "environment.sh"), seen: map[string]bool{}, seenAgents: map[string]bool{}, children: map[string]string{}, agents: map[string]codebuddyHookAgent{}, done: make(chan struct{})}
	h.active, h.envKeys = true, make(map[string]bool)
	h.leaseCIDs = make(map[string]bool)
	for key := range env {
		h.envKeys[key] = true
	}
	h.brief = opts.CodebuddyRuntimeBrief
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	h.nonce = hex.EncodeToString(nonce[:])
	if err = writeCodebuddyHookEnvironment(h.envFile, env); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := shellQuote(executable) + " " + CodebuddyHookHelperArg + " " + shellQuote(h.socket) + " " + shellQuote(h.nonce)
	settings, err := codebuddyHookSettings(opts)
	if err != nil {
		return nil, err
	}
	hooks := map[string][]json.RawMessage{}
	if raw := settings["hooks"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &hooks); err != nil || hooks == nil {
			return nil, errors.New("CodeBuddy hooks must be an object of hook lists")
		}
	}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "SubagentStart", "SubagentStop"} {
		raw, _ := json.Marshal(map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 10}}})
		hooks[event] = append(hooks[event], raw)
	}
	settings["hooks"], err = json.Marshal(hooks)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(h.settings, raw, 0600); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		return nil, err
	}
	h.server = &http.Server{Handler: http.HandlerFunc(h.serve), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: time.Second}
	go func() {
		defer close(h.done)
		_ = h.server.Serve(listener)
	}()
	return h, nil
}

func writeCodebuddyHookEnvironment(path string, env map[string]string, clear ...string) error {
	keys := map[string]bool{}
	for _, key := range clear {
		keys[key] = true
	}
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(strings.ToUpper(key), "MULTICA_") {
			keys[key] = true
		}
	}
	for key, value := range env {
		if !validCodebuddyEnvironmentKey(key) || strings.ContainsRune(value, 0) {
			return errors.New("CodeBuddy task environment contains an invalid shell assignment")
		}
		keys[key] = true
	}
	var ordered []string
	for key := range keys {
		if !validCodebuddyEnvironmentKey(key) {
			return errors.New("CodeBuddy inherited task environment contains an invalid shell name")
		}
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	var body strings.Builder
	for _, key := range ordered {
		body.WriteString("unset " + key + "\n")
		if value, exists := env[key]; exists {
			body.WriteString("export " + key + "=" + shellQuote(value) + "\n")
		}
	}
	body.WriteString("export " + codebuddyDisableBackgroundTasksEnv + "=1\n")
	return os.WriteFile(path, []byte(body.String()), 0600)
}

func (h *codebuddyHooks) begin(ctx context.Context, env map[string]string, brief string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.active || len(h.children) != 0 || len(h.agents) != 0 {
		return errors.New("CodeBuddy hook owner has an unfinished lease")
	}
	var clear []string
	for key := range h.envKeys {
		clear = append(clear, key)
	}
	name, err := newCodebuddyGeneration()
	if err != nil {
		return err
	}
	path := filepath.Join(h.dir, name+".sh")
	if err := writeCodebuddyHookEnvironment(path, env, clear...); err != nil {
		return err
	}
	for key := range env {
		h.envKeys[key] = true
	}
	h.ctx, h.envFile, h.brief, h.active = ctx, path, brief, true
	h.leaseCIDs = make(map[string]bool)
	h.sid, h.root, h.goal = "", "", false
	return nil
}

func (h *codebuddyHooks) end() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active = false
	h.ctx = context.Background()
	h.brief, h.sid, h.root, h.goal = "", "", "", false
	if len(h.children) != 0 || len(h.agents) != 0 {
		return errors.New("CodeBuddy foreground hook calls have not completed")
	}
	return os.Remove(h.envFile)
}

func (h *codebuddyHooks) acceptsUpdate(sid, cid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active && !h.closed && h.ctx.Err() == nil && sid == h.sid && cid != "" && h.leaseCIDs[cid]
}

func (h *codebuddyHooks) currentRoot(cid string) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.root == cid
}

func validCodebuddyEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func newCodebuddyGeneration() (string, error) {
	id, err := uuid.NewV7()
	return strings.ReplaceAll(id.String(), "-", ""), err
}

func (h *codebuddyHooks) bind(sid string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.sid = sid
	h.mu.Unlock()
}

func (h *codebuddyHooks) allowGoal(allow bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.goal = allow
	h.mu.Unlock()
}

func (h *codebuddyHooks) expectPrompt(cid string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.root, h.seen[cid] = cid, true
	h.leaseCIDs[cid] = true
	h.mu.Unlock()
}

func (h *codebuddyHooks) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.mu.Unlock()
	_ = h.server.Close()
	<-h.done
	_ = os.RemoveAll(h.dir)
}

func (h *codebuddyHooks) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/hook" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(h.nonce)) != 1 {
		http.Error(w, "hook authorization rejected", http.StatusUnauthorized)
		return
	}
	var input codebuddyHookInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, codebuddyHookLimit))
	if decoder.Decode(&input) != nil {
		http.Error(w, "invalid hook input", http.StatusBadRequest)
		return
	}
	output, err := h.handle(input)
	reply := codebuddyHookReply{Output: output}
	if err != nil {
		reply.Error = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func (h *codebuddyHooks) handle(input codebuddyHookInput) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !h.active || h.ctx.Err() != nil || h.sid == "" || input.SessionID != h.sid || input.GenerationID == "" {
		return nil, errors.New("CodeBuddy hook has no matching active task generation")
	}
	cid := input.GenerationID
	if input.Event == "UserPromptSubmit" {
		if input.AgentID != "" {
			return nil, errors.New("CodeBuddy child cannot register a root prompt")
		}
		if cid != h.root {
			if !h.goal || !input.Internal || h.seen[cid] {
				return nil, errors.New("CodeBuddy prompt generation was not issued by this task")
			}
			h.root, h.seen[cid] = cid, true
			h.leaseCIDs[cid] = true
		}
		if h.brief != "" {
			return json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": input.Event, "additionalContext": h.brief}})
		}
		return json.RawMessage(`{}`), nil
	}
	if input.Event == "SubagentStart" {
		if input.AgentID == "" || h.seen[cid] || h.seenAgents[input.AgentID] {
			return nil, errors.New("CodeBuddy child generation is missing or already used")
		}
		knownParent := false
		for _, parent := range h.agents {
			if parent.cid == h.root || h.children[parent.cid] == parent.agent && parent.agent != "" {
				knownParent = true
				break
			}
		}
		if !knownParent {
			return nil, errors.New("CodeBuddy child has no active foreground Agent call")
		}
		h.children[cid], h.seen[cid] = input.AgentID, true
		h.leaseCIDs[cid] = true
		h.seenAgents[input.AgentID] = true
		return json.RawMessage(`{}`), nil
	}
	validRoot := cid == h.root && input.AgentID == ""
	validChild := input.AgentID != "" && h.children[cid] == input.AgentID
	if !validRoot && !validChild {
		return nil, errors.New("CodeBuddy hook belongs to an unknown or completed generation")
	}
	switch input.Event {
	case "SubagentStop":
		if !validChild {
			return nil, errors.New("CodeBuddy child stop has no matching child")
		}
		delete(h.children, cid)
	case "PreToolUse":
		if input.ToolID == "" {
			return nil, errors.New("CodeBuddy tool hook has no call identity")
		}
		if input.Tool == "Agent" || input.Tool == "Task" {
			h.agents[input.ToolID] = codebuddyHookAgent{cid: cid, agent: input.AgentID}
		}
		if input.Tool == "Bash" {
			command, ok := input.Input["command"].(string)
			if !ok {
				return nil, errors.New("CodeBuddy Bash hook has no command")
			}
			updated := make(map[string]any, len(input.Input))
			for key, value := range input.Input {
				updated[key] = value
			}
			updated["command"] = ". " + shellQuote(h.envFile) + "\n" + command
			return json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": input.Event, "updatedInput": updated}})
		}
	case "PostToolUse", "PostToolUseFailure":
		if owner, exists := h.agents[input.ToolID]; exists {
			if owner.cid != cid || owner.agent != input.AgentID {
				return nil, errors.New("CodeBuddy tool completion has a different generation")
			}
			delete(h.agents, input.ToolID)
		}
	default:
		return nil, errors.New("unsupported CodeBuddy task hook")
	}
	return json.RawMessage(`{}`), nil
}

// RunCodebuddyHookHelper 转发厂商原生 hook。非零退出（调用方使用 2）阻止未归属
// 的工具或提示词继续执行；此入口不产生 allow，也不接收业务 HTTP 凭据。
func RunCodebuddyHookHelper(args []string, input io.Reader, output io.Writer) error {
	if len(args) != 2 || !filepath.IsAbs(args[0]) || len(args[1]) != 64 {
		return errors.New("invalid CodeBuddy hook helper invocation")
	}
	raw, err := io.ReadAll(io.LimitReader(input, codebuddyHookLimit+1))
	if err != nil || len(raw) > codebuddyHookLimit || !json.Valid(raw) {
		return errors.New("invalid CodeBuddy hook payload")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", args[0])
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/hook", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", args[1])
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return errors.New("CodeBuddy hook owner is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("CodeBuddy hook owner rejected the request")
	}
	var reply codebuddyHookReply
	if err := json.NewDecoder(io.LimitReader(response.Body, codebuddyHookLimit)).Decode(&reply); err != nil {
		return errors.New("invalid CodeBuddy hook owner response")
	}
	if reply.Error != "" {
		return fmt.Errorf("CodeBuddy task hook rejected: %s", reply.Error)
	}
	if !json.Valid(reply.Output) {
		return errors.New("CodeBuddy hook owner omitted its output")
	}
	_, err = output.Write(reply.Output)
	return err
}

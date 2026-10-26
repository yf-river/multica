package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// CodebuddyProcessPool 拥有原生 ACP 进程；同一逻辑会话的任务独占租约。
// 根进程不持有任务环境，Bash 和受管 MCP 的执行身份由租约换发。
type CodebuddyProcessPool struct {
	mu        sync.Mutex
	root      string
	capacity  int
	idle      time.Duration
	logger    *slog.Logger
	entries   map[string]*codebuddyPoolEntry
	changed   chan struct{}
	closed    bool
	lock      io.Closer
	unhealthy atomic.Bool
}

type codebuddyPoolEntry struct {
	mu                           sync.Mutex
	writeMu                      sync.Mutex
	key, fingerprint, dir, owner string
	cmd                          *exec.Cmd
	stdin                        io.WriteCloser
	hooks                        *codebuddyHooks
	stderr                       *codexPoolStderr
	done                         chan struct{}
	readerDone                   chan struct{}
	active                       *codebuddyProcessLease
	initialize                   json.RawMessage
	execution                    string
	taskProcess                  bool
	sequence                     atomic.Int64
	busy                         bool
	lastUsed                     time.Time
	unpin                        func()
	logger                       *slog.Logger
}

type codebuddyProcessLease struct {
	pool       *CodebuddyProcessPool
	entry      *codebuddyPoolEntry
	reader     *io.PipeReader
	writer     *io.PipeWriter
	pending    map[int]string
	execution  string
	stderr     *stderrTail
	reused     bool
	closing    bool
	once       sync.Once
	releaseErr error
}

func NewCodebuddyProcessPool(root string, capacity int, idle time.Duration, logger *slog.Logger) (*CodebuddyProcessPool, error) {
	if !filepath.IsAbs(root) || capacity < 1 || idle <= 0 {
		return nil, errors.New("invalid CodeBuddy process pool configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("CodeBuddy process pool requires a private directory")
	}
	lock, err := lockCodexPoolRoot(root)
	if err != nil {
		return nil, err
	}
	if err := recoverCodebuddyPoolProcesses(root); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return &CodebuddyProcessPool{root: root, capacity: capacity, idle: idle, logger: logger, lock: lock, entries: map[string]*codebuddyPoolEntry{}, changed: make(chan struct{})}, nil
}

func (p *CodebuddyProcessPool) signalLocked() { close(p.changed); p.changed = make(chan struct{}) }

func (p *CodebuddyProcessPool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	var entries []*codebuddyPoolEntry
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	p.entries = map[string]*codebuddyPoolEntry{}
	p.signalLocked()
	p.mu.Unlock()
	for _, e := range entries {
		p.stop(e)
	}
	_ = p.lock.Close()
}

func (p *CodebuddyProcessPool) stop(e *codebuddyPoolEntry) {
	e.mu.Lock()
	execution := e.execution
	if e.active != nil {
		execution = e.active.execution
		_ = e.active.writer.CloseWithError(errors.New("CodeBuddy resident process stopped"))
	}
	e.mu.Unlock()
	_ = e.stdin.Close()
	select {
	case <-e.done:
	case <-time.After(time.Second):
		signalProcessGroup(e.cmd, syscall.SIGKILL)
	}
	var executionErr error
	if execution != "" {
		executionErr = reapCodexResidentExecution(execution)
	}
	ownerErr := reapCodexResidentOwner(e.owner)
	<-e.done
	<-e.readerDone
	e.hooks.close()
	if ownerErr != nil || executionErr != nil || !waitProcessGroupGone(e.cmd, 3*time.Second) {
		p.unhealthy.Store(true)
		p.logger.Error("CodeBuddy resident process cleanup could not be confirmed", "pid", e.cmd.Process.Pid)
		return
	}
	if e.unpin != nil {
		e.unpin()
	}
	releaseProcessGroup(e.cmd)
	_ = os.RemoveAll(e.dir)
}

func (p *CodebuddyProcessPool) acquire(ctx context.Context, cfg Config, opts ExecOptions) (*codebuddyProcessLease, error) {
	if !codebuddyManagedHooksSupported(cfg) || cfg.ResidentSessionKey == "" || cfg.TaskID == "" {
		return nil, errors.New("CodeBuddy resident execution requires a supported builtin and logical task identity")
	}
	keyBytes := sha256.Sum256([]byte(cfg.ResidentSessionKey + "\x00" + opts.Cwd + "\x00" + resolveSDKConfigDir(cfg.Env)))
	key := hex.EncodeToString(keyBytes[:])
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fingerprint, err := codebuddyResidentFingerprint(cfg, opts)
		if err != nil {
			return nil, err
		}
		taskProcess := opts.MaxTurns > 0 || codebuddyNeedsTaskProcess(cfg, opts.Cwd)
		p.mu.Lock()
		if p.closed || p.unhealthy.Load() {
			p.mu.Unlock()
			return nil, errors.New("CodeBuddy process pool is unavailable")
		}
		e := p.entries[key]
		if e != nil && e.busy {
			changed := p.changed
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
				continue
			}
		}
		reused := e != nil
		if e != nil {
			select {
			case <-e.done:
				e.fingerprint = ""
			default:
			}
			if e.fingerprint != fingerprint {
				delete(p.entries, key)
				p.stop(e)
				e = nil
				reused = false
			}
		}
		if e == nil && len(p.entries) >= p.capacity {
			var oldest *codebuddyPoolEntry
			for _, candidate := range p.entries {
				if !candidate.busy && (oldest == nil || candidate.lastUsed.Before(oldest.lastUsed)) {
					oldest = candidate
				}
			}
			if oldest == nil {
				changed := p.changed
				p.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-changed:
					continue
				}
			}
			delete(p.entries, oldest.key)
			p.stop(oldest)
		}
		if p.unhealthy.Load() {
			p.mu.Unlock()
			return nil, errors.New("CodeBuddy prior owner cleanup failed")
		}
		if e == nil {
			e, err = p.start(key, fingerprint, cfg, opts, taskProcess)
			if err != nil {
				p.signalLocked()
				p.mu.Unlock()
				return nil, err
			}
			p.entries[key] = e
		}
		e.busy = true
		var nonce [16]byte
		_, err = rand.Read(nonce[:])
		if err != nil {
			delete(p.entries, key)
			p.stop(e)
			p.signalLocked()
			p.mu.Unlock()
			return nil, err
		}
		execution := hex.EncodeToString(nonce[:])
		env := make(map[string]string, len(cfg.Env)+1)
		for k, v := range cfg.Env {
			env[k] = v
		}
		env[codexExecutionOwnerEnv] = execution
		if err = e.hooks.begin(ctx, env, opts.CodebuddyRuntimeBrief); err != nil {
			delete(p.entries, key)
			p.stop(e)
			p.signalLocked()
			p.mu.Unlock()
			return nil, err
		}
		r, w := io.Pipe()
		lease := &codebuddyProcessLease{pool: p, entry: e, reader: r, writer: w, pending: map[int]string{}, execution: execution, reused: reused, stderr: e.stderr.begin()}
		e.mu.Lock()
		e.active = lease
		e.execution = execution
		e.mu.Unlock()
		if err = writeCodebuddyPoolRecord(e.dir, e.hooks.dir, e.owner, cfg.TaskID, execution, e.cmd.Process.Pid); err != nil {
			delete(p.entries, key)
			p.stop(e)
			p.signalLocked()
			p.mu.Unlock()
			return nil, err
		}
		p.mu.Unlock()
		return lease, nil
	}
}

// 原生 user/project/local MCP 可能隐式读取任务环境，且具有自己的作用域审批。
// 保留其原生解析与审批，使用独占进程；受管 MCP 可通过 ACP 换发环境。
func codebuddyNeedsTaskProcess(cfg Config, cwd string) bool {
	home := cfg.Env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	config := resolveSDKConfigDir(cfg.Env)
	if home == "" || config == "" {
		return true
	}
	exists := func(path string) bool { _, err := os.Lstat(path); return !errors.Is(err, os.ErrNotExist) }
	for _, path := range []string{filepath.Join(config, ".mcp.json"), filepath.Join(config, "mcp.json"), filepath.Join(home, ".codebuddy.json")} {
		if exists(path) {
			return true
		}
	}
	var hasMCP func(any) bool
	hasMCP = func(value any) bool {
		switch x := value.(type) {
		case map[string]any:
			for key, child := range x {
				if strings.EqualFold(key, "mcpServers") || strings.EqualFold(key, "mcp_servers") {
					return true
				}
				if hasMCP(child) {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if hasMCP(child) {
					return true
				}
			}
		}
		return false
	}
	settingsNeedsProcess := func(path string) bool {
		if !exists(path) {
			return false
		}
		raw, ok := readRegularFileBounded(path)
		if !ok {
			return true
		}
		var document any
		return json.Unmarshal(raw, &document) != nil || hasMCP(document)
	}
	if settingsNeedsProcess(filepath.Join(config, "settings.json")) {
		return true
	}
	for dir := cwd; dir != ""; dir = filepath.Dir(dir) {
		for _, name := range []string{".mcp.json", ".codebuddy/.mcp.json", ".codebuddy/mcp.json"} {
			if exists(filepath.Join(dir, name)) {
				return true
			}
		}
		for _, name := range []string{".codebuddy/settings.json", ".codebuddy/settings.local.json"} {
			if settingsNeedsProcess(filepath.Join(dir, name)) {
				return true
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	needs := false
	_ = filepath.WalkDir(filepath.Join(config, "plugins", "cache"), func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			needs = true
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == ".mcp.json" || entry.Name() == "mcp.json" || entry.Name() == "plugin.json" && settingsNeedsProcess(path) {
			needs = true
			return filepath.SkipAll
		}
		return nil
	})
	return needs
}

func (p *CodebuddyProcessPool) start(key, fingerprint string, cfg Config, opts ExecOptions, taskProcess bool) (*codebuddyPoolEntry, error) {
	dir := filepath.Join(p.root, key)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = os.Remove(dir)
		return nil, err
	}
	owner := hex.EncodeToString(nonce[:])
	static := opts
	static.CodebuddyRuntimeBrief = ""
	hooks, err := newCodebuddyHooks(context.Background(), nil, static)
	if err != nil {
		_ = os.Remove(dir)
		return nil, err
	}
	if err = hooks.end(); err != nil {
		hooks.close()
		_ = os.Remove(dir)
		return nil, err
	}
	if err = writeCodebuddyPoolRecord(dir, hooks.dir, owner, "", "", 0); err != nil {
		hooks.close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	static.CodebuddySettingsPath = hooks.settings
	args := codebuddyNativeArgs(static, cfg)
	executable := cfg.ExecutablePath
	if executable == "" {
		executable = "codebuddy"
	}
	cmd := cfg.commandAt(executable).exec(context.Background(), args...)
	cmd.Env = append(codexResidentEnvironment(cfg.Env), codebuddyDisableBackgroundTasksEnv+"=1", codexPoolOwnerEnv+"="+owner)
	if taskProcess {
		cmd.Env = append(buildCodebuddyEnv(cfg.Env), codexPoolOwnerEnv+"="+owner)
	}
	cmd.Dir, cmd.WaitDelay = opts.Cwd, 5*time.Second
	hideAgentWindow(cmd)
	configureCodexResidentOwner(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		hooks.close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		hooks.close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stderr := &codexPoolStderr{}
	cmd.Stderr = stderr
	cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	if err = startOwnedProcessTree(cmd, p.logger); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		hooks.close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	e := &codebuddyPoolEntry{key: key, fingerprint: fingerprint, dir: dir, owner: owner, cmd: cmd, stdin: stdin, hooks: hooks, stderr: stderr, done: make(chan struct{}), readerDone: make(chan struct{})}
	e.logger = p.logger
	e.taskProcess = taskProcess
	if cfg.PinResidentHome != nil {
		e.unpin = cfg.PinResidentHome()
	}
	go func() { _ = cmd.Wait(); close(e.done) }()
	go e.read(stdout)
	if err = writeCodebuddyPoolRecord(dir, hooks.dir, owner, "", "", cmd.Process.Pid); err != nil {
		p.stop(e)
		return nil, err
	}
	return e, nil
}

func (e *codebuddyPoolEntry) read(stdout io.ReadCloser) {
	defer close(e.readerDone)
	defer stdout.Close()
	defer func() {
		e.mu.Lock()
		if e.active != nil {
			_ = e.active.writer.CloseWithError(errors.New("CodeBuddy ACP process exited"))
		}
		e.mu.Unlock()
	}()
	scanner := newAgentStreamScanner(stdout)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					Meta map[string]json.RawMessage `json:"_meta"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &frame) != nil {
			continue
		}
		e.mu.Lock()
		lease := e.active
		accept := lease != nil && !lease.closing
		if len(frame.ID) > 0 && frame.Method == "" {
			var id int
			if json.Unmarshal(frame.ID, &id) != nil || !accept {
				accept = false
			} else if method, ok := lease.pending[id]; !ok {
				accept = false
			} else {
				delete(lease.pending, id)
				if method == "initialize" && len(frame.Result) > 0 {
					e.initialize = append(json.RawMessage(nil), frame.Result...)
				}
			}
		} else if frame.Method == "session/update" {
			var cid string
			_ = json.Unmarshal(frame.Params.Update.Meta["codebuddy.ai/requestId"], &cid)
			accept = accept && e.hooks.acceptsUpdate(frame.Params.SessionID, cid)
		} else if frame.Method != "" && len(frame.ID) > 0 {
			// 原生反向请求也必须带当前会话；空闲或旧会话不能借用新租约的回答器。
			e.hooks.mu.Lock()
			sid := e.hooks.sid
			active := e.hooks.active
			e.hooks.mu.Unlock()
			accept = accept && active && sid != "" && frame.Params.SessionID == sid
		}
		e.mu.Unlock()
		var requestID, conversationID string
		_ = json.Unmarshal(frame.Params.Update.Meta["codebuddy.ai/requestId"], &requestID)
		_ = json.Unmarshal(frame.Params.Update.Meta["codebuddy.ai/conversationRequestId"], &conversationID)
		e.logger.Debug("codebuddy resident ACP frame", "method", frame.Method, "rpc_id", string(frame.ID), "session_id", frame.Params.SessionID, "request_id", requestID, "conversation_id", conversationID, "accepted", accept)
		if accept {
			_, _ = lease.writer.Write(append(line, '\n'))
		} else if frame.Method != "" && len(frame.ID) > 0 {
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": map[string]any{"code": -32000, "message": "No active CodeBuddy task owns this request"}})
			e.writeMu.Lock()
			_, _ = e.stdin.Write(append(reply, '\n'))
			e.writeMu.Unlock()
		}
	}
}

func (l *codebuddyProcessLease) Write(data []byte) (int, error) {
	var frame struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(data, &frame) != nil {
		return 0, errors.New("invalid CodeBuddy ACP request")
	}
	e := l.entry
	e.logger.Debug("codebuddy resident ACP write", "method", frame.Method, "rpc_id", string(frame.ID))
	e.mu.Lock()
	if e.active != l || l.closing {
		e.mu.Unlock()
		return 0, errors.New("CodeBuddy lease is no longer active")
	}
	cached := append(json.RawMessage(nil), e.initialize...)
	if frame.Method == "initialize" && len(cached) > 0 {
		e.mu.Unlock()
		reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": cached})
		if _, err := l.writer.Write(append(reply, '\n')); err != nil {
			return 0, err
		}
		return len(data), nil
	}
	if frame.Method != "" && len(frame.ID) > 0 {
		var id int
		if json.Unmarshal(frame.ID, &id) != nil {
			e.mu.Unlock()
			return 0, errors.New("invalid CodeBuddy client request ID")
		}
		l.pending[id] = frame.Method
	}
	e.mu.Unlock()
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if pipe, ok := e.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = pipe.SetWriteDeadline(time.Now().Add(8 * time.Second))
	}
	return e.stdin.Write(data)
}

func (l *codebuddyProcessLease) Close() error {
	l.entry.mu.Lock()
	l.closing = true
	l.entry.mu.Unlock()
	_ = l.reader.Close()
	return l.writer.Close()
}

func (l *codebuddyProcessLease) release(healthy bool) error {
	l.once.Do(func() {
		if l.entry.taskProcess {
			healthy = false
		}
		_ = l.Close()
		p, e := l.pool, l.entry
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.entries[e.key] != e {
			if p.unhealthy.Load() {
				l.releaseErr = errors.New("CodeBuddy resident cleanup could not be confirmed")
			}
			return
		}
		e.mu.Lock()
		e.active = nil
		e.mu.Unlock()
		if err := reapCodexResidentExecution(l.execution); err != nil {
			healthy = false
		}
		if err := e.hooks.end(); err != nil {
			healthy = false
		}
		if !healthy || p.closed {
			delete(p.entries, e.key)
			p.stop(e)
			if p.unhealthy.Load() {
				l.releaseErr = errors.New("CodeBuddy resident cleanup could not be confirmed")
			}
			p.signalLocked()
			return
		}
		if err := writeCodebuddyPoolRecord(e.dir, e.hooks.dir, e.owner, "", "", e.cmd.Process.Pid); err != nil {
			delete(p.entries, e.key)
			p.stop(e)
			if p.unhealthy.Load() {
				l.releaseErr = errors.New("CodeBuddy resident cleanup could not be confirmed")
			}
			p.signalLocked()
			return
		}
		e.mu.Lock()
		e.execution = ""
		e.mu.Unlock()
		e.stderr.detach(l.stderr)
		e.busy, e.lastUsed = false, time.Now()
		released := e.lastUsed
		p.signalLocked()
		time.AfterFunc(p.idle, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.entries[e.key] == e && !e.busy && e.lastUsed == released {
				delete(p.entries, e.key)
				p.stop(e)
				p.signalLocked()
			}
		})
	})
	return l.releaseErr
}

func codebuddyResidentFingerprint(cfg Config, opts ExecOptions) (string, error) {
	stable := opts
	stable.CodebuddySettingsPath = ""
	rootEnv := codexResidentEnvironment(cfg.Env)
	sort.Strings(rootEnv)
	args := codebuddyNativeArgs(stable, cfg)
	identity, _ := json.Marshal([]any{cfg.ExecutablePath, cfg.LaunchPrefix, cfg.CLIVersion, args, rootEnv})
	h := sha256.New()
	_, _ = h.Write(identity)
	if info, err := os.Stat(cfg.ExecutablePath); err == nil {
		_, _ = fmt.Fprintf(h, "%d:%d:%d", info.Size(), info.ModTime().UnixNano(), info.Mode())
	}
	if opts.CodebuddySettingsPath != "" {
		raw, ok := readRegularFileBounded(opts.CodebuddySettingsPath)
		if !ok {
			return "", errors.New("CodeBuddy runtime settings are unavailable")
		}
		_, _ = h.Write(raw)
	}
	home := resolveSDKConfigDir(cfg.Env)
	for _, name := range []string{"settings.json", "config.json", "auth.json", "credentials.json", ".credentials.json", ".mcp.json", "mcp.json", "skills", "plugins/cache", "plugins/installed_plugins.json", "rules"} {
		if err := hashCodexResidentSkills(h, filepath.Join(home, name), "account/"+name, map[string]bool{}); err != nil {
			return "", err
		}
	}
	userHome := cfg.Env["HOME"]
	if userHome == "" {
		userHome, _ = os.UserHomeDir()
	}
	if userHome == "" {
		return "", errors.New("CodeBuddy account home is unavailable")
	}
	if err := hashCodebuddyResidentAuth(h, filepath.Join(userHome, ".local", "share", "CodeBuddyExtension", "Data", "Public", "auth")); err != nil {
		return "", err
	}
	if err := hashCodebuddyResidentStorage(h, filepath.Join(home, "local_storage")); err != nil {
		return "", err
	}
	marketplaces := filepath.Join(home, "plugins", "known_marketplaces.json")
	if raw, err := os.ReadFile(marketplaces); err == nil {
		var sources map[string]map[string]json.RawMessage
		if json.Unmarshal(raw, &sources) != nil {
			return "", errors.New("invalid CodeBuddy marketplace registry")
		}
		for _, source := range sources {
			delete(source, "lastUpdated")
		}
		stable, err := json.Marshal(sources)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(stable)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	for dir := opts.Cwd; dir != ""; dir = filepath.Dir(dir) {
		for _, name := range []string{"CODEBUDDY.md", ".codebuddy/settings.json", ".codebuddy/settings.local.json", ".codebuddy/skills", ".mcp.json"} {
			if err := hashCodexResidentSkills(h, filepath.Join(dir, name), dir+"/"+name, map[string]bool{}); err != nil {
				return "", err
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// 原生认证服务拥有同账号票据的刷新。租约绑定账号、组织、权限及认证配置，
// 历史账号列表和可轮换票据不决定进程身份；未知认证格式按完整字节绑定。
func hashCodebuddyResidentAuth(out io.Writer, root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.Name() != "Tencent-Cloud.coding-copilot.info" {
			if err := hashCodexResidentSkills(out, path, "account/auth/"+entry.Name(), map[string]bool{}); err != nil {
				return err
			}
			continue
		}
		raw, ok := readRegularFileBounded(path)
		if !ok {
			return errors.New("CodeBuddy authentication source is unavailable")
		}
		var document map[string]any
		if json.Unmarshal(raw, &document) != nil {
			return errors.New("invalid CodeBuddy authentication source")
		}
		account, _ := document["account"].(map[string]any)
		uid, _ := account["uid"].(string)
		if uid == "" {
			_, _ = out.Write(raw)
			continue
		}
		identity := map[string]any{}
		for _, key := range []string{"uid", "uin", "type", "enterpriseId", "accountType", "idp", "oneidAccountId", "isCurrentOneIdEnterprise", "isCurrentOneIdPersonal", "isAdmin", "isCreator", "pluginEnabled"} {
			if value, exists := account[key]; exists {
				identity[key] = value
			}
		}
		auth, _ := document["auth"].(map[string]any)
		configuration := map[string]any{}
		for key, value := range auth {
			switch key {
			case "accessToken", "refreshToken", "expiresIn", "refreshExpiresIn", "lastRefreshTime", "expiresAt", "refreshExpiresAt", "sessionState":
				continue
			}
			configuration[key] = value
		}
		stable, err := json.Marshal([]any{identity, configuration})
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "account/auth/%s:%o\n", entry.Name(), info.Mode().Perm())
		_, _ = out.Write(stable)
	}
	return nil
}

// 2.176 的 ab_test_snapshot 使用键的 MD5 作为文件名。updatedAt 是快照写入
// 时刻；功能配置和其它存储（包括认证）全部参与指纹。
func hashCodebuddyResidentStorage(out io.Writer, root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read CodeBuddy account storage")
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		// 产品目录是 CLI 从云端刷新的派生缓存；账户认证来自独立 auth 目录。
		if entry.Name() == "entry_604f48c944053e01d9546675443286c1.info" || entry.Name() == "entry_d43e96994f944cfb77961c2ea7d04605.info" {
			continue
		}
		if entry.Name() != "entry_4348632e75ae0692960c04ccaf0a17df.info" {
			if err := hashCodexResidentSkills(out, path, "account/local_storage/"+entry.Name(), map[string]bool{}); err != nil {
				return err
			}
			continue
		}
		raw, ok := readRegularFileBounded(path)
		if !ok {
			return errors.New("CodeBuddy feature cache is unavailable")
		}
		var snapshot map[string]any
		if json.Unmarshal(raw, &snapshot) != nil || snapshot == nil {
			return errors.New("invalid CodeBuddy feature cache")
		}
		delete(snapshot, "updatedAt")
		stable, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "account/local_storage/%s:%o\n", entry.Name(), info.Mode().Perm())
		_, _ = out.Write(stable)
	}
	return nil
}

// 受管 stdio MCP 的任务身份由当前租约提供；用户的其它显式环境仍由其配置决定。
func codebuddyResidentMCP(servers []any, cfg Config, execution string) []any {
	result := make([]any, 0, len(servers))
	for _, raw := range servers {
		server := raw.(map[string]any)
		copy := make(map[string]any, len(server))
		for k, v := range server {
			copy[k] = v
		}
		if _, stdio := server["command"]; stdio {
			values := map[string]any{}
			if list, ok := server["env"].([]map[string]any); ok {
				for _, pair := range list {
					values[pair["name"].(string)] = pair["value"]
				}
			}
			for k, v := range cfg.Env {
				upper := strings.ToUpper(k)
				if strings.HasPrefix(upper, "MULTICA_") || upper == "TMPDIR" || upper == "TMP" || upper == "TEMP" {
					values[k] = v
				}
			}
			values[codexExecutionOwnerEnv] = execution
			var keys []string
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			list := make([]map[string]any, 0, len(keys))
			for _, k := range keys {
				list = append(list, map[string]any{"name": k, "value": values[k]})
			}
			copy["env"] = list
		}
		result = append(result, copy)
	}
	return result
}

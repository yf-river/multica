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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// CodexProcessPool 拥有 daemon 的原生 app-server。任务只持有独占会话租约，
// 每轮 WebSocket 连接结束不会终止已卸载全部线程的原生进程。
type CodexProcessPool struct {
	mu        sync.Mutex
	root      string
	capacity  int
	idle      time.Duration
	logger    *slog.Logger
	entries   map[string]*codexPoolEntry
	changed   chan struct{}
	closed    bool
	wg        sync.WaitGroup
	lock      io.Closer
	unhealthy atomic.Bool
}

type codexPoolEntry struct {
	key, fingerprint, socket, dir string
	cmd                           *exec.Cmd
	done                          chan struct{}
	busy                          bool
	lastUsed                      time.Time
	unpin                         func()
	stderr                        *codexPoolStderr
	owner, home                   string
}

type codexProcessLease struct {
	pool         *CodexProcessPool
	entry        *codexPoolEntry
	threadConfig map[string]any
	execution    string
	stderr       *stderrTail
	reused       bool
	once         sync.Once
}

func CodexResidentSupported(version string) bool {
	parsed, err := parseSemver(version)
	return err == nil && !parsed.lessThan(semver{Minor: 164})
}

func NewCodexProcessPool(root string, capacity int, idle time.Duration, logger *slog.Logger) (*CodexProcessPool, error) {
	if capacity < 1 || idle <= 0 || !filepath.IsAbs(root) {
		return nil, errors.New("invalid Codex process pool configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&0077 != 0 {
		return nil, errors.New("Codex process pool requires a private directory")
	}
	p := &CodexProcessPool{root: root, capacity: capacity, idle: idle, logger: logger, entries: make(map[string]*codexPoolEntry), changed: make(chan struct{})}
	p.lock, err = lockCodexPoolRoot(root)
	if err != nil {
		return nil, err
	}
	if err := recoverCodexPoolProcesses(root); err != nil {
		_ = p.lock.Close()
		return nil, err
	}
	return p, nil
}

func (p *CodexProcessPool) signalLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// Close 在 daemon 已等待任务收尾后调用；超时任务留下的进程也由同一所有者回收。
func (p *CodexProcessPool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	entries := make([]*codexPoolEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		entries = append(entries, entry)
	}
	p.entries = make(map[string]*codexPoolEntry)
	p.signalLocked()
	p.mu.Unlock()
	for _, entry := range entries {
		p.stop(entry)
	}
	p.wg.Wait()
	_ = p.lock.Close()
}

func (p *CodexProcessPool) stop(entry *codexPoolEntry) {
	select {
	case <-entry.done:
	default:
		signalProcessGroup(entry.cmd, syscall.SIGKILL)
	}
	ownerErr := reapCodexResidentOwner(entry.owner)
	<-entry.done
	if ownerErr != nil || !waitProcessGroupGone(entry.cmd, 3*time.Second) {
		p.unhealthy.Store(true)
		p.logger.Error("Codex resident process group did not exit", "pid", entry.cmd.Process.Pid)
		return
	}
	if entry.unpin != nil {
		entry.unpin()
	}
	_ = os.RemoveAll(entry.dir)
	_ = os.RemoveAll(filepath.Dir(entry.socket))
	releaseProcessGroup(entry.cmd)
}

func (p *CodexProcessPool) acquire(ctx context.Context, cfg Config, opts ExecOptions, command Command, args []string) (*codexProcessLease, error) {
	home := cfg.Env["CODEX_HOME"]
	config, fingerprint, err := codexResidentConfig(cfg, opts, command, args)
	if err != nil {
		return nil, err
	}
	var executionBytes [16]byte
	if _, err := rand.Read(executionBytes[:]); err != nil {
		return nil, err
	}
	execution := hex.EncodeToString(executionBytes[:])
	config["shell_environment_policy"].(map[string]any)["set"].(map[string]any)[codexExecutionOwnerEnv] = execution
	if servers, ok := config["mcp_servers"].(map[string]any); ok {
		for _, raw := range servers {
			if server, ok := raw.(map[string]any); ok {
				if _, stdio := server["command"].(string); stdio {
					server["env"].(map[string]any)[codexExecutionOwnerEnv] = execution
				}
			}
		}
	}
	state := cfg.Env["CODEX_SQLITE_HOME"]
	if state == "" {
		state = home
	}
	keyBytes := sha256.Sum256([]byte(cfg.ResidentSessionKey + "\x00" + opts.Cwd + "\x00" + state))
	key := hex.EncodeToString(keyBytes[:])
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.closed || p.unhealthy.Load() {
			p.mu.Unlock()
			return nil, errors.New("Codex process pool is closed")
		}
		entry := p.entries[key]
		if entry != nil && entry.busy {
			changed := p.changed
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
				continue
			}
		}
		if entry != nil {
			select {
			case <-entry.done:
				entry.fingerprint = ""
			default:
			}
			if entry.fingerprint != fingerprint {
				delete(p.entries, key)
				// 保留池锁到回收完成，避免同一逻辑会话同时存在两个原生所有者。
				p.stop(entry)
				entry = nil
			}
		}
		if entry != nil {
			entry.busy = true
			if err := writeCodexPoolProcessRecord(entry.dir, entry.socket, entry.home, entry.owner, cfg.TaskID, execution, entry.cmd.Process.Pid); err != nil {
				delete(p.entries, key)
				p.stop(entry)
				p.signalLocked()
				p.mu.Unlock()
				return nil, err
			}
			p.mu.Unlock()
			return &codexProcessLease{pool: p, entry: entry, threadConfig: config, execution: execution, stderr: entry.stderr.begin(), reused: true}, nil
		}
		if len(p.entries) >= p.capacity {
			var oldest *codexPoolEntry
			for _, candidate := range p.entries {
				if !candidate.busy && (oldest == nil || candidate.lastUsed.Before(oldest.lastUsed)) {
					oldest = candidate
				}
			}
			if oldest != nil {
				delete(p.entries, oldest.key)
				p.stop(oldest)
			} else {
				changed := p.changed
				p.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-changed:
					continue
				}
			}
		}
		entry, err = p.start(ctx, key, fingerprint, execution, cfg, opts, command, args)
		if err != nil {
			p.signalLocked()
			p.mu.Unlock()
			return nil, err
		}
		p.entries[key] = entry
		p.mu.Unlock()
		return &codexProcessLease{pool: p, entry: entry, threadConfig: config, execution: execution, stderr: entry.stderr.current}, nil
	}
}

// RetireHome 在原生状态迁移前收齐旧 home 的原生进程，调用者仍持有会话 writer lock。
func (p *CodexProcessPool) RetireHome(ctx context.Context, home string) error {
	if p == nil {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.mu.Lock()
		busy := false
		for key, entry := range p.entries {
			if entry.home != home {
				continue
			}
			if entry.busy {
				busy = true
				continue
			}
			delete(p.entries, key)
			p.stop(entry)
			p.signalLocked()
		}
		changed := p.changed
		unhealthy := p.unhealthy.Load()
		p.mu.Unlock()
		if unhealthy {
			return errors.New("Codex state migration could not confirm native process cleanup")
		}
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (p *CodexProcessPool) start(ctx context.Context, key, fingerprint, execution string, cfg Config, opts ExecOptions, command Command, args []string) (*codexPoolEntry, error) {
	dir := filepath.Join(p.root, key)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	// Unix socket 有路径长度限制；私有短目录的地址只存在受保护的进程记录中。
	socketDir, err := os.MkdirTemp("", "multica-codex-")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	socket := filepath.Join(socketDir, "rpc.sock")
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	owner := hex.EncodeToString(nonce[:])
	if err := writeCodexPoolProcessRecord(dir, socket, cfg.Env["CODEX_HOME"], owner, cfg.TaskID, execution, 0); err != nil {
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	launchArgs := append([]string(nil), args...)
	launchArgs[2] = "unix://" + socket
	cmd := command.exec(context.Background(), launchArgs...)
	cacheHome := filepath.Join(dir, "home")
	sqliteHome, err := prepareCodexResidentHome(cfg.Env["CODEX_HOME"], cacheHome, cfg.Env["CODEX_SQLITE_HOME"])
	if err != nil {
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	rootEnv := make(map[string]string, len(cfg.Env)+2)
	for key, value := range cfg.Env {
		rootEnv[key] = value
	}
	rootEnv["CODEX_HOME"], rootEnv["CODEX_SQLITE_HOME"] = cacheHome, sqliteHome
	cmd.Dir, cmd.Env = opts.Cwd, codexResidentEnvironment(rootEnv)
	cmd.Env = append(cmd.Env, codexPoolOwnerEnv+"="+owner)
	configureCodexResidentOwner(cmd)
	stderr := &codexPoolStderr{current: newStderrTail(io.Discard, codexStderrTailBytes)}
	cmd.Stderr, cmd.Stdout = stderr, io.Discard
	if err := startOwnedProcessTree(cmd, p.logger); err != nil {
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	entry := &codexPoolEntry{key: key, fingerprint: fingerprint, socket: socket, dir: dir, cmd: cmd, done: make(chan struct{}), busy: true, stderr: stderr, owner: owner, home: cfg.Env["CODEX_HOME"]}
	if cfg.PinResidentHome != nil {
		entry.unpin = cfg.PinResidentHome()
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		_ = cmd.Wait()
		close(entry.done)
	}()
	if err := writeCodexPoolProcessRecord(dir, socket, cfg.Env["CODEX_HOME"], owner, cfg.TaskID, execution, cmd.Process.Pid); err != nil {
		p.stop(entry)
		return nil, err
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		conn, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return entry, nil
		}
		select {
		case <-ctx.Done():
			p.stop(entry)
			return nil, ctx.Err()
		case <-deadline.C:
			p.stop(entry)
			return nil, errors.New("Codex resident socket startup timed out")
		case <-entry.done:
			p.stop(entry)
			return nil, errors.New("Codex resident process exited before listening")
		case <-poll.C:
		}
	}
}

func (lease *codexProcessLease) release(healthy bool) {
	lease.once.Do(func() {
		p, entry := lease.pool, lease.entry
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.entries[entry.key] != entry {
			return
		}
		if !healthy || p.closed {
			delete(p.entries, entry.key)
			p.stop(entry)
			p.signalLocked()
			return
		}
		if err := writeCodexPoolProcessRecord(entry.dir, entry.socket, entry.home, entry.owner, "", "", entry.cmd.Process.Pid); err != nil {
			delete(p.entries, entry.key)
			p.stop(entry)
			p.signalLocked()
			return
		}
		entry.stderr.detach(lease.stderr)
		entry.busy, entry.lastUsed = false, time.Now()
		releasedAt := entry.lastUsed
		p.signalLocked()
		time.AfterFunc(p.idle, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.entries[entry.key] == entry && !entry.busy && entry.lastUsed.Equal(releasedAt) {
				delete(p.entries, entry.key)
				p.stop(entry)
				p.signalLocked()
			}
		})
	})
}

func codexResidentEnvironment(env map[string]string) []string {
	merged := buildEnv(env)
	base := make([]string, 0, len(merged))
	for _, field := range merged {
		key, _, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(key) {
		case "TMPDIR", "TMP", "TEMP":
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(key), "MULTICA_") {
			base = append(base, field)
		}
	}
	return base
}

// 当前任务配置走线程 RPC，原生常驻进程的环境里没有任务身份与凭据。
func codexResidentConfig(cfg Config, opts ExecOptions, command Command, args []string) (map[string]any, string, error) {
	home := cfg.Env["CODEX_HOME"]
	if cfg.ResidentSessionKey == "" || cfg.TaskID == "" || home == "" {
		return nil, "", errors.New("Codex resident execution requires a logical session and task home")
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return nil, "", errors.New("cannot read current Codex task configuration")
	}
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, "", errors.New("cannot parse current Codex task configuration")
	}
	if config == nil {
		config = make(map[string]any)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, "", errors.New("Codex task configuration cannot be sent through native RPC")
	}
	var stable map[string]any
	_ = json.Unmarshal(encoded, &stable)
	delete(stable, "mcp_servers")
	delete(stable, "shell_environment_policy")
	// 全部 MCP 在当前线程重建；env_vars 的任务值不能从常驻进程的环境读取。
	if servers, ok := config["mcp_servers"].(map[string]any); ok {
		for _, raw := range servers {
			server, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if _, stdio := server["command"].(string); stdio {
				env, _ := server["env"].(map[string]any)
				if env == nil {
					env = make(map[string]any)
				}
				if names, ok := server["env_vars"].([]any); ok {
					for _, rawName := range names {
						if name, ok := rawName.(string); ok {
							if value, exists := cfg.Env[name]; exists {
								env[name] = value
							}
						}
					}
				}
				for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
					if value, exists := cfg.Env[name]; exists {
						env[name] = value
					}
				}
				server["env"] = env
			}
			headers, _ := server["http_headers"].(map[string]any)
			if headers == nil {
				headers = make(map[string]any)
			}
			if name, ok := server["bearer_token_env_var"].(string); ok {
				if value, exists := cfg.Env[name]; exists {
					headers["Authorization"] = "Bearer " + value
					delete(server, "bearer_token_env_var")
				}
			}
			if names, ok := server["env_http_headers"].(map[string]any); ok {
				for header, rawName := range names {
					if name, ok := rawName.(string); ok {
						if value, exists := cfg.Env[name]; exists {
							headers[header] = value
							delete(names, header)
						}
					}
				}
			}
			if len(headers) > 0 {
				server["http_headers"] = headers
			}
		}
	}
	policy, _ := config["shell_environment_policy"].(map[string]any)
	if policy == nil {
		policy = make(map[string]any)
	}
	set, _ := policy["set"].(map[string]any)
	if set == nil {
		set = make(map[string]any)
	}
	for key := range set {
		if strings.HasPrefix(strings.ToUpper(key), "MULTICA_") || key == "TMPDIR" || key == "TMP" || key == "TEMP" || key == "CODEX_HOME" {
			delete(set, key)
		}
	}
	for key, value := range cfg.Env {
		set[key] = value
	}
	policy["set"] = set
	config["shell_environment_policy"] = policy
	config["thread_unload_delay_secs"] = int64(0)

	h := sha256.New()
	auth, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", errors.New("cannot bind current Codex account")
	}
	_, _ = h.Write(auth)
	rootEnv := codexResidentEnvironment(cfg.Env)
	// 任务 home 只提供当前线程资源；稳定 SQLite 已接好时，home 路径不决定进程身份。
	if cfg.Env["CODEX_SQLITE_HOME"] != "" {
		for index, field := range rootEnv {
			if strings.HasPrefix(field, "CODEX_HOME=") {
				rootEnv[index] = "CODEX_HOME=<task-home>"
			}
		}
	}
	sort.Strings(rootEnv)
	identity, _ := json.Marshal([]any{stable, command.Path, command.Prefix, args, rootEnv, cfg.CodexVersion})
	_, _ = h.Write(identity)
	if info, err := os.Stat(command.Path); err == nil {
		_, _ = fmt.Fprintf(h, "%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	for _, name := range []string{"skills", "plugins", "rules"} {
		if err := hashCodexResidentSkills(h, filepath.Join(home, name), name, make(map[string]bool)); err != nil {
			return nil, "", err
		}
	}
	for _, name := range []string{"config.json", "instructions.md"} {
		body, err := os.ReadFile(filepath.Join(home, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, "", errors.New("cannot bind current Codex user instructions")
		}
		_, _ = fmt.Fprintln(h, name)
		_, _ = h.Write(body)
	}
	return config, hex.EncodeToString(h.Sum(nil)), nil
}

// 常驻 home 只含账号与静态配置；会话和 SQLite 仍写回任务原有的持久位置。
func prepareCodexResidentHome(source, destination, envSQLiteHome string) (string, error) {
	if err := os.Mkdir(destination, 0700); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(source, "config.toml"))
	if err != nil {
		return "", errors.New("cannot read Codex resident source configuration")
	}
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		return "", errors.New("cannot parse Codex resident source configuration")
	}
	if config == nil {
		config = make(map[string]any)
	}
	delete(config, "mcp_servers")
	delete(config, "shell_environment_policy")
	sqliteHome, _ := config["sqlite_home"].(string)
	if sqliteHome == "" {
		sqliteHome = envSQLiteHome
	}
	if sqliteHome == "" {
		sqliteHome = source
	}
	config["sqlite_home"] = sqliteHome
	data, err = toml.Marshal(config)
	if err != nil {
		return "", errors.New("cannot encode sanitized Codex resident configuration")
	}
	if err := os.WriteFile(filepath.Join(destination, "config.toml"), data, 0600); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(source, "sessions"), 0700); err != nil {
		return "", err
	}
	for _, name := range []string{"auth.json", "config.json", "instructions.md", "models_cache.json", "skills", "plugins", "sessions", "rules"} {
		path, err := filepath.EvalSymlinks(filepath.Join(source, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", errors.New("cannot resolve Codex resident account resources")
		}
		if err := os.Symlink(path, filepath.Join(destination, name)); err != nil {
			return "", err
		}
	}
	return sqliteHome, nil
}

func hashCodexResidentSkills(out io.Writer, path, relative string, visited map[string]bool) error {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot resolve Codex skill configuration")
	}
	return hashCodexResidentResource(out, resolved, relative, visited)
}

// 父目录已解析，普通子项直接读取元数据；只有链接需要再次解析完整路径。
// 每轮仍读取全部文件正文，内容及权限变化都参与进程配置指纹。
func hashCodexResidentResource(out io.Writer, resolved, relative string, visited map[string]bool) error {
	info, err := os.Lstat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read Codex skill configuration")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return hashCodexResidentSkills(out, resolved, relative, visited)
	}
	_, _ = fmt.Fprintf(out, "%s:%o\n", relative, info.Mode().Perm())
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return errors.New("Codex static resource is not a regular file")
		}
		file, err := os.Open(resolved)
		if err != nil {
			return errors.New("cannot read Codex static resource")
		}
		_, copyErr := io.Copy(out, file)
		return errors.Join(copyErr, file.Close())
	}
	if visited[resolved] {
		return nil
	}
	visited[resolved] = true
	children, err := os.ReadDir(resolved)
	if err != nil {
		return errors.New("cannot enumerate Codex skill configuration")
	}
	for _, child := range children {
		if child.Name() == "node_modules" || child.Name() == ".git" || child.Name() == ".venv" || child.Name() == "__pycache__" {
			continue
		}
		if err := hashCodexResidentResource(out, filepath.Join(resolved, child.Name()), filepath.Join(relative, child.Name()), visited); err != nil {
			return err
		}
	}
	return nil
}

// 原生 stderr 按租约切换，上一轮的目录刷新失败不能污染下一轮的重试判断。
type codexPoolStderr struct {
	mu      sync.Mutex
	current *stderrTail
}

func (s *codexPoolStderr) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		return s.current.Write(data)
	}
	return len(data), nil
}
func (s *codexPoolStderr) begin() *stderrTail {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = newStderrTail(io.Discard, codexStderrTailBytes)
	return s.current
}

func (s *codexPoolStderr) detach(tail *stderrTail) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == tail {
		s.current = nil
	}
}

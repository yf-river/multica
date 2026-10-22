package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// HealthResponse 是 daemon 本地健康端点返回的响应。
type HealthResponse struct {
	Status string `json:"status"`
	PID    int    `json:"pid"`
	// OS 是 daemon 的 runtime.GOOS。调用方在跨转发连接做生命周期操作前，可先把它与
	// 自己主机的系统对比（例如从 Windows 主机连到一个 Linux daemon）。见 #3916。
	OS     string `json:"os"`
	Uptime string `json:"uptime"`
	// Profile 是启动本 daemon 所用的 CLI profile 名；默认 profile 为空。健康端口是把
	// profile 名哈希进一个 1000 端口区间推出来的，因此两个名字可能撞出同一个端口，而
	// 调用方没有别的办法分辨应答的是谁：`--profile a daemon stop` 会顺手把 profile b
	// 的 daemon 杀掉（#6694）。
	//
	// 故意不写 omitempty。空串是一个真实答案——“我是默认 profile 的 daemon”——必须
	// 和 #6694 之前“完全无法自报家门”的老 daemon 区分开。调用方以该字段是否存在为
	// 判断依据，把两者合并会让每个默认 daemon 都显得无法辨认。
	Profile    string `json:"profile"`
	DaemonID   string `json:"daemon_id"`
	DeviceName string `json:"device_name"`
	ServerURL  string `json:"server_url"`
	CLIVersion string `json:"cli_version"`
	// ActiveTaskCount 保持对每个已认领 handleTask 生命周期的“兼容/安全总数”。后面那
	// 几个可累加计数器则是为了把“真实 provider 执行”与“本地目录停放”分开，便于看
	// 吞吐与诊断。
	ActiveTaskCount       int64 `json:"active_task_count"`
	RunningTaskCount      int64 `json:"running_task_count"`
	ResourceWaitTaskCount int64 `json:"resource_wait_task_count"`
	// 仓库维护是“不影响存活性”的后台活动，因此健康仍是 HTTP 200/running。下面这些
	// 可累加计数器向运维解释仓库检出能力为何下降，同时不暴露本地缓存路径。
	RepoMaintenanceActive int      `json:"repo_maintenance_active,omitempty"`
	RepoCheckoutWaiters   int      `json:"repo_checkout_waiters,omitempty"`
	Agents                []string `json:"agents"`
	// SkippedAgents 记录“本机确实发现了某 provider，但上一轮注册把它丢掉”的原因
	// （版本探不出来、低于最低支持版本）。纯诊断用途，为空时省略，让老消费端看不出
	// 变化。
	//
	// 没有它，“CLI 没装”和“装了但被拒”都会渲染成 runtime 缺席——这正是 GH #6077
	// 让反馈者无从下手的原因（MUL-5439）。
	SkippedAgents map[string]string `json:"skipped_agents,omitempty"`
	// ReloadPendingReason 解释 daemon 为何已确认磁盘上的 multica 版本变了却还没重启
	// 进去——上次屏障检查时正忙，空闲后会重试。为空时省略，老消费端看不出变化。仅
	// 诊断用途：没有任何逻辑依赖它。
	ReloadPendingReason string            `json:"reload_pending_reason,omitempty"`
	Workspaces          []healthWorkspace `json:"workspaces"`
}

// healthWorkspace 是健康响应里单个 workspace 及其 runtime 列表的形态。
type healthWorkspace struct {
	ID       string   `json:"id"`
	Runtimes []string `json:"runtimes"`
}

// listenHealth 绑定健康端口。返回监听器；若端口被占（另一个 daemon 已在运行）则报错。
func (d *Daemon) listenHealth() (net.Listener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", d.cfg.HealthPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("another daemon is already running on %s: %w", addr, err)
	}
	return ln, nil
}

// repoCheckoutRequest 是 POST /repo/checkout 请求的请求体。
type repoCheckoutRequest struct {
	URL          string `json:"url"`
	WorkspaceID  string `json:"workspace_id"`
	WorkDir      string `json:"workdir"`
	Ref          string `json:"ref,omitempty"`
	AgentName    string `json:"agent_name"`
	TaskID       string `json:"task_id"`
	CheckoutMode string `json:"checkout_mode,omitempty"`
	// RetryBusy 由“认识 503 + Retry-After”的客户端发送。老客户端省略它，沿用历史上
	// 无限期等锁的行为。
	RetryBusy bool `json:"retry_busy,omitempty"`
}

// activeRepoCheckoutTask 是登记在册、正被某个活动任务持有的仓库检出身份。
type activeRepoCheckoutTask struct {
	WorkspaceID string
	TaskID      string
	AgentID     string
	AgentName   string
	WorkDir     string
}

// registerActiveRepoCheckoutTask 把检出身份绑定到活动任务上。token 防止本机的
// 未认证调用方冒用别的任务身份或 workdir。它不是操作系统用户级隔离边界：能偷走子
// 进程环境里 token 的另一进程可冒充该任务，而它本就持有该任务的 API 凭据。
func (d *Daemon) registerActiveRepoCheckoutTask(token string, task activeRepoCheckoutTask) {
	d.repoCheckoutTasksMu.Lock()
	defer d.repoCheckoutTasksMu.Unlock()
	if d.repoCheckoutTasks == nil {
		d.repoCheckoutTasks = make(map[string]activeRepoCheckoutTask)
	}
	d.repoCheckoutTasks[token] = task
}

// clearActiveRepoCheckoutTask 在任务结束时摘掉 token 绑定，使该 token 立即失效。
func (d *Daemon) clearActiveRepoCheckoutTask(token string) {
	d.repoCheckoutTasksMu.Lock()
	delete(d.repoCheckoutTasks, token)
	d.repoCheckoutTasksMu.Unlock()
}

// repoCheckoutAuthResult 区分 /repo/checkout 鉴权失败的两种方式。两者在调用方眼里
// 长得一样，但修法完全不同——把它们并成一个 401 正是 #7520 花了 13 小时才定位的
// 原因。
type repoCheckoutAuthResult int

const (
	repoCheckoutAuthOK repoCheckoutAuthResult = iota
	// repoCheckoutAuthNoCredential: 请求没带可用的 Authorization 头。当前 CLI 总是
	// 会发，所以实际遇到的多是比 repoCheckoutMinCLIVersion 更老的 `multica` 二进制
	// ——永久性失败，重试也救不了。
	repoCheckoutAuthNoCredential
	// repoCheckoutAuthUnknownCredential: 带了一个 token，但它没有绑定到“跑在本
	// daemon 里”的任务——该任务已经结束，或 daemon 在 agent 进程仍存活时重启过。
	repoCheckoutAuthUnknownCredential
)

// repoCheckoutMinCLIVersion 是首个让 `multica repo checkout` 发送本端点所需
// Authorization 头的版本。请求头与这个校验落在同一个提交里（#7205），因此无论任务
// 多健康，比它老的 CLI 都会在这里被拒。
const repoCheckoutMinCLIVersion = "v0.4.30"

// activeRepoCheckoutTask 从请求里解析 Bearer token 并查出绑定的活动任务，同时区分
// “没带凭据”与“凭据不认识”两类鉴权失败。
func (d *Daemon) activeRepoCheckoutTask(r *http.Request) (activeRepoCheckoutTask, repoCheckoutAuthResult) {
	const bearer = "Bearer "
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, bearer) {
		return activeRepoCheckoutTask{}, repoCheckoutAuthNoCredential
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, bearer))
	if token == "" {
		return activeRepoCheckoutTask{}, repoCheckoutAuthNoCredential
	}
	d.repoCheckoutTasksMu.RLock()
	task, ok := d.repoCheckoutTasks[token]
	d.repoCheckoutTasksMu.RUnlock()
	if !ok {
		return activeRepoCheckoutTask{}, repoCheckoutAuthUnknownCredential
	}
	return task, repoCheckoutAuthOK
}

// repoCheckoutListBinariesCommand 给出对应平台的“列出 PATH 上每个副本”的命令。
// 被拒的要点就是“调用方跑错了二进制”，要是再丢给它一个 shell 里不存在的命令，
// 只是把死胡同换了个位置。
func repoCheckoutListBinariesCommand() string {
	if runtime.GOOS == "windows" {
		return "where.exe multica"
	}
	return "which -a multica"
}

// repoCheckoutAuthErrorMessage 构造被拒响应体。它必须能自解释：调用方是任务里的
// agent，CLI 会把这个字符串当作整个失败信息打出来。任何没写明白的内容，都得靠 agent
// 读不到的 daemon 日志去还原。
//
// 兼容性建议故意放在这里而不是 CLI 里。会命中 repoCheckoutAuthNoCredential 的人群，
// 按定义就是“老得没收到任何客户端修复”的版本；而这条路径上唯一保证是最新版本的
// 组件，就是 daemon。
//
// 这里的每句说法都要同时经得起 Windows、Linux 与 macOS 的考验，而且必须是真的而非
// “大概率是真的”——给出读者平台上错误的建议，或断言一个无人核实的版本匹配，恰好
// 会复现本消息想要终结的那种误导。
func (d *Daemon) repoCheckoutAuthErrorMessage(result repoCheckoutAuthResult) string {
	if result == repoCheckoutAuthUnknownCredential {
		return "repo checkout credential is not bound to a task running in this daemon: " +
			"the task it belongs to has already finished, or the daemon restarted after this agent started. " +
			"Repo checkout is only available while the task that owns this workdir is still running."
	}

	var b strings.Builder
	b.WriteString("repo checkout requires an active task credential, and this request carried none. ")
	b.WriteString("The multica CLI has sent that credential since ")
	b.WriteString(repoCheckoutMinCLIVersion)
	b.WriteString(", so this request came from an older `multica` binary")
	if daemonVersion := strings.TrimSpace(d.cfg.CLIVersion); daemonVersion != "" {
		b.WriteString(" (this daemon runs ")
		b.WriteString(daemonVersion)
		b.WriteString(")")
	}
	b.WriteString(" and will fail every time, not intermittently. List every copy on PATH with `")
	b.WriteString(repoCheckoutListBinariesCommand())
	b.WriteString("`, check each one's version, then upgrade the stale one with `multica update`")
	b.WriteString(" — it handles Homebrew and direct installs on every platform.")
	// os.Executable() 报告的是本进程“从哪儿被启动”，并不承诺磁盘上现在的字节：
	// daemon 刻意支持“重启被推迟、二进制已被越权替换”的场景（见 trySelfReload）。
	// 在这里断言版本匹配，可能给版本错位的受害者递上第二份过期二进制，所以只说明
	// 来源、让读者自己去核对版本。
	if selfBin, err := resolveSelfExecutable(); err == nil {
		b.WriteString(" This daemon started from ")
		b.WriteString(selfBin)
		if pending := d.reloadPending(); pending != "" {
			b.WriteString(", whose on-disk copy has since changed (")
			b.WriteString(pending)
			b.WriteString(")")
		}
		b.WriteString("; check that copy's version before running it directly.")
	}
	return b.String()
}

// writeRepoCheckoutAuthError 拒绝请求并在 daemon.log 留痕。之前 401 分支什么都不
// 记，agent 在 stderr 看到的失败，对想查它的人完全不可见（#7520）。token 永不写
// 日志：它是活的任务凭据。
func (d *Daemon) writeRepoCheckoutAuthError(w http.ResponseWriter, result repoCheckoutAuthResult) {
	message := d.repoCheckoutAuthErrorMessage(result)
	reason := "no_credential"
	if result == repoCheckoutAuthUnknownCredential {
		reason = "unknown_credential"
	}
	d.logger.Warn("repo checkout rejected",
		"reason", reason,
		"min_cli_version", repoCheckoutMinCLIVersion,
		"daemon_version", d.cfg.CLIVersion,
	)
	http.Error(w, message, http.StatusUnauthorized)
}

// authorizeRepoCheckoutWorkDir 校验请求的 workdir 落在活动任务 workdir 之内（符号
// 链接解析后），防止越权指到别的目录；通过则返回规范化后的 workdir。
func authorizeRepoCheckoutWorkDir(activeRoot, requested string) (string, error) {
	root, err := filepath.Abs(activeRoot)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	workdir, err := filepath.Abs(requested)
	if err != nil {
		return "", err
	}
	workdir, err = filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, workdir)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("workdir is outside the active task workdir")
	}
	return workdir, nil
}

const (
	repoCheckoutLockWaitTimeout = 10 * time.Second
	repoCheckoutRetryAfter      = 2 * time.Second
	repoCheckoutRetryHeader     = "X-Multica-Retryable"
	repoCheckoutRetryValueBusy  = "repo-busy"
)

// healthHandler 返回 /health 的 HTTP 处理器。从 serveHealth 里拆出来，是为了让测试
// 不用起监听就能直接调用它。
func (d *Daemon) healthHandler(startedAt time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		var wsList []healthWorkspace
		for id, ws := range d.workspaces {
			wsList = append(wsList, healthWorkspace{
				ID:       id,
				Runtimes: ws.runtimeIDs,
			})
		}
		d.mu.Unlock()

		agents := make([]string, 0, len(d.agents()))
		for name := range d.agents() {
			agents = append(agents, name)
		}

		// preflight（PAT 续期 + 首次 workspace 同步 + runtime 注册）完成前是
		// "starting"，daemon 真正能认领任务后才是 "running"。健康端口在 preflight 前
		// 就绑定了（为了存活与诊断），所以调用方不能把“端口通”当就绪——它们靠这个
		// 状态判断。只认 "running" 的消费端会把 "starting" 安全地当未就绪。
		status := "starting"
		if d.ready.Load() {
			status = "running"
		}

		resp := HealthResponse{
			Status:                status,
			PID:                   os.Getpid(),
			OS:                    runtime.GOOS,
			Uptime:                time.Since(startedAt).Truncate(time.Second).String(),
			Profile:               d.cfg.Profile,
			DaemonID:              d.cfg.DaemonID,
			DeviceName:            d.cfg.DeviceName,
			ServerURL:             d.cfg.ServerBaseURL,
			CLIVersion:            d.cfg.CLIVersion,
			ActiveTaskCount:       d.activeTasks.Load(),
			RunningTaskCount:      d.runningTasks.Load(),
			ResourceWaitTaskCount: d.resourceWaitTasks.Load(),
			Agents:                agents,
			SkippedAgents:         d.skippedAgentsSnapshot(),

			ReloadPendingReason: d.reloadPending(),
			Workspaces:          wsList,
		}
		if reporter, ok := d.repoCache.(interface{ Activity() repocache.Activity }); ok {
			activity := reporter.Activity()
			resp.RepoMaintenanceActive = activity.MaintenanceActive
			resp.RepoCheckoutWaiters = activity.ForegroundWaiters
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// shutdownHandler 通过取消顶层 context 触发一次优雅关停。供 `multica daemon stop`
// 使用，这样就不依赖 OS 信号投递——一旦 daemon 以 DETACHED_PROCESS 方式拉起（与
// stop 调用方没有共享控制台），Windows 上的信号投递并不可靠。监听器只绑在
// 127.0.0.1，所以只有本机进程能碰到这个端点。
func (d *Daemon) shutdownHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "shutting down"})
		if d.cancelFunc != nil {
			// 异步取消，让响应先冲刷出去；否则 srv.Close() 会和写响应竞态。
			go d.cancelFunc()
		}
	}
}

// 发布排空只停止新认领，轮询状态直到已有任务与在途认领全部结束。
// DELETE 用于发布取消后的恢复；端点仅绑定本机回环地址。
func (d *Daemon) drainHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.claimMu.Lock()
		defer d.claimMu.Unlock()
		switch r.Method {
		case http.MethodPost:
			d.deploymentDrain = true
		case http.MethodDelete:
			d.deploymentDrain = false
		case http.MethodGet:
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"draining":                d.deploymentDrain,
			"release_pause_supported": true,
			"ready_to_stop":           d.deploymentDrain && d.claimsInFlight == 0 && d.activeTasks.Load() == 0,
			"active_tasks":            d.activeTasks.Load(), "claims_in_flight": d.claimsInFlight,
		})
	}
}

// serveHealth 在给定监听器上运行健康 HTTP server，阻塞直到 ctx 被取消。
func (d *Daemon) serveHealth(ctx context.Context, ln net.Listener, startedAt time.Time) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", d.healthHandler(startedAt))
	mux.HandleFunc("/shutdown", d.shutdownHandler())
	mux.HandleFunc("/drain", d.drainHandler())
	mux.HandleFunc("/repo/checkout", d.repoCheckoutHandler())

	srv := &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	d.logger.Info("health server listening", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		d.logger.Warn("health server error", "error", err)
	}
}

// repoCheckoutHandler 处理 POST /repo/checkout：先鉴权、核对任务上下文与 workdir
// 归属，再经 repo cache 完成隔离检出。
func (d *Daemon) repoCheckoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activeTask, authResult := d.activeRepoCheckoutTask(r)
		if authResult != repoCheckoutAuthOK {
			d.writeRepoCheckoutAuthError(w, authResult)
			return
		}

		var req repoCheckoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.URL = strings.TrimSpace(req.URL)
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if req.WorkspaceID == "" {
			http.Error(w, "workspace_id is required", http.StatusBadRequest)
			return
		}
		if req.WorkDir == "" {
			http.Error(w, "workdir is required", http.StatusBadRequest)
			return
		}
		if req.CheckoutMode != "" && req.CheckoutMode != repoCheckoutModeIsolated {
			http.Error(w, "invalid checkout_mode", http.StatusBadRequest)
			return
		}
		if req.WorkspaceID != activeTask.WorkspaceID || req.TaskID != activeTask.TaskID {
			http.Error(w, "repo checkout task context does not match the active task", http.StatusForbidden)
			return
		}
		authorizedWorkDir, authErr := authorizeRepoCheckoutWorkDir(activeTask.WorkDir, req.WorkDir)
		if authErr != nil {
			http.Error(w, "repo checkout workdir is not owned by the active task", http.StatusForbidden)
			return
		}
		// 身份一律取自绑定 token 的活动任务。AgentName 与其它由调用方填的字段只是
		// 兼容输入，绝不参与分支归属的决定。
		req.WorkspaceID = activeTask.WorkspaceID
		req.TaskID = activeTask.TaskID
		req.AgentName = activeTask.AgentName
		req.WorkDir = authorizedWorkDir

		if d.repoCache == nil {
			http.Error(w, "repo cache not initialized", http.StatusInternalServerError)
			return
		}

		if err := d.ensureRepoReady(r.Context(), req.WorkspaceID, req.URL); err != nil {
			if r.Context().Err() != nil {
				d.logger.Debug("repo checkout readiness cancelled", "url", req.URL, "error", err)
				return
			}
			statusCode := http.StatusInternalServerError
			if errors.Is(err, ErrRepoNotConfigured) {
				statusCode = http.StatusBadRequest
			}
			d.logger.Error("repo checkout readiness failed", "workspace_id", req.WorkspaceID, "url", req.URL, "error", err)
			http.Error(w, err.Error(), statusCode)
			return
		}

		checkoutRef := strings.TrimSpace(req.Ref)
		if checkoutRef == "" {
			checkoutRef = d.taskRepoDefaultRef(req.WorkspaceID, req.TaskID, req.URL)
		}

		params := repocache.WorktreeParams{
			WorkspaceID:         req.WorkspaceID,
			RepoURL:             req.URL,
			WorkDir:             req.WorkDir,
			Ref:                 checkoutRef,
			AgentName:           req.AgentName,
			TaskID:              req.TaskID,
			CoAuthoredByEnabled: d.workspaceCoAuthoredByEnabled(req.WorkspaceID),
			IsolatedGitMetadata: req.CheckoutMode == repoCheckoutModeIsolated,
		}
		if req.RetryBusy {
			params.LockWaitTimeout = repoCheckoutLockWaitTimeout
		}
		var result *repocache.WorktreeResult
		var err error
		if cache, ok := d.repoCache.(interface {
			CreateWorktreeContext(context.Context, repocache.WorktreeParams) (*repocache.WorktreeResult, error)
		}); ok {
			result, err = cache.CreateWorktreeContext(r.Context(), params)
		} else {
			result, err = d.repoCache.CreateWorktree(params)
		}
		if err != nil {
			if errors.Is(err, repocache.ErrRepoBusy) && req.RetryBusy {
				w.Header().Set(repoCheckoutRetryHeader, repoCheckoutRetryValueBusy)
				w.Header().Set("Retry-After", fmt.Sprintf("%.0f", repoCheckoutRetryAfter.Seconds()))
				http.Error(w, "repository is busy with another operation; retry later", http.StatusServiceUnavailable)
				return
			}
			if r.Context().Err() != nil {
				d.logger.Debug("repo checkout cancelled", "url", req.URL, "error", err)
				return
			}
			d.logger.Error("repo checkout failed", "url", req.URL, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

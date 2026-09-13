package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/processtree"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// reposDirName 是 workspaces 根目录里的裸仓库缓存目录。它是各 workspace 任务目录的
// “兄弟”而不是其中之一，因此每一次对根的遍历都必须显式决定拿它怎么办。
const reposDirName = ".repos"

// gcLoop 周期扫描本地 workspace 目录并按配置的保留策略执行清理。
func (d *Daemon) gcLoop(ctx context.Context) {
	if !d.cfg.GCEnabled {
		d.logger.Info("gc: disabled")
		return
	}
	d.logger.Info("gc: started",
		"interval", d.cfg.GCInterval,
		"ttl", d.cfg.GCTTL,
		"completed_task_ttl", d.cfg.GCCompletedTaskTTL,
		"orphan_ttl", d.cfg.GCOrphanTTL,
		"artifact_ttl", d.cfg.GCArtifactTTL,
		"repo_ttl", d.cfg.GCRepoTTL,
		"repo_maintenance_enabled", d.cfg.GCRepoMaintenanceEnabled,
		"artifact_patterns", d.cfg.GCArtifactPatterns,
		"managed_artifact_subpaths", execenv.ManagedReclaimableArtifactSubpaths(),
	)

	// 启动后短延迟先跑一次（让 daemon 完成初始化）。
	if err := sleepWithContext(ctx, 30*time.Second); err != nil {
		return
	}
	d.runGC(ctx)

	ticker := time.NewTicker(d.cfg.GCInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.runGC(ctx)
		}
	}
}

// gcStats 累计一个 GC 周期的字节数与按模式的命中数。
type gcStats struct {
	cleaned         int // 由“父生命周期或完成任务”策略整目录删除的任务目录数
	orphaned        int // 整目录删除的任务目录数（无 meta / issue 不可达）
	skipped         int // 原样保留的任务目录数
	artifactDirs    int // 至少回收了一个 artifact 的任务目录数
	artifactRemoved int // 删除的 artifact 子目录数
	storesReclaimed int // 超过 TTL 被回收的“每会话”Codex session store 数
	// hermesMemoryStoresReclaimed 与 storesReclaimed 分开计数：两类 store 装着不同的
	// 东西、走不同的 TTL，揉成一个数字会让运维两边都看不清楚。
	hermesMemoryStoresReclaimed   int            // 超过 TTL 被回收的“每 agent”Hermes memory store 数
	hermesSessionStoresReclaimed  int            // 超过 TTL 被回收的“每会话”Hermes session store 数
	repoCachesReclaimed           int            // .repos 下超过 TTL 被驱逐的裸仓库缓存数
	taskTempDirsReclaimed         int            // 属主执行结束后被回收的 temp 基下“每任务”临时目录数
	taskRootIndexEntriesReclaimed int            // 超过 orphan TTL 被回收的“被放弃的稳定根记录”与未发布条目数
	bytesReclaimed                int64          // 本周期释放的总字节数
	byPattern                     map[string]int // 配置的 basename 或受管路径标签 -> 回收计数
}

// runGC 对所有 workspace 目录执行一次 GC 扫描。
func (d *Daemon) runGC(ctx context.Context) {
	root := d.cfg.WorkspacesRoot
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		d.logger.Warn("gc: read workspaces root failed", "error", err)
		return
	}

	stats := &gcStats{byPattern: map[string]int{}}
	for _, wsEntry := range entries {
		// 跳过每个 daemon 内部的点开头目录，而不只是 .repos。workspace 目录永远是
		// UUID，所以点前缀的条目必是我们自己的缓存。把 .skill-cache 当 workspace 走，
		// 会让它的 `v1` 目录看起来像一个没有 .gc_meta.json 的任务目录，于是 orphan 路径
		// 一旦它的 mtime 超过 72h 没进新包，就会删掉整份技能包缓存。那回收了几百 KB，
		// 代价却是整包重新下载。
		if !wsEntry.IsDir() || strings.HasPrefix(wsEntry.Name(), ".") {
			continue
		}
		wsDir := filepath.Join(root, wsEntry.Name())
		d.gcWorkspace(ctx, wsDir, stats)
	}

	// 稳定根记录在物理 env root 之前发布，这样重派发不会选到另一个可读路径。如果准备
	// 阶段从未走到 ClaimEnvRoot，常规 GC 遍历就没有任务目录可回收。单独约束这些记录，
	// 且只在孤儿宽限期与“权威的终态/未找到任务检查”之后。
	rootRecordsRemoved, rootRecordsErr := execenv.PruneTaskRootIndex(root, d.cfg.GCOrphanTTL, time.Now(), func(_ string, taskID string) bool {
		if ctx.Err() != nil || d.client == nil {
			return false
		}
		status, statusErr := d.client.GetTaskGCCheck(ctx, taskID)
		if statusErr != nil {
			return isAccessNotFound(statusErr)
		}
		return isAgentTaskTerminal(status.Status)
	})
	if rootRecordsErr != nil {
		d.logger.Warn("gc: prune task root index failed", "error", rootRecordsErr)
	}
	stats.taskRootIndexEntriesReclaimed += rootRecordsRemoved

	// 先修剪所有裸仓库缓存里过期的 worktree 引用，再驱逐没人再需要的缓存。这些位于
	// 任何 workspace 目录之外，上面的任务遍历永远不会回收它们。
	d.pruneRepoWorktreesContext(ctx, root, stats)

	// 回收闲置超过 TTL 的“每 issue”Codex session store。它们位于共享的 ~/.codex home
	// 之下（WorkspacesRoot 之外），让续接能跨过任务 GC 幸存——这意味着它们需要自己的
	// 有界生命周期（MUL-4424）。
	if storesRemoved, storeBytes := execenv.PruneCodexSessionStores(d.cfg.Profile, d.cfg.GCCodexSessionTTL, time.Now(), d.reserveStoreForDeletion, d.logger); storesRemoved > 0 {
		stats.storesReclaimed += storesRemoved
		stats.bytesReclaimed += storeBytes
	}

	// “每 agent”Hermes memory store 同理：它们按设计活得比任务长（这正是修 #6638 的
	// 办法），所以被删 agent 的记忆需要自己的有界生命周期。保留期比 Codex 的长得多——
	// 这只是几个 markdown 文件，回收它们对用户是可见的失忆。
	if storesRemoved, storeBytes := execenv.PruneHermesMemoryStores(d.cfg.Profile, d.cfg.GCHermesMemoryTTL, time.Now(), d.reserveStoreForDeletion, d.logger); storesRemoved > 0 {
		stats.hermesMemoryStoresReclaimed += storesRemoved
		stats.bytesReclaimed += storeBytes
	}

	// 以及“每会话”Hermes session store——它们因同一理由活得比任务长（这正是修
	// #6806 的办法），但装的是 transcript 而不是笔记——所以用更短、类似 Codex 的保留
	// 期。
	if storesRemoved, storeBytes := execenv.PruneHermesSessionStores(d.cfg.Profile, d.cfg.GCHermesSessionTTL, time.Now(), d.reserveStoreForDeletion, d.logger); storesRemoved > 0 {
		stats.hermesSessionStoresReclaimed += storesRemoved
		stats.bytesReclaimed += storeBytes
	}

	// 回收“属主执行已结束”的每任务临时目录。这些是 agent 进程的 TMPDIR，位于系统
	// temp 基下而非 WorkspacesRoot，因此上面的任务遍历从未见过它们，唯一能删它们的是
	// runTask 里的 RemoveAll defer——daemon 被杀时它根本不会跑，里面有文件仍被打开时
	// 它也会失败（#7364）。与这里其它修剪器不同，这个问的是内核而非时钟：它只在拿到
	// 属主仍会持有的 .task_lock 之后才删目录。
	if base, _, err := taskTempBaseDir(); err == nil {
		if tempRemoved, tempBytes := execenv.PruneTaskTempDirs(base, d.cfg.GCTaskTempLegacyTTL, time.Now(), d.logger); tempRemoved > 0 {
			stats.taskTempDirsReclaimed += tempRemoved
			stats.bytesReclaimed += tempBytes
		}
	}

	if stats.cleaned > 0 || stats.orphaned > 0 || stats.artifactDirs > 0 || stats.storesReclaimed > 0 || stats.hermesMemoryStoresReclaimed > 0 || stats.hermesSessionStoresReclaimed > 0 || stats.repoCachesReclaimed > 0 || stats.taskTempDirsReclaimed > 0 || stats.taskRootIndexEntriesReclaimed > 0 {
		d.logger.Info("gc: cycle complete",
			"cleaned", stats.cleaned,
			"orphaned", stats.orphaned,
			"skipped", stats.skipped,
			"artifact_dirs", stats.artifactDirs,
			"artifact_removed", stats.artifactRemoved,
			"codex_session_stores_reclaimed", stats.storesReclaimed,
			"hermes_memory_stores_reclaimed", stats.hermesMemoryStoresReclaimed,
			"hermes_session_stores_reclaimed", stats.hermesSessionStoresReclaimed,
			"repo_caches_reclaimed", stats.repoCachesReclaimed,
			"task_temp_dirs_reclaimed", stats.taskTempDirsReclaimed,
			"task_root_index_entries_reclaimed", stats.taskRootIndexEntriesReclaimed,
			"bytes_reclaimed", stats.bytesReclaimed,
			"by_pattern", stats.byPattern,
		)
	}
}

// gcWorkspace 扫描单个 workspace 目录里的任务目录。
func (d *Daemon) gcWorkspace(ctx context.Context, wsDir string, stats *gcStats) {
	taskEntries, err := os.ReadDir(wsDir)
	if err != nil {
		d.logger.Warn("gc: read workspace dir failed", "dir", wsDir, "error", err)
		return
	}

	cleanedHere := 0
	issueCandidatesByWorkspace := make(map[string][]issueGCCandidate)
	for _, entry := range taskEntries {
		if ctx.Err() != nil {
			return
		}
		if !entry.IsDir() {
			continue
		}
		taskDir := filepath.Join(wsDir, entry.Name())
		if d.isActiveEnvRoot(taskDir) {
			stats.skipped++
			continue
		}
		meta, metaErr := execenv.ReadGCMeta(taskDir)
		if metaErr == nil && meta.Kind == execenv.GCKindIssue && strings.TrimSpace(meta.IssueID) != "" {
			if workspaceID := strings.TrimSpace(meta.WorkspaceID); workspaceID != "" {
				issueCandidatesByWorkspace[workspaceID] = append(
					issueCandidatesByWorkspace[workspaceID],
					issueGCCandidate{taskDir: taskDir, meta: meta},
				)
				continue
			}
		}
		action := d.shouldCleanTaskDir(ctx, taskDir)
		cleanedHere += d.applyGCAction(taskDir, action, stats)
	}
	workspaceIDs := make([]string, 0, len(issueCandidatesByWorkspace))
	for workspaceID := range issueCandidatesByWorkspace {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	sort.Strings(workspaceIDs)
	for _, workspaceID := range workspaceIDs {
		cleanedHere += d.gcWorkspaceIssues(ctx, workspaceID, issueCandidatesByWorkspace[workspaceID], stats)
	}

	// workspace 目录若已空则把它自己也删掉。
	if cleanedHere > 0 {
		remaining, _ := os.ReadDir(wsDir)
		if len(remaining) == 0 {
			os.Remove(wsDir)
		}
	}
}

const issueGCBatchSize = 500

// issueGCCandidate 是待批处理对账的一个“由 issue 背书”的任务目录及其 GC 元数据。
type issueGCCandidate struct {
	taskDir string
	meta    *execenv.GCMeta
}

// gcWorkspaceIssues 用有界次数的 workspace 级请求对账所有 issue 背书的任务目录。
// 同一 issue 的多个任务目录共享一个结果。连到老服务器时客户端会透明地回退到旧式
// per-issue 端点。
func (d *Daemon) gcWorkspaceIssues(ctx context.Context, workspaceID string, candidates []issueGCCandidate, stats *gcStats) int {
	if len(candidates) == 0 {
		return 0
	}

	issueIDs := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		issueID := strings.TrimSpace(candidate.meta.IssueID)
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		issueIDs = append(issueIDs, issueID)
	}

	results := make(map[string]IssueGCCheckResult, len(issueIDs))
	for start := 0; start < len(issueIDs); start += issueGCBatchSize {
		if ctx.Err() != nil {
			break
		}
		end := min(start+issueGCBatchSize, len(issueIDs))
		chunkResults, err := d.client.GetIssueGCChecks(ctx, workspaceID, issueIDs[start:end])
		if err != nil {
			d.logger.Warn("gc: batch issue check failed",
				"workspace", workspaceID,
				"count", end-start,
				"error", err,
			)
			continue
		}
		for issueID, result := range chunkResults {
			results[issueID] = result
		}
	}

	cleaned := 0
	for i, candidate := range candidates {
		if ctx.Err() != nil {
			stats.skipped += len(candidates) - i
			break
		}
		issueID := strings.TrimSpace(candidate.meta.IssueID)
		result, ok := results[issueID]
		if !ok || result.Err != nil {
			// 本轮拿不到关于父 issue 的可用答案，任务数据先留着。可再生的 Codex 缓存
			// 仍是可回收对象——见 applyManagedArtifactFallback。
			action := d.applyManagedArtifactFallback(candidate.taskDir, candidate.meta, gcActionSkip)
			cleaned += d.applyGCAction(candidate.taskDir, action, stats)
			continue
		}
		action := d.gcDecisionIssueResult(candidate.taskDir, candidate.meta, result)
		action = d.applyLocalDirectoryGCOverride(candidate.meta, action)
		action = d.applyManagedArtifactFallback(candidate.taskDir, candidate.meta, action)
		cleaned += d.applyGCAction(candidate.taskDir, action, stats)
	}
	return cleaned
}

// applyGCAction 执行一个决策并更新周期统计。每次变更都会原子地预留 env root，因为
// 服务器对账请求在途期间可能有一个任务开始。
func (d *Daemon) applyGCAction(taskDir string, action gcAction, stats *gcStats) int {
	if action != gcActionSkip {
		if _, err := d.gcTaskDirOwner(taskDir); err != nil {
			d.logger.Warn("gc: refusing to mutate unowned task directory", "dir", taskDir, "error", err)
			stats.skipped++
			return 0
		}
		release, ok := d.reserveEnvRootForGC(taskDir)
		if !ok {
			stats.skipped++
			return 0
		}
		defer release()
		// 拿到排它锁后重读属主信息：并发的 reset 不能在“校验”与“变更”之间改掉归属。
		if _, err := d.gcTaskDirOwner(taskDir); err != nil {
			d.logger.Warn("gc: refusing to mutate task directory after ownership changed", "dir", taskDir, "error", err)
			stats.skipped++
			return 0
		}
	}
	switch action {
	case gcActionClean:
		bytes, removed := d.cleanTaskDir(taskDir)
		if !removed {
			stats.skipped++
			return 0
		}
		stats.cleaned++
		stats.bytesReclaimed += bytes
		return 1
	case gcActionOrphan:
		bytes, removed := d.cleanTaskDir(taskDir)
		if !removed {
			stats.skipped++
			return 0
		}
		stats.orphaned++
		stats.bytesReclaimed += bytes
		return 1
	case gcActionCleanArtifacts:
		removed, bytes, perPattern := d.cleanTaskArtifacts(taskDir, d.cfg.GCArtifactPatterns)
		recordArtifactCleanup(stats, removed, bytes, perPattern)
		stats.skipped++ // 任务目录本身保留
	case gcActionCleanManagedArtifacts:
		removed, bytes, perPattern := d.cleanManagedTaskArtifacts(taskDir)
		recordArtifactCleanup(stats, removed, bytes, perPattern)
		stats.skipped++ // 任务目录本身保留
	default:
		stats.skipped++
	}
	return 0
}

func recordArtifactCleanup(stats *gcStats, removed int, bytes int64, perPattern map[string]int) {
	if removed == 0 {
		return
	}
	stats.artifactDirs++
	stats.artifactRemoved += removed
	stats.bytesReclaimed += bytes
	if stats.byPattern == nil {
		stats.byPattern = map[string]int{}
	}
	for pattern, count := range perPattern {
		stats.byPattern[pattern] += count
	}
}

type gcAction int

const (
	gcActionSkip                  gcAction = iota
	gcActionClean                          // 父生命周期或完成任务策略选中了整目录清理
	gcActionOrphan                         // 无 meta 或 issue 未知且目录够老
	gcActionCleanArtifacts                 // 任务完成得够久；只丢弃可再生的 artifact
	gcActionCleanManagedArtifacts          // 保留任务，只丢弃确切的 daemon 受管 artifact
)

// shouldCleanTaskDir 决定是否应删除一个任务目录。按 meta.Kind 分派，让 chat /
// autopilot / quick-create 任务各自跟随真正管辖其生命周期的父记录。
func (d *Daemon) shouldCleanTaskDir(ctx context.Context, taskDir string) gcAction {
	// 当前正跑在这个 env root 上的任务绝不回收——连 done/cancelled 或 orphan-404 路径
	// 也不行。一个重派发或仍在运行的任务可能复用一个“已完成、且 updated_at 比 TTL 还
	// 老”的 issue 的旧 workdir（任务重新认领不推进 updated_at），所以光靠常规 TTL 检查
	// 注意不到恢复的活动。
	if d.isActiveEnvRoot(taskDir) {
		return gcActionSkip
	}

	meta, err := execenv.ReadGCMeta(taskDir)
	if err != nil {
		if _, ownerErr := d.gcTaskDirOwner(taskDir); ownerErr != nil {
			d.logger.Warn("gc: skipping directory without valid task ownership", "dir", taskDir, "error", ownerErr)
			return gcActionSkip
		}
		return d.orphanByMTime(taskDir, "no meta")
	}

	action := d.shouldCleanTaskDirForKind(ctx, taskDir, meta)
	action = d.applyLocalDirectoryGCOverride(meta, action)
	return d.applyManagedArtifactFallback(taskDir, meta, action)
}

// applyManagedArtifactFallback 当任务自己的可再生 Codex 缓存超过 GCArtifactTTL 时，
// 把一次 skip 升级成“受管 artifact 清理”。
//
// 任务的*数据*能否删除是按 kind 的问题：取决于父记录，由 shouldCleanTaskDirForKind
// 回答。daemon 自己的*可再生缓存*能否回收则根本不是按 kind 的问题——codex-home/
// .sandbox-bin 是一份约 285 MiB 的 Codex 二进制副本，无论父记录是谁，下次运行都会按
// 需重新供给。只把这条回收接进 issue 路径（#5654），会让其它每种 kind 无限期抱着缓存；
// 对 chat 那是真正无界的，因为会话“active”没有时限，而 Desktop 的 chat 是主要交互
// 界面（#6782）。
//
// 刻意是从 gcActionSkip 出发的单向升级。Clean 与 Orphan 本来就比它删得多，
// applyLocalDirectoryGCOverride 掌握反方向的降级——所以它只会拓宽、绝不会收窄一个
// 周期回收的范围。
//
// 注意它在 GC 检查 API 调用本身失败时也会触发（一次瞬时网络错误归结为
// gcActionSkip）。这是有意为之：缓存可再生，不需要删除任务数据所需的“父记录确认”。
// 上面的 isActiveEnvRoot 短路与 applyGCAction 里的 env-root 预留仍会保住运行中任务的
// 缓存。
func (d *Daemon) applyManagedArtifactFallback(taskDir string, meta *execenv.GCMeta, action gcAction) gcAction {
	if action != gcActionSkip || d.cfg.GCArtifactTTL <= 0 {
		return action
	}
	// CompletedAt 为零表示该任务从未经 WriteGCMeta 上报过完成。把这些留给按 kind 的
	// 旧式处理，而不是从一个无关时钟去猜。
	if meta.CompletedAt.IsZero() || time.Since(meta.CompletedAt) <= d.cfg.GCArtifactTTL {
		return action
	}
	// 对保持非终态的任务，completed_at 再也不会动，所以没有下面这行的话决策会永远停在
	// "reclaim"，每周期都付一次预留与一次“什么也删不到”的删除扫描。与再供给竞态无害：
	// 下一周期会捡起来。
	if !hasManagedArtifact(taskDir) {
		return action
	}
	d.logger.Info("gc: eligible for managed artifact cleanup",
		"dir", filepath.Base(taskDir),
		"kind", string(meta.Kind),
		"completed_at", meta.CompletedAt.Format(time.RFC3339),
	)
	return gcActionCleanManagedArtifacts
}

// applyLocalDirectoryGCOverride 对 local_directory 任务把“整目录删除”降级为更窄的
// 清理，保住用户自己的审计痕迹。
func (d *Daemon) applyLocalDirectoryGCOverride(meta *execenv.GCMeta, action gcAction) gcAction {
	if !meta.LocalDirectory {
		return action
	}
	// local_directory 任务的 envRoot 无限期保留，用户才能翻 output/ 与 logs/ 做取证。
	// WorkDir 是用户自己的路径、位于 taskDir 之外。envRoot 装着 daemon 的持久日志账本
	// 加可再生的工具缓存，所以前者保留、后者允许做窄范围清理。
	//
	//   gcActionClean   → 降级成 artifact 模式清理，让 envRoot（尤其是日志账本）存活。
	//   gcActionOrphan  → 只做确切的受管 artifact 清理；我们绝不经 mtime 路径抹掉一个
	//                     local_directory 的 envRoot——父 issue / chat 记录消失不应
	//                     殃及删除用户的审计痕迹。
	//
	// GCArtifactTTL 显式为零时 artifact 清理保持禁用。
	// gcActionCleanArtifacts、gcActionCleanManagedArtifacts 与 gcActionSkip 都遵守
	// “绝不整目录 RemoveAll envRoot”的规矩。
	if d.cfg.GCArtifactTTL <= 0 {
		return gcActionSkip
	}
	switch action {
	case gcActionClean:
		return gcActionCleanArtifacts
	case gcActionOrphan:
		return gcActionCleanManagedArtifacts
	default:
		return action
	}
}

// shouldCleanTaskDirForKind 跑按 Kind 的分派但不套 local_directory 覆盖。拆出来让
// shouldCleanTaskDir 能拦截结果。
func (d *Daemon) shouldCleanTaskDirForKind(ctx context.Context, taskDir string, meta *execenv.GCMeta) gcAction {
	switch meta.Kind {
	case execenv.GCKindIssue:
		return d.gcDecisionIssue(ctx, taskDir, meta)
	case execenv.GCKindChat:
		return d.gcDecisionChat(ctx, taskDir, meta)
	case execenv.GCKindAutopilotRun:
		return d.gcDecisionAutopilotRun(ctx, taskDir, meta)
	case execenv.GCKindQuickCreate:
		return d.gcDecisionQuickCreate(ctx, taskDir, meta)
	default:
		// 未知 kind：回退到按 mtime 的 orphan 清理，未来某 daemon 写下一个我们不认识
		// 的 kind 也不至于被瞬间抹掉。
		return d.orphanByMTime(taskDir, "unknown kind")
	}
}

// orphanByMTime 目录老于 GCOrphanTTL 时返回 gcActionOrphan，否则 gcActionSkip。它集中
// 了每种 kind 都在用的“没有父记录信号，只能看磁盘”的兜底。
func (d *Daemon) orphanByMTime(taskDir, reason string) gcAction {
	info, err := os.Stat(taskDir)
	if err != nil {
		return gcActionSkip
	}
	if time.Since(info.ModTime()) > d.cfg.GCOrphanTTL {
		d.logger.Info("gc: orphan directory", "dir", taskDir, "reason", reason, "age", time.Since(info.ModTime()).Round(time.Hour))
		return gcActionOrphan
	}
	return gcActionSkip
}

// isAccessNotFound 检测 gc-check 端点返回的 404。同一个状态码同时覆盖“行已删除”与
// “daemon token 看不见这个 workspace”（requireDaemonWorkspaceAccess 防枚举形态），
// 因此调用方单看响应无法区分两者。
func isAccessNotFound(err error) bool {
	var reqErr *requestError
	return errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound
}

func (d *Daemon) gcDecisionIssue(ctx context.Context, taskDir string, meta *execenv.GCMeta) gcAction {
	if strings.TrimSpace(meta.IssueID) == "" {
		return d.orphanByMTime(taskDir, "empty issue id")
	}

	status, err := d.client.GetIssueGCCheck(ctx, meta.IssueID)
	if err != nil {
		if isAccessNotFound(err) {
			// 404 有歧义：服务器对“issue 已删除”与“daemon token 无权访问该 workspace”
			// 都返回它。回退到按 mtime 把关的 orphan 清理，让被缩小权限的 token 无法
			// 瞬间抹掉 issue 仍然在线的目录。
			return d.orphanByMTime(taskDir, "issue not accessible")
		}
		return gcActionSkip
	}

	return d.gcDecisionIssueResult(taskDir, meta, IssueGCCheckResult{
		ID:        meta.IssueID,
		Found:     true,
		Status:    status.Status,
		UpdatedAt: status.UpdatedAt,
	})
}

func (d *Daemon) gcDecisionIssueResult(taskDir string, meta *execenv.GCMeta, result IssueGCCheckResult) gcAction {
	if !result.Found {
		return d.orphanByMTime(taskDir, "issue not accessible")
	}

	// result.Status 是服务端归一化过的 CATEGORY，所以这里的字面比较也覆盖自定义状态
	// ——停在 `done`-category 自定义状态上的 issue 在这里是终态。（MUL-6243）
	if (result.Status == "done" || result.Status == "cancelled") &&
		time.Since(result.UpdatedAt) > d.cfg.GCTTL {
		d.logger.Info("gc: eligible for cleanup",
			"dir", filepath.Base(taskDir),
			"kind", "issue",
			"issue", meta.IssueID,
			"status", result.Status,
			"updated_at", result.UpdatedAt.Format(time.RFC3339),
		)
		return gcActionClean
	}

	// 运维可对“已完成的 issue 任务环境”选择硬保留上限，即使父 issue 仍开着。刻意要求
	// “父查找成功且状态可识别”：瞬时网络/鉴权失败与响应漂移宁可保留数据，也不把“不可
	// 用或畸形状态”变成删除信号。
	// 活动根守卫与预留保护并发复用。用户自有的 local_directory env 维持现有“仅
	// artifact”策略，更短的 completed-task TTL 不会让 artifact 清理提前发生。
	if d.cfg.GCCompletedTaskTTL > 0 &&
		!meta.LocalDirectory &&
		!meta.CompletedAt.IsZero() &&
		isKnownIssueStatus(result.Status) &&
		time.Since(meta.CompletedAt) > d.cfg.GCCompletedTaskTTL {
		d.logger.Info("gc: completed task eligible for full cleanup",
			"dir", filepath.Base(taskDir),
			"kind", "issue",
			"issue", meta.IssueID,
			"status", result.Status,
			"completed_at", meta.CompletedAt.Format(time.RFC3339),
			"completed_task_ttl", d.cfg.GCCompletedTaskTTL,
		)
		return gcActionClean
	}

	if d.cfg.GCArtifactTTL > 0 && !meta.CompletedAt.IsZero() && time.Since(meta.CompletedAt) > d.cfg.GCArtifactTTL {
		d.logger.Info("gc: eligible for artifact cleanup",
			"dir", filepath.Base(taskDir),
			"kind", "issue",
			"issue", meta.IssueID,
			"status", result.Status,
			"completed_at", meta.CompletedAt.Format(time.RFC3339),
		)
		return gcActionCleanArtifacts
	}

	// 旧元数据可能没有 completed_at。这个案例保守处理：元数据文件自身闲置超过更长的
	// orphan TTL 后，只回收确切的 daemon 受管缓存。WriteGCMeta 会在每个完成的任务之后
	// 替换这个文件，所以哪怕 taskDir 下的活动让根目录 mtime 变旧，一次近期的复用也会
	// 刷新该信号。
	if d.cfg.GCArtifactTTL > 0 && meta.CompletedAt.IsZero() {
		if age, ok := gcMetaFileAge(taskDir); ok && age > d.cfg.GCOrphanTTL {
			d.logger.Info("gc: legacy task eligible for managed artifact cleanup",
				"dir", filepath.Base(taskDir),
				"kind", "issue",
				"issue", meta.IssueID,
				"status", result.Status,
				"age", age.Round(time.Hour),
			)
			return gcActionCleanManagedArtifacts
		}
	}

	return gcActionSkip
}

// isKnownIssueStatus 镜像服务器强制的 issue 状态约束。整目录清理必须“闭合失败”：老
// daemon 收到未来状态或 GC 检查端点的畸形响应时，不应据此删除。
//
// 下面 7 个名字在自定义状态（MUL-6243）之后仍然正确，因为 gc-check 端点返回的是状态
// 的 CATEGORY 而非存储的 key——见 BatchIssueGCCheck / GetIssueGCCheck，它们经
// issuestatus.Effective 解析。不要教这个函数认识自定义状态：已装 daemon 没有可解析它们
// 的目录，而早于该特性的 daemon 必须继续对升级后的服务器做正确决策。由
// TestIssueGCChecksReportCategoryNotRawCustomStatus 钉住。
func isKnownIssueStatus(status string) bool {
	switch status {
	case "backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled":
		return true
	default:
		return false
	}
}

// gcMetaFileAge 返回 .gc_meta.json 文件自身的 mtime 年龄（自上次写入起），供没有
// completed_at 的旧元数据做保守回收判断。
func gcMetaFileAge(taskDir string) (time.Duration, bool) {
	info, err := os.Stat(filepath.Join(taskDir, ".gc_meta.json"))
	if err != nil {
		return 0, false
	}
	return time.Since(info.ModTime()), true
}

func (d *Daemon) gcDecisionChat(ctx context.Context, taskDir string, meta *execenv.GCMeta) gcAction {
	if strings.TrimSpace(meta.ChatSessionID) == "" {
		return d.orphanByMTime(taskDir, "empty chat session id")
	}

	status, err := d.client.GetChatSessionGCCheck(ctx, meta.ChatSessionID)
	if err != nil {
		if isAccessNotFound(err) {
			// 404 表示 chat_session 行没了——DeleteChatSession 是真 DELETE，所以用户
			// 一点按钮，硬删立刻传导到这里。这是我们拿到的最强回收信号，恰好就是验收
			// 标准 #3：一个 GC 周期（≤ GCInterval）内回收，而不是 72 小时。
			//
			// 我们不按 mtime 把关：meta 文件里的每个 chat_session_id 都是本 daemon 在
			// 当前 token 下写的，因此不存在需要防御的跨 workspace 探测。
			d.logger.Info("gc: eligible for cleanup",
				"dir", filepath.Base(taskDir),
				"kind", "chat",
				"chat_session", meta.ChatSessionID,
				"reason", "session not accessible (hard-deleted)",
			)
			return gcActionClean
		}
		return gcActionSkip
	}

	switch status.Status {
	case "active":
		// 活动 chat 会话的目录绝不按 mtime 回收——那会悄悄杀掉用户的闲置会话、破坏
		// 他们下一条消息的 "PriorWorkDir" 续接。
		//
		// 这保护的是会话自己的数据，不是 daemon 的可再生缓存。shouldCleanTaskDir 会在
		// 这次 skip 之上叠加 applyManagedArtifactFallback，因此闲置超过 GCArtifactTTL
		// 的会话会交还 codex-home/.sandbox-bin，下一条消息再重新供给。续接时那要付一次
		// 约 285 MiB 的 Codex 引导，是 gcDecisionIssueResult 对“issue 仍开的已完成
		// 任务”已经做过的同一笔交易——没有它，活动会话会把缓存永远钉住（#6782）。
		return gcActionSkip
	case "archived":
		if time.Since(status.UpdatedAt) > d.cfg.GCTTL {
			d.logger.Info("gc: eligible for cleanup",
				"dir", filepath.Base(taskDir),
				"kind", "chat",
				"chat_session", meta.ChatSessionID,
				"status", status.Status,
				"updated_at", status.UpdatedAt.Format(time.RFC3339),
			)
			return gcActionClean
		}
	}
	return gcActionSkip
}

func (d *Daemon) gcDecisionAutopilotRun(ctx context.Context, taskDir string, meta *execenv.GCMeta) gcAction {
	if strings.TrimSpace(meta.AutopilotRunID) == "" {
		return d.orphanByMTime(taskDir, "empty autopilot run id")
	}

	status, err := d.client.GetAutopilotRunGCCheck(ctx, meta.AutopilotRunID)
	if err != nil {
		if isAccessNotFound(err) {
			return d.orphanByMTime(taskDir, "autopilot run not accessible")
		}
		return gcActionSkip
	}

	// autopilot_run CHECK 约束里的终态：
	//   completed, failed, skipped — run 做完了自己的事。
	//   issue_created            — run 产出了一个拥有自己 workdir 的 issue 任务；
	//                              从此这个 run 的 workdir 就是死重。
	// 非终态：pending, running。一直跳到它们进入终态，而不是按 mtime 去约束——长时间
	// 运行的 autopilot 是真实存在的。
	//
	// autopilot run 的 workdir 从不复用：与 issue/chat 任务不同，没有 PriorWorkDir 把
	// 同一目录交给后来的 run，所以每个 run 都拿全新的。run 产出的东西已全部在服务端
	// （issue_created 的 run 把活交给了拥有自己 envRoot 的 issue 任务）。因此 run 一到
	// 终态，目录就是死重、立即回收，不用等 GCTTL——与 gcDecisionQuickCreate 对
	// quick-create 目录的理由一致。shouldCleanTaskDir 里的活动 env root 短路仍保护飞行
	// 中的 run，所以这不会抽掉活产出的地毯。
	if isAutopilotRunTerminal(status.Status) {
		d.logger.Info("gc: eligible for cleanup",
			"dir", filepath.Base(taskDir),
			"kind", "autopilot_run",
			"autopilot_run", meta.AutopilotRunID,
			"status", status.Status,
		)
		return gcActionClean
	}
	return gcActionSkip
}

// isAutopilotRunTerminal 镜像 migrations/042_autopilot.up.sql 里的 run.status CHECK。
// 非终态是 pending/running；schema 允许的其它每个值，从 daemon 视角都是最终静息状态
// （run 不再在这个 workdir 里产出工作）。
func isAutopilotRunTerminal(status string) bool {
	switch status {
	case "completed", "failed", "skipped", "issue_created":
		return true
	default:
		return false
	}
}

func (d *Daemon) gcDecisionQuickCreate(ctx context.Context, taskDir string, meta *execenv.GCMeta) gcAction {
	if strings.TrimSpace(meta.TaskID) == "" {
		return d.orphanByMTime(taskDir, "empty task id")
	}

	status, err := d.client.GetTaskGCCheck(ctx, meta.TaskID)
	if err != nil {
		if isAccessNotFound(err) {
			// 任务行被硬删，或 token 看不见它。无论哪种都回退到按 mtime 把关的 orphan，
			// 对缩小权限的 token 保持安全——与 issue 路径同理。
			return d.orphanByMTime(taskDir, "task not accessible")
		}
		return gcActionSkip
	}

	// quick-create workdir 不会被 LinkTaskToIssue 最终挂接的 issue 任务复用——那个
	// issue 会拿自己的 envRoot。所以 quick-create 任务一到终态就能立即回收目录，不用
	// 等 GCTTL。用户想回看时，挂接的 issue 里已有 agent 的输出。
	if isAgentTaskTerminal(status.Status) {
		d.logger.Info("gc: eligible for cleanup",
			"dir", filepath.Base(taskDir),
			"kind", "quick_create",
			"task", meta.TaskID,
			"status", status.Status,
		)
		return gcActionClean
	}
	return gcActionSkip
}

// isAgentTaskTerminal 报告 agent_task_queue.status 的某个值是否代表终态。镜像任务服务
// 各处使用的状态枚举——规范清单见 service/task.go。
func isAgentTaskTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// gcTaskDirOwner 只有当“标记本身”与“它位于 WorkspacesRoot 之下的位置”都一致时才
// 返回权威属主信息。GC 元数据缺失不构成归属证明：部分准备好的根有 .task_owner，而
// 任意用户目录没有。
func (d *Daemon) gcTaskDirOwner(taskDir string) (*execenv.EnvRootOwner, error) {
	owner, err := execenv.ReadEnvRootOwner(taskDir)
	if err != nil {
		return nil, fmt.Errorf("read task owner: %w", err)
	}
	if owner == nil {
		return nil, errors.New("task owner is missing")
	}
	validated := *owner
	if validated.WorkspaceID == "" && validated.TaskID != "" {
		// 旧版属主标记只含任务 ID。完成元数据能补上缺失的 workspace 身份，且不削弱
		// “Prepare 时必须存在属主标记”这一硬要求。
		meta, metaErr := execenv.ReadGCMeta(taskDir)
		if metaErr != nil || strings.TrimSpace(meta.WorkspaceID) == "" {
			return nil, errors.New("legacy task owner has no workspace identity or valid GC metadata")
		}
		validated.WorkspaceID = strings.TrimSpace(meta.WorkspaceID)
	}
	if err := execenv.ValidateEnvRootOwnerPath(d.cfg.WorkspacesRoot, taskDir, validated); err != nil {
		return nil, err
	}
	return &validated, nil
}

// cleanTaskDir 删除一个“已被证明 daemon 所有”的任务目录、记日志并返回回收字节数供
// 周期汇总。删除失败或被拒时返回 removed=false。
func (d *Daemon) cleanTaskDir(taskDir string) (bytes int64, removed bool) {
	// 先量大小、后证归属。dirSize 会遍历整棵树，在大任务目录上耗时足以让“已校验的
	// 目录”在我们脚下被替换——在校验前做遍历，会把 RemoveAll 交到一个几十秒前才证明
	// 归我们、此刻未必仍是我们的路径。这是纵深防御检查，所以它紧挨着删除。
	bytes = dirSize(taskDir)
	owner, ownerErr := d.gcTaskDirOwner(taskDir)
	if ownerErr != nil {
		d.logger.Warn("gc: refusing to remove unowned task directory", "dir", taskDir, "error", ownerErr)
		return 0, false
	}
	if err := os.RemoveAll(taskDir); err != nil {
		d.logger.Warn("gc: remove task dir failed", "dir", taskDir, "error", err)
		return 0, false
	} else {
		d.logger.Info("gc: removed", "dir", taskDir, "bytes_reclaimed", bytes)
	}
	if err := execenv.RemoveRootDirRecord(d.cfg.WorkspacesRoot, taskDir, *owner); err != nil {
		d.logger.Warn("gc: remove stable task root record failed", "dir", taskDir, "error", err)
	}
	return bytes, true
}

// linkedDirModes 是标志“该目录条目是指向任务并不拥有的内容的链接”的模式位。每个
// “删除或测量”的任务目录遍历都必须拒绝下钻它们：每任务 codex-home 会把用户的真实
// 技能、Codex session store 与插件缓存链接进自己，下钻等于把 GC 放进用户家目录。
//
// Windows 上单看 ModeSymlink 不够。createDirLink 在 os.Symlink 被拒（无开发者模式）
// 时回退到目录 junction（mklink /J）；而自 Go 1.23 起 os.Lstat 把 junction 报成
// ModeDir|ModeIrregular、没有 ModeSymlink 位，同时它的 DirEntry 仍答 IsDir() ==
// true。因此只检查 ModeSymlink 会让 filepath.WalkDir 径直走进链接目标。
const linkedDirModes = os.ModeSymlink | os.ModeIrregular

// cleanTaskArtifacts 遍历 taskDir 删除每个“basename 命中某模式”的目录，外加确切的
// daemon 受管 artifact 路径。返回 (removedCount, bytesReclaimed, perPattern)。
//
// 安全契约：
//   - 模式只看 basename；带路径分隔符的条目被丢弃。
//   - 绝不下钻 .git 子树，即使某模式会命中，agent 的 git 历史也保持完好。
//   - 链接目录整体跳过——链接本身与目标都不碰，恶意或过期链接无法把 GC 引到 workdir
//     之外。见 linkedDirModes 什么算链接。
//   - 每个删除目标都验证在 taskDir 内，被篡改的 .gc_meta.json 骗不了 daemon 删到
//     sandbox 之外。
func (d *Daemon) cleanTaskArtifacts(taskDir string, patterns []string) (removed int, bytes int64, perPattern map[string]int) {
	return d.cleanTaskArtifactsMatching(taskDir, newArtifactMatcher(patterns, execenv.ManagedReclaimableArtifactSubpaths()))
}

// cleanManagedTaskArtifacts 删除 taskDir 下确切的 daemon 受管 artifact 子路径。
//
// 受管集合是一列精确的相对路径，所以是“直接寻址”而不是“搜寻”。为找一个位置已知的
// 目录去遍历整棵任务树，要付一整个仓库检出那么多次 stat；而保持非终态的任务——活动
// chat 会话——在其存活期内每个 GC 周期都要付这笔钱，哪怕缓存早已不在了。
// cleanTaskArtifacts 仍要遍历，因为它的 basename 模式可能命中任意深度；这里则没什么
// 可找的。
func (d *Daemon) cleanManagedTaskArtifacts(taskDir string) (removed int, bytes int64, perPattern map[string]int) {
	perPattern = map[string]int{}
	if taskDir == "" {
		return
	}
	absRoot, err := filepath.Abs(taskDir)
	if err != nil {
		return
	}
	for _, subpath := range execenv.ManagedReclaimableArtifactSubpaths() {
		rel, ok := safeRelativePath(subpath)
		if !ok {
			continue
		}
		target, ok := managedArtifactTarget(absRoot, rel)
		if !ok {
			continue
		}
		size := dirSize(target)
		if rmErr := os.RemoveAll(target); rmErr != nil {
			d.logger.Warn("gc: artifact remove failed", "path", target, "error", rmErr)
			continue
		}
		removed++
		bytes += size
		perPattern[managedArtifactPatternPrefix+filepath.ToSlash(rel)]++
		d.logger.Info("gc: artifact removed", "path", target, "bytes", size)
	}
	return
}

// managedArtifactTarget 把 absRoot 下的一条受管相对子路径解析成“可安全删除”的绝对
// 路径；没有可回收对象时返回 false。
//
// 它取代的树遍历曾拒绝下钻符号链接与 Windows junction：每任务 codex-home 把用户的
// 真实技能、Codex session store 与插件缓存链接进自己，跟随一个就等于把 RemoveAll 放进
// 用户家目录。直接寻址意味着 absRoot 到叶子之间每个组件都要复查，而不只是叶子。见
// linkedDirModes。
//
// 包含性无需单独检查：safeRelativePath 已拒绝绝对路径与任何向上逃逸，filepath.Clean
// 不留内部 ".."，因此逐组件 join 不可能离开 absRoot。
func managedArtifactTarget(absRoot, rel string) (string, bool) {
	current := absRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			// 已被回收、从未创建、或不可读——三者都意味着“本周期无事可做”。
			return "", false
		}
		if info.Mode()&linkedDirModes != 0 || !info.IsDir() {
			return "", false
		}
	}
	return current, true
}

// hasManagedArtifact 报告是否有任何受管子路径真实存在。没有它，决策层会对一个
// completed_at 不再前进的长命任务反复返回 gcActionCleanManagedArtifacts，每个周期都
// 做一次 env-root 预留、记一条“删了个寂寞”的日志。
func hasManagedArtifact(taskDir string) bool {
	absRoot, err := filepath.Abs(taskDir)
	if err != nil {
		return false
	}
	for _, subpath := range execenv.ManagedReclaimableArtifactSubpaths() {
		rel, ok := safeRelativePath(subpath)
		if !ok {
			continue
		}
		if _, ok := managedArtifactTarget(absRoot, rel); ok {
			return true
		}
	}
	return false
}

func (d *Daemon) cleanTaskArtifactsMatching(taskDir string, matcher artifactMatcher) (removed int, bytes int64, perPattern map[string]int) {
	perPattern = map[string]int{}
	if taskDir == "" || (len(matcher.basenames) == 0 && len(matcher.exactPaths) == 0) {
		return
	}

	absRoot, err := filepath.Abs(taskDir)
	if err != nil {
		return
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // 尽力而为——继续走
		}
		if path == absRoot {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		// 绝不下钻 .git——即使某模式（如 "objects"）会命中，agent 提交也得以保留。
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		// 拒绝跟随链接目录。WalkDir 在某些平台把它们报成 Dir 类型；用 lstat 确认。
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil
		}
		if info.Mode()&linkedDirModes != 0 {
			return filepath.SkipDir
		}
		pattern, ok := matcher.matchDirectory(absRoot, path, entry)
		if !ok {
			return nil
		}
		size := dirSize(path)
		if rmErr := os.RemoveAll(path); rmErr != nil {
			d.logger.Warn("gc: artifact remove failed", "path", path, "error", rmErr)
			return filepath.SkipDir
		}
		removed++
		bytes += size
		perPattern[pattern]++
		d.logger.Info("gc: artifact removed", "path", path, "bytes", size)
		// 不要下钻进这个已删除的子树。
		return filepath.SkipDir
	})
	if walkErr != nil {
		d.logger.Warn("gc: artifact walk failed", "dir", taskDir, "error", walkErr)
	}
	return
}

// dirSize 返回 root 下所有常规文件的总字节数。链接内容不计入：os.RemoveAll 会丢掉
// 链接而留下目标，数了它会让“一次删除回收的量”虚高。非致命：遍历中的错误被忽略，
// 调用方可以报一个尽力而为的字节数而不必中止整个 GC 周期。
func dirSize(root string) int64 {
	total, _ := dirSizeContext(context.Background(), root)
	return total
}

// dirSizeContext 是支持取消的 dirSize。
func dirSizeContext(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			return nil
		}
		if entry.Type()&linkedDirModes != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

const (
	gitCmdTimeout         = 30 * time.Second
	gitMaintenanceTimeout = 10 * time.Minute
	repoMaintenanceMarker = ".multica-maintenance-pending"
)

// pruneRepoWorktrees 对所有缓存里的裸仓库跑 `git worktree prune`，再驱逐没人再需要的
// 那些。
func (d *Daemon) pruneRepoWorktrees(workspacesRoot string, stats *gcStats) {
	d.pruneRepoWorktreesContext(context.Background(), workspacesRoot, stats)
}

func (d *Daemon) pruneRepoWorktreesContext(ctx context.Context, workspacesRoot string, stats *gcStats) {
	reposRoot := filepath.Join(workspacesRoot, reposDirName)
	wsEntries, err := os.ReadDir(reposRoot)
	if err != nil {
		return
	}

	for _, wsEntry := range wsEntries {
		if ctx.Err() != nil {
			return
		}
		if !wsEntry.IsDir() {
			continue
		}
		wsRepoDir := filepath.Join(reposRoot, wsEntry.Name())
		repoEntries, err := os.ReadDir(wsRepoDir)
		if err != nil {
			continue
		}
		for _, repoEntry := range repoEntries {
			if ctx.Err() != nil {
				return
			}
			if !repoEntry.IsDir() {
				continue
			}
			barePath := filepath.Join(wsRepoDir, repoEntry.Name())
			if !isBareRepo(barePath) {
				continue
			}
			d.maintainRepoCache(ctx, barePath, stats)
		}
		// 最后一个仓库也没了之后，删掉该 workspace 目录。
		if remaining, err := os.ReadDir(wsRepoDir); err == nil && len(remaining) == 0 {
			os.Remove(wsRepoDir)
		}
	}
}

func (d *Daemon) maintainRepoCache(ctx context.Context, barePath string, stats *gcStats) {
	d.withRepoMaintenance(ctx, barePath, func(maintenanceCtx context.Context) {
		d.pruneWorktreeLocked(maintenanceCtx, barePath)
		if maintenanceCtx.Err() == nil {
			d.evictRepoCacheLocked(maintenanceCtx, barePath, stats)
		}
	})
}

// pruneWorktree 只跑维护那半——修剪过期 worktree 与 agent 分支——不考虑驱逐。
func (d *Daemon) pruneWorktree(barePath string) {
	d.withRepoMaintenance(context.Background(), barePath, func(ctx context.Context) {
		d.pruneWorktreeLocked(ctx, barePath)
	})
}

// repoMaintenanceBackend 是 repoCache 可选实现的“前台优先维护门”接口。
type repoMaintenanceBackend interface {
	WithRepoMaintenance(context.Context, string, func(context.Context) error) (bool, error)
}

// withRepoMaintenance 可用时走缓存的“前台优先”门。回退保留早于该可选接口的
// 测试/降级后端，且不改变 repoCacheBackend 契约。
func (d *Daemon) withRepoMaintenance(ctx context.Context, barePath string, fn func(context.Context)) {
	if cache, ok := d.repoCache.(repoMaintenanceBackend); ok {
		ran, err := cache.WithRepoMaintenance(ctx, barePath, func(maintenanceCtx context.Context) error {
			fn(maintenanceCtx)
			return nil
		})
		if err != nil && ctx.Err() == nil {
			d.logger.Warn("gc: repo maintenance lock failed", "repo", barePath, "error", err)
		}
		if !ran {
			d.logger.Debug("gc: repo maintenance skipped for foreground work", "repo", barePath)
		}
		return
	}
	d.withRepoLock(barePath, func() { fn(ctx) })
}

// withRepoLock 让一次变更与同一裸仓库上的 Sync / CreateWorktree 串行。没有 repo cache
// 的 daemon（测试、降级启动）无锁可取，直接执行该工作。
func (d *Daemon) withRepoLock(barePath string, fn func()) {
	if d.repoCache == nil {
		fn()
		return
	}
	if err := d.repoCache.WithRepoLock(barePath, func() error {
		fn()
		return nil
	}); err != nil {
		d.logger.Warn("gc: repo lock failed", "repo", barePath, "error", err)
	}
}

// evictRepoCacheLocked 删除没人再需要的裸仓库缓存。调用方必须持有仓库锁，因此它不会
// 与同一仓库上的 Sync 或 CreateWorktree 竞态。
//
// 四个条件缺一不可：
//
//  1. GCRepoTTL > 0 ——驱逐是可退出的。
//
//  2. 没有受管 workspace 仍声称要这个仓库。这是 RETAIN（保留）谓词而非删除谓词，方向
//     很关键：Sync 会在 workspace 注册时（每次 daemon 启动都会发生）重克隆每个“在列但
//     缺失”的仓库。驱逐仍被挂接的仓库只换来下次重启时整仓重克隆——那不是回收空间，
//     是搬移空间。由于该集合只*阻止*删除，陈旧或空的集合不可能扩大删除范围；它只能撤
//     掉一层防护，而第 3、4 条仍兜着。
//
//  3. 没有任何 worktree 剩下——在 `git worktree prune` 已删掉“任务目录已被 GC 移除”
//     的条目之后检查。活动 worktree 的 .git 指向这个目录，删它等于弄坏那份检出。
//
//  4. GCRepoTTL 内没有任务从它创建过 worktree。未知的时间戳会被打上戳并跳过，绝不
//     当作远古——见 repocache.LastUsed。
//
// 误驱逐只费时间、不失正确性：下一个需要该仓库的任务走 ensureRepoReady 的缓存未命
// 中路径，按需重新同步与克隆。
func (d *Daemon) evictRepoCacheLocked(ctx context.Context, barePath string, stats *gcStats) {
	if d.cfg.GCRepoTTL <= 0 {
		return
	}
	// 便宜的提前返回，让仍被挂接的仓库——常见情形——永远不用付下面 git 与文件系统
	// 的工作。
	if d.repoBarePathIsLive(barePath) {
		return
	}

	worktrees, err := linkedWorktreeCountContext(ctx, barePath)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.logger.Warn("gc: worktree count failed", "repo", barePath, "error", err)
		return
	}
	if worktrees > 0 {
		return
	}

	lastUsed, ok := repocache.LastUsed(barePath)
	if !ok {
		// 该缓存创建于“打时间戳”功能之前。现在开始给它计时；把 "unknown" 读成远古，
		// 会在升级后第一个周期就把机器上每个升级前缓存全驱逐掉。
		repocache.MarkUsed(barePath, d.logger)
		return
	}
	idle := time.Since(lastUsed)
	if idle <= d.cfg.GCRepoTTL {
		return
	}

	// 在最终检查之前量大小，而不是之后。dirSize 走遍仓库每个文件，在多 GiB 缓存上
	// 耗时足以让某个 workspace 在我们脚下重新挂接——把它放在检查与删除之间，等于把本
	// 检查要关闭的窗口重新打开大半。
	bytes, err := dirSizeContext(ctx, barePath)
	if err != nil {
		return
	}

	// 删除前立刻再问一次。上面的检查跑过 git 与文件系统，期间 workspace 可能重新挂接
	// 该仓库；重读内存状态只花一次互斥、不花网络，还能把窗口从“整轮 .repos 遍历”
	// 缩小到这两条相邻语句。
	if d.repoBarePathIsLive(barePath) {
		return
	}

	if err := os.RemoveAll(barePath); err != nil {
		d.logger.Warn("gc: repo cache remove failed", "repo", barePath, "error", err)
		return
	}
	stats.repoCachesReclaimed++
	stats.bytesReclaimed += bytes
	d.logger.Info("gc: repo cache evicted",
		"repo", filepath.Base(barePath),
		"workspace", filepath.Base(filepath.Dir(barePath)),
		"last_used", lastUsed.UTC().Format(time.RFC3339),
		"idle", idle.Round(time.Hour),
		"bytes_reclaimed", bytes,
	)
}

// linkedWorktreeCount 返回某个裸仓库还有多少条链接 worktree。`git worktree list
// --porcelain` 每个 worktree 输出一个空行分隔的块，并用 `bare` 行标记裸仓库自己的
// 块；只有链接块代表“仓库没了会坏掉的检出”。
func linkedWorktreeCount(barePath string) (int, error) {
	return linkedWorktreeCountContext(context.Background(), barePath)
}

func linkedWorktreeCountContext(ctx context.Context, barePath string) (int, error) {
	out, err := runGitGCCommandContext(ctx, barePath, "worktree", "list", "--porcelain")
	if err != nil {
		return 0, err
	}

	count := 0
	inBlock := false
	isBare := false
	flush := func() {
		if inBlock && !isBare {
			count++
		}
		inBlock = false
		isBare = false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			inBlock = true
		case line == "bare":
			isBare = true
		}
	}
	flush()
	return count, nil
}

func (d *Daemon) pruneWorktreeLocked(ctx context.Context, barePath string) {
	if out, err := runGitGCCommandContext(ctx, barePath, "worktree", "prune"); err != nil {
		if ctx.Err() != nil {
			return
		}
		d.logger.Warn("gc: worktree prune failed",
			"repo", barePath,
			"output", out,
			"error", err,
		)
	}

	activeBranches, err := agentWorktreeBranchesContext(ctx, barePath)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.logger.Warn("gc: worktree branch scan failed", "repo", barePath, "error", err)
		return
	}

	agentBranches, err := listAgentBranchesContext(ctx, barePath)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.logger.Warn("gc: agent branch scan failed", "repo", barePath, "error", err)
		return
	}

	deleted := 0
	for _, branch := range agentBranches {
		if _, ok := activeBranches[branch]; ok {
			continue
		}
		if out, err := runGitGCCommandContext(ctx, barePath, "branch", "-D", "--", branch); err != nil {
			if ctx.Err() != nil {
				return
			}
			d.logger.Warn("gc: agent branch delete failed",
				"repo", barePath,
				"branch", branch,
				"output", out,
				"error", err,
			)
			continue
		}
		deleted++
	}
	markerPath := filepath.Join(barePath, repoMaintenanceMarker)
	pending := deleted > 0
	if pending {
		if err := os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
			d.logger.Warn("gc: record pending repo maintenance failed", "repo", barePath, "error", err)
		}
		d.logger.Info("gc: deleted stale agent branches", "repo", barePath, "count", deleted)
	} else if _, err := os.Stat(markerPath); err == nil {
		pending = true
	}
	if !pending {
		return
	}
	if !d.cfg.GCRepoMaintenanceEnabled {
		d.logger.Debug("gc: heavy repo maintenance disabled", "repo", barePath)
		return
	}
	// agent CLI 会直接在 daemon 进程内仓库闸门之外改链接 worktree 的引用。有活动任务
	// 时不要启动重维护；本检查之后到达的新任务或检出会经下面的维护 context 抢占。
	if d.activeTasks.Load() > 0 {
		d.logger.Debug("gc: heavy repo maintenance deferred while tasks are active", "repo", barePath)
		return
	}

	// 只在真的删过引用时才做更重的维护，这样不会把每个 GC tick 都变成对每个缓存仓库
	// 的一次完整 `git gc --prune`。prune 那步有自己更长的超时，因为它在真实裸缓存上
	// 可能要跑几分钟；放在共享 30s 预算下会在中途被杀。
	maintenance := []struct {
		args    []string
		timeout time.Duration
	}{
		{args: []string{"reflog", "expire", "--expire=30.days", "--all"}, timeout: gitCmdTimeout},
		{args: []string{"gc", "--prune=30.days"}, timeout: gitMaintenanceTimeout},
	}
	completed := true
	for _, step := range maintenance {
		if ctx.Err() != nil || d.activeTasks.Load() > 0 {
			return
		}
		before := snapshotRepoMaintenanceLocks(barePath)
		if out, err := runGitCommandContext(ctx, barePath, step.timeout, step.args...); err != nil {
			completed = false
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, repocache.ErrMaintenancePreempted) {
				d.cleanupNewRepoMaintenanceLocks(barePath, before)
			}
			if errors.Is(context.Cause(ctx), repocache.ErrMaintenancePreempted) {
				d.logger.Info("gc: git maintenance preempted for foreground work",
					"repo", barePath,
					"command", strings.Join(step.args, " "),
				)
				return
			}
			d.logger.Warn("gc: git maintenance failed",
				"repo", barePath,
				"command", strings.Join(step.args, " "),
				"output", out,
				"error", err,
			)
		}
	}
	if completed {
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			d.logger.Warn("gc: clear pending repo maintenance failed", "repo", barePath, "error", err)
		}
	}
}

// runGitGCCommand 以默认超时在某裸仓库上跑一个 GC 用 git 命令。
func runGitGCCommand(barePath string, args ...string) (string, error) {
	return runGitGCCommandContext(context.Background(), barePath, args...)
}

// runGitCommand 以给定超时在某裸仓库上跑任意 git 命令。
func runGitCommand(barePath string, timeout time.Duration, args ...string) (string, error) {
	return runGitCommandContext(context.Background(), barePath, timeout, args...)
}

func runGitGCCommandContext(ctx context.Context, barePath string, args ...string) (string, error) {
	return runGitCommandContext(ctx, barePath, gitCmdTimeout, args...)
}

func runGitCommandContext(parent context.Context, barePath string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmdArgs := append([]string{"-C", barePath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := processtree.CombinedOutput(ctx, cmd, 5*time.Second)
	return strings.TrimSpace(string(out)), err
}

// repoMaintenanceLockSnapshot 是一次维护前的锁文件快照。
type repoMaintenanceLockSnapshot map[string]struct{}

// snapshotRepoMaintenanceLocks 只记录已知由下面的维护命令产生的锁路径。随后的清理只
// 在“该路径快照里没有、且进程树已确认消失”时删除它。检出在同一个仓库门上等待，任务
// 派发等 CancelMaintenance 的屏障，因此在清理前不会有 agent git 工作造出竞争的锁。
func snapshotRepoMaintenanceLocks(barePath string) repoMaintenanceLockSnapshot {
	locks := make(repoMaintenanceLockSnapshot)
	for _, path := range repoMaintenanceLockPaths(barePath) {
		locks[path] = struct{}{}
	}
	return locks
}

// repoMaintenanceLockPaths 枚举一次被抢占的维护可能留下的锁文件（gc.pid、
// packed-refs.lock 以及 refs/objects 等目录下的 *.lock）。
func repoMaintenanceLockPaths(barePath string) []string {
	var locks []string
	for _, name := range []string{"gc.pid", "packed-refs.lock"} {
		path := filepath.Join(barePath, name)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			locks = append(locks, path)
		}
	}
	for _, root := range []string{
		filepath.Join(barePath, "refs"),
		filepath.Join(barePath, "logs", "refs"),
		filepath.Join(barePath, "objects", "info"),
		filepath.Join(barePath, "objects", "pack"),
	} {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.Type()&linkedDirModes != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".lock") {
				locks = append(locks, path)
			}
			return nil
		})
	}
	return locks
}

// cleanupNewRepoMaintenanceLocks 删除一次被抢占的维护相对快照新增、且进程树已确认的
// 锁文件。
func (d *Daemon) cleanupNewRepoMaintenanceLocks(barePath string, before repoMaintenanceLockSnapshot) {
	for _, path := range repoMaintenanceLockPaths(barePath) {
		if _, existed := before[path]; existed {
			continue
		}
		rel, err := filepath.Rel(barePath, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			d.logger.Warn("gc: refused maintenance lock cleanup outside repo", "repo", barePath, "path", path)
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			d.logger.Warn("gc: maintenance lock cleanup failed", "repo", barePath, "lock", rel, "error", err)
			continue
		}
		d.logger.Info("gc: removed lock left by interrupted maintenance", "repo", barePath, "lock", rel)
	}
}

// agentWorktreeBranches 返回某裸仓库“当前仍被 worktree 使用”的 agent/ 分支集合。
func agentWorktreeBranches(barePath string) (map[string]struct{}, error) {
	return agentWorktreeBranchesContext(context.Background(), barePath)
}

func agentWorktreeBranchesContext(ctx context.Context, barePath string) (map[string]struct{}, error) {
	out, err := runGitGCCommandContext(ctx, barePath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	branches := make(map[string]struct{})
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "branch refs/heads/") {
			continue
		}
		branch := strings.TrimPrefix(line, "branch refs/heads/")
		if strings.HasPrefix(branch, "agent/") {
			branches[branch] = struct{}{}
		}
	}
	return branches, nil
}

// listAgentBranches 返回某裸仓库里 agent/ 命名空间下所有分支。
func listAgentBranches(barePath string) ([]string, error) {
	return listAgentBranchesContext(context.Background(), barePath)
}

func listAgentBranchesContext(ctx context.Context, barePath string) ([]string, error) {
	// 尾斜杠把模式收窄到 `agent/` 命名空间。没有它，`for-each-ref` 还会返回一个字面
	// 名叫 `agent` 的分支——agentWorktreeBranches 会忽略它——那个分支于是会被删掉。
	out, err := runGitGCCommandContext(ctx, barePath, "for-each-ref", "--format=%(refname:short)", "refs/heads/agent/")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var branches []string
	for _, line := range strings.Split(out, "\n") {
		branch := strings.TrimSpace(line)
		if branch == "" {
			continue
		}
		branches = append(branches, branch)
	}
	return branches, nil
}

// isBareRepo 检查一个路径看起来是不是裸 git 仓库。
func isBareRepo(path string) bool {
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, "objects")); err != nil {
		return false
	}
	return true
}

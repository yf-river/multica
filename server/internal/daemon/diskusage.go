package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// TaskDiskUsage 描述一个任务 workdir 在磁盘上占用的空间。
//
// ParentID 是按 Kind 区分口径、真正管辖该目录生命周期的那个记录 id（issue id、chat
// 会话 id、autopilot run id 或 task id）。ParentStatus 是该记录的当前状态；在
// ResolveParentStatuses 填进来之前一直为空，因为 ScanDiskUsage 本身纯本地，
// .gc_meta.json 并不持久化状态。
type TaskDiskUsage struct {
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceShort    string `json:"workspace_short"`
	TaskShort         string `json:"task_short"`
	Path              string `json:"path"`
	Kind              string `json:"kind"`
	ParentID          string `json:"parent_id,omitempty"`
	ParentStatus      string `json:"parent_status"`
	AgeSeconds        int64  `json:"age_seconds"`
	SizeBytes         int64  `json:"size_bytes"`
	ArtifactSizeBytes int64  `json:"artifact_size_bytes"`
}

// WorkspaceDiskUsage 聚合某个 workspace 跨全部任务的占用。ArtifactRatio 是可被 GC
// artifact 清理回收的部分占 SizeBytes 的比例（0..1）——放在这里，JSON 消费端就不用
// 重新推导（表格视图渲染该列时也不用在空 workspace 上除以零）。
type WorkspaceDiskUsage struct {
	WorkspaceID       string  `json:"workspace_id"`
	WorkspaceShort    string  `json:"workspace_short"`
	TaskCount         int     `json:"task_count"`
	SizeBytes         int64   `json:"size_bytes"`
	ArtifactSizeBytes int64   `json:"artifact_size_bytes"`
	ArtifactRatio     float64 `json:"artifact_ratio"`
	OldestAgeSeconds  int64   `json:"oldest_age_seconds"`
}

// DiskUsageReport 是单次 ScanDiskUsage 调用的完整结果。Total* 字段永远反映整个
// 扫描，绝不反映 `--top` 截断后的视图——需要显示小计的消费端自己去加和切片即可。
type DiskUsageReport struct {
	WorkspacesRoot          string               `json:"workspaces_root"`
	GeneratedAt             time.Time            `json:"generated_at"`
	ArtifactPatterns        []string             `json:"artifact_patterns"`
	ManagedArtifactSubpaths []string             `json:"managed_artifact_subpaths"`
	Tasks                   []TaskDiskUsage      `json:"tasks"`
	Workspaces              []WorkspaceDiskUsage `json:"workspaces"`
	TotalTaskCount          int                  `json:"total_task_count"`
	TotalWorkspaceCount     int                  `json:"total_workspace_count"`
	TotalSizeBytes          int64                `json:"total_size_bytes"`
	TotalArtifactSizeBytes  int64                `json:"total_artifact_size_bytes"`
	TotalArtifactRatio      float64              `json:"total_artifact_ratio"`
	// RepoCacheSizeBytes 是裸仓库缓存（.repos）的占用。它是任务目录的“兄弟”，不是
	// 任务目录本身，因此单独上报并刻意排除在 Total* 之外：那些合计描述的是任务目录，
	// 把共享缓存折进去会跟“不含它的单任务数字”重复计数。
	RepoCacheSizeBytes int64 `json:"repo_cache_size_bytes"`
	RepoCacheCount     int   `json:"repo_cache_count"`
}

// DiskUsageRoot 把一个 workspaces 根目录与推导出它的 profile 配对（"" = 默认根目录）。
// ScanDiskUsageRoots 逐个扫描并给每个报告打上 profile 标签，跨根视图就能把占用归因
// 回 profile（例如一个叫 `work` 的根目录）。
type DiskUsageRoot struct {
	Profile string
	Root    string
}

// RootDiskUsage 是 AggregateDiskUsageReport 里单个根目录的报告。
type RootDiskUsage struct {
	Profile string          `json:"profile"`
	Report  DiskUsageReport `json:"report"`
}

// AggregateDiskUsageReport 是一次遍历扫描多个 workspace 根目录的结果。Total* 字段
// 是每个根目录完整扫描的大合计（绝不是 `--top` 截断后的视图），即使每个根目录的表格
// 为了展示被裁掉，合计占用依然准确。
type AggregateDiskUsageReport struct {
	GeneratedAt             time.Time       `json:"generated_at"`
	ArtifactPatterns        []string        `json:"artifact_patterns"`
	ManagedArtifactSubpaths []string        `json:"managed_artifact_subpaths"`
	Roots                   []RootDiskUsage `json:"roots"`
	TotalTaskCount          int             `json:"total_task_count"`
	TotalWorkspaceCount     int             `json:"total_workspace_count"`
	TotalSizeBytes          int64           `json:"total_size_bytes"`
	TotalArtifactSizeBytes  int64           `json:"total_artifact_size_bytes"`
	TotalArtifactRatio      float64         `json:"total_artifact_ratio"`
	TotalRepoCacheSizeBytes int64           `json:"total_repo_cache_size_bytes"`
	TotalRepoCacheCount     int             `json:"total_repo_cache_count"`
}

// ScanDiskUsageRoots 按顺序扫描每个根目录并返回合并报告。每个根目录复用
// ScanDiskUsage——根目录不存在时给出空报告（而不是报错），与单根命令行为一致，因此
// 从没用过的 profile 根目录只是贡献零。真正读不了的根目录（例如根目录本身权限错）会
// 中止整个扫描。
func ScanDiskUsageRoots(roots []DiskUsageRoot, artifactPatterns []string) (AggregateDiskUsageReport, error) {
	agg := AggregateDiskUsageReport{GeneratedAt: time.Now().UTC()}
	matcher := newArtifactMatcher(artifactPatterns, execenv.ManagedReclaimableArtifactSubpaths())
	agg.ArtifactPatterns = sortedKeys(matcher.basenames)
	agg.ManagedArtifactSubpaths = matcher.managedSubpaths()

	for _, r := range roots {
		report, err := ScanDiskUsage(r.Root, artifactPatterns)
		if err != nil {
			return agg, err
		}
		agg.Roots = append(agg.Roots, RootDiskUsage{Profile: r.Profile, Report: report})
		agg.TotalTaskCount += report.TotalTaskCount
		agg.TotalWorkspaceCount += report.TotalWorkspaceCount
		agg.TotalSizeBytes += report.TotalSizeBytes
		agg.TotalArtifactSizeBytes += report.TotalArtifactSizeBytes
		agg.TotalRepoCacheSizeBytes += report.RepoCacheSizeBytes
		agg.TotalRepoCacheCount += report.RepoCacheCount
	}
	agg.TotalArtifactRatio = ratio(agg.TotalArtifactSizeBytes, agg.TotalSizeBytes)
	return agg, nil
}

// DiskUsageKindUnknown 是 .gc_meta.json 缺失或不可读的任务目录上报的 kind。与 GC
// orphan 路径对它们的处理一致——磁盘上确实存在，但没有一条能锁定的父记录。
const DiskUsageKindUnknown = "unknown"

// ScanDiskUsage 遍历 workspacesRoot 并返回磁盘占用报告。遍历是只读的、绝不跟随
// 符号链接、只统计常规文件。artifactPatterns 会经 cleanTaskArtifacts 所用的“只看
// basename”过滤，daemon 托管的确切 artifact 路径也纳入，因此上报的 "artifact" 占用
// 与 GC 实际能回收的字节一致。.git 子树计入总量但绝不进入 artifact 占用——见
// taskSize。根目录不存在时返回空报告（而非报错）——从没跑过的 daemon 本就无目录可
// 遍历。
//
// 扫描纯本地。ParentStatus 留空；想要 STATUS 列被填上的调用方随后调用
// ResolveParentStatuses。
func ScanDiskUsage(workspacesRoot string, artifactPatterns []string) (DiskUsageReport, error) {
	report := DiskUsageReport{
		WorkspacesRoot:   workspacesRoot,
		GeneratedAt:      time.Now().UTC(),
		ArtifactPatterns: nil,
	}
	if workspacesRoot == "" {
		return report, fmt.Errorf("disk-usage: workspaces root is required")
	}

	matcher := newArtifactMatcher(artifactPatterns, execenv.ManagedReclaimableArtifactSubpaths())
	report.ArtifactPatterns = sortedKeys(matcher.basenames)
	report.ManagedArtifactSubpaths = matcher.managedSubpaths()

	wsEntries, err := os.ReadDir(workspacesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("disk-usage: read workspaces root: %w", err)
	}

	wsAgg := map[string]*WorkspaceDiskUsage{}

	for _, wsEntry := range wsEntries {
		if !wsEntry.IsDir() {
			continue
		}
		// 裸仓库缓存不是 workspace。单独量它而不是直接跳过：它按自己的节奏回收
		// （GCRepoTTL），过去在这里不可见，导致上报总数跟用户文件管理器对不上且没有
		// 明确解释。
		if wsEntry.Name() == reposDirName {
			report.RepoCacheSizeBytes, report.RepoCacheCount = repoCacheSize(filepath.Join(workspacesRoot, wsEntry.Name()))
			continue
		}
		// 其它点开头目录是 daemon 内部缓存（技能包等），绝不是 workspace。把它们当
		// workspace 数，会在 per-workspace 表格里冒出 ".skillca" 之类的行。
		if strings.HasPrefix(wsEntry.Name(), ".") {
			continue
		}
		physicalWorkspace := wsEntry.Name()
		wsDir := filepath.Join(workspacesRoot, physicalWorkspace)
		taskEntries, err := os.ReadDir(wsDir)
		if err != nil {
			continue
		}
		for _, t := range taskEntries {
			if !t.IsDir() {
				continue
			}
			taskDir := filepath.Join(wsDir, t.Name())
			usage := buildTaskUsage(taskDir, physicalWorkspace, t.Name(), matcher)

			report.Tasks = append(report.Tasks, usage)
			report.TotalSizeBytes += usage.SizeBytes
			report.TotalArtifactSizeBytes += usage.ArtifactSizeBytes

			workspaceID := usage.WorkspaceID
			ws, ok := wsAgg[workspaceID]
			if !ok {
				ws = &WorkspaceDiskUsage{
					WorkspaceID:    workspaceID,
					WorkspaceShort: usage.WorkspaceShort,
				}
				wsAgg[workspaceID] = ws
			}
			ws.TaskCount++
			ws.SizeBytes += usage.SizeBytes
			ws.ArtifactSizeBytes += usage.ArtifactSizeBytes
			if usage.AgeSeconds > ws.OldestAgeSeconds {
				ws.OldestAgeSeconds = usage.AgeSeconds
			}
		}
	}

	sort.Slice(report.Tasks, func(i, j int) bool {
		return report.Tasks[i].SizeBytes > report.Tasks[j].SizeBytes
	})

	report.Workspaces = make([]WorkspaceDiskUsage, 0, len(wsAgg))
	for _, ws := range wsAgg {
		ws.ArtifactRatio = ratio(ws.ArtifactSizeBytes, ws.SizeBytes)
		report.Workspaces = append(report.Workspaces, *ws)
	}
	sort.Slice(report.Workspaces, func(i, j int) bool {
		return report.Workspaces[i].SizeBytes > report.Workspaces[j].SizeBytes
	})

	report.TotalTaskCount = len(report.Tasks)
	report.TotalWorkspaceCount = len(report.Workspaces)
	report.TotalArtifactRatio = ratio(report.TotalArtifactSizeBytes, report.TotalSizeBytes)

	return report, nil
}

// repoCacheSize 量出裸仓库缓存大小并统计其中仓库个数。布局是
// .repos/<workspace-id>/<repo-dir>，所以个数就是第二层目录的数量——也就是 GC 驱逐
// 的单位。
func repoCacheSize(reposRoot string) (sizeBytes int64, repoCount int) {
	wsEntries, err := os.ReadDir(reposRoot)
	if err != nil {
		return 0, 0
	}
	for _, wsEntry := range wsEntries {
		if !wsEntry.IsDir() {
			continue
		}
		wsDir := filepath.Join(reposRoot, wsEntry.Name())
		repoEntries, err := os.ReadDir(wsDir)
		if err != nil {
			continue
		}
		for _, repoEntry := range repoEntries {
			if !repoEntry.IsDir() {
				continue
			}
			repoCount++
			sizeBytes += dirSize(filepath.Join(wsDir, repoEntry.Name()))
		}
	}
	return sizeBytes, repoCount
}

// ratio 返回 numerator / denominator，把 0/0（以及任何零分母）映射成 0 而不是 NaN。
// 调用方会把结果渲染成百分比，NaN 会以 "NaN%" 出现在表格里——在源头就防住。
func ratio(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

// buildPatternSet 清洗模式列表：去空白、丢弃空串与含路径分隔符的项（只认纯
// basename），得到用于匹配的集合。
func buildPatternSet(patterns []string) map[string]struct{} {
	set := make(map[string]struct{}, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "/\\") {
			continue
		}
		set[p] = struct{}{}
	}
	return set
}

// sortedKeys 返回集合的排序键列表，保证输出顺序稳定。
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildTaskUsage 汇总单个任务目录：尽量从 .gc_meta.json / managed env 元数据里还原
// 归属与年龄，否则回退到目录 mtime。
func buildTaskUsage(taskDir, wsID, taskShort string, matcher artifactMatcher) TaskDiskUsage {
	usage := TaskDiskUsage{
		WorkspaceID:    wsID,
		WorkspaceShort: ShortID(wsID),
		TaskShort:      taskShort,
		Path:           taskDir,
		Kind:           DiskUsageKindUnknown,
	}

	metaPresent := false
	if provenance, err := execenv.ReadManagedEnvProvenance(taskDir); err == nil && provenance != nil {
		if workspaceID := strings.TrimSpace(provenance.WorkspaceID); workspaceID != "" {
			usage.WorkspaceID = workspaceID
			usage.WorkspaceShort = ShortID(workspaceID)
		}
	}
	if owner, err := execenv.ReadEnvRootOwner(taskDir); err == nil && owner != nil {
		if workspaceID := strings.TrimSpace(owner.WorkspaceID); workspaceID != "" {
			usage.WorkspaceID = workspaceID
			usage.WorkspaceShort = ShortID(workspaceID)
		}
	}
	if meta, err := execenv.ReadGCMeta(taskDir); err == nil && meta != nil {
		metaPresent = true
		if workspaceID := strings.TrimSpace(meta.WorkspaceID); workspaceID != "" {
			usage.WorkspaceID = workspaceID
			usage.WorkspaceShort = ShortID(workspaceID)
		}
		usage.Kind = string(meta.Kind)
		usage.ParentID = parentIDForMeta(meta)
		if !meta.CompletedAt.IsZero() {
			usage.AgeSeconds = int64(time.Since(meta.CompletedAt).Seconds())
		} else if age, ok := gcMetaFileAge(taskDir); ok {
			usage.AgeSeconds = int64(age.Seconds())
		}
	}
	// 没有可读元数据时，像 orphanByMTime 那样用 taskDir 的 mtime。有可读元数据但没
	// 有 completed_at 的旧数据用文件自身 mtime——对应 gcDecisionIssueResult 只认
	// managed 的回退逻辑。
	if usage.AgeSeconds <= 0 && !metaPresent {
		if info, err := os.Stat(taskDir); err == nil {
			usage.AgeSeconds = int64(time.Since(info.ModTime()).Seconds())
		}
	}

	usage.SizeBytes, usage.ArtifactSizeBytes = taskSize(taskDir, matcher)
	return usage
}

// parentIDForMeta 返回管辖该任务目录生命周期的记录 id。GCMeta 是按 Kind 区分口径的
// 判别联合，只有与 Kind 匹配的那个字段有意义。
func parentIDForMeta(meta *execenv.GCMeta) string {
	switch meta.Kind {
	case execenv.GCKindIssue:
		return strings.TrimSpace(meta.IssueID)
	case execenv.GCKindChat:
		return strings.TrimSpace(meta.ChatSessionID)
	case execenv.GCKindAutopilotRun:
		return strings.TrimSpace(meta.AutopilotRunID)
	case execenv.GCKindQuickCreate:
		return strings.TrimSpace(meta.TaskID)
	default:
		return ""
	}
}

// ParentStatusFetcher 把某个 workspace 的一批 issue id 解析成当前状态。服务器没返回
// 的 id（已删除，或对当前 token 不可见）必须从结果里省略，而不是映射成占位值——调用
// 方才能区分“没解析出来”和“真实状态”。
type ParentStatusFetcher func(ctx context.Context, workspaceID string, issueIDs []string) (map[string]string, error)

// ResolveParentStatuses 把报告里所有 issue 类任务的 ParentStatus 填上。ScanDiskUsage
// 刻意不联网——本函数是选择性的第二遍，把 STATUS 列变成真实数据。
//
// 只解析 issue 类任务：它们是任务目录的绝大多数，也是唯一有批量对账端点的种类。
// chat、autopilot-run 与 quick-create 目录保持 ParentStatus 为空，而不去为它们各发
// 一次请求。
//
// 设计上就是尽力而为：某个 workspace 拉取失败时，它的任务保持未解析、错误返回给调用
// 方去呈现，但其它 workspace 照常填好。调用方不能把“返回了非 nil 错误”当成“报告不
// 可用”。
func ResolveParentStatuses(ctx context.Context, report *DiskUsageReport, fetch ParentStatusFetcher) error {
	if report == nil || fetch == nil {
		return nil
	}

	idsByWorkspace := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, task := range report.Tasks {
		if task.Kind != string(execenv.GCKindIssue) || task.ParentID == "" {
			continue
		}
		if seen[task.WorkspaceID] == nil {
			seen[task.WorkspaceID] = map[string]bool{}
		}
		// 多个任务目录可能共享同一个 issue（重新派发的任务复用旧 workdir），所以问
		// 服务器之前先去重。
		if seen[task.WorkspaceID][task.ParentID] {
			continue
		}
		seen[task.WorkspaceID][task.ParentID] = true
		idsByWorkspace[task.WorkspaceID] = append(idsByWorkspace[task.WorkspaceID], task.ParentID)
	}
	if len(idsByWorkspace) == 0 {
		return nil
	}

	var firstErr error
	statuses := make(map[string]map[string]string, len(idsByWorkspace))
	for workspaceID, ids := range idsByWorkspace {
		resolved := make(map[string]string, len(ids))
		// 与 GC 循环相同的分块大小，一个超大根目录就不会触发服务器的 batch 上限。
		for start := 0; start < len(ids); start += issueGCBatchSize {
			end := min(start+issueGCBatchSize, len(ids))
			chunk, err := fetch(ctx, workspaceID, ids[start:end])
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			for id, status := range chunk {
				resolved[id] = status
			}
		}
		statuses[workspaceID] = resolved
	}

	for i := range report.Tasks {
		task := &report.Tasks[i]
		if task.Kind != string(execenv.GCKindIssue) || task.ParentID == "" {
			continue
		}
		if status, ok := statuses[task.WorkspaceID][task.ParentID]; ok {
			task.ParentStatus = status
		}
	}
	return firstErr
}

// taskSize 遍历 taskDir 并返回 (总字节, artifact 字节)。绝不跟随符号链接、只统计常规
// 文件。被 matcher 命中的目录按 artifact 子树处理——它的体积同时计入两个总数，且遍历
// 不再下钻，让数字与 GC 真对其实行 cleanTaskArtifacts 时 os.RemoveAll 能回收的字节
// 一致。
//
// .git 子树整体计入 totalBytes、但绝不进入 artifactBytes。它是用户文件管理器里能看
// 到的真实占用，一次完整的 gcActionClean 会连同任务目录一起删掉它（上报 bytes_reclaimed
// 的 dirSize 也会数它）——把它排除会让 SizeBytes 与 GC 实际能释放的对不上。遍历依然
// 拒绝进入 .git，使 artifact 统计与 cleanTaskArtifacts“绝不回收 .git 内部”保持一致。
func taskSize(taskDir string, matcher artifactMatcher) (totalBytes int64, artifactBytes int64) {
	if taskDir == "" {
		return
	}
	absRoot, err := filepath.Abs(taskDir)
	if err != nil {
		return
	}

	_ = filepath.WalkDir(absRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == absRoot {
			return nil
		}
		// 链接：绝不跟随、绝不计数。WalkDir 已经拒绝下钻符号链接，但链接文件会以
		// “非目录条目”出现在这里，而 Windows junction 会被当成 WalkDir 会下钻的
		// 目录（见 linkedDirModes）——两者都显式丢弃，让统计与 cleanTaskArtifacts
		// 不碰链接目标的立场一致。
		if entry.Type()&linkedDirModes != 0 {
			if entry.IsDir() {
				// 这是 junction：WalkDir 会下钻进链接目标。这里能安全用 SkipDir，
				// 仅因为该条目是目录——对文件返回它会把剩下的兄弟条目也跳过。
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				totalBytes += dirSize(path)
				return filepath.SkipDir
			}
			if _, ok := matcher.matchDirectory(absRoot, path, entry); ok {
				size := dirSize(path)
				totalBytes += size
				artifactBytes += size
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			totalBytes += info.Size()
		}
		return nil
	})
	return
}

// ShortID 返回 UUID 去掉横线后的前 8 个字符；不足 8 个时返回原始输入。镜像
// execenv.shortID——它在内部子包里，没有导出。
func ShortID(id string) string {
	s := strings.ReplaceAll(id, "-", "")
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

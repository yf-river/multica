// 任务环境根目录的"稳定身份"管理：把 (workspace, task) 两个稳定 id 与磁盘上
// 某个物理路径的对应关系记录进一个索引（.task_roots），确保任务无论换多少次
// 可读显示名，都始终解析到同一个物理根目录。也负责 GC 前的路径归属校验与
// 陈旧记录的清理。

package execenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	taskRootIndexDir   = ".task_roots"
	taskRootRecordFile = "root.json"
	// taskRootPendingPrefix 标识 installTaskRootRecord 用改名（rename）落定前
	// 的暂存目录。仍带着此前缀的目录从未成为任何任务的权威记录。
	taskRootPendingPrefix    = ".pending-"
	taskRootIndexMinPruneAge = time.Hour
)

// taskRootRecord 是单条稳定索引：记录某 workspace 下某 task 认领的物理根
// 目录的相对路径。磁盘记录比可读路径活得久，是唯一权威来源。
type taskRootRecord struct {
	WorkspaceID  string `json:"workspace_id"`
	TaskID       string `json:"task_id"`
	RelativePath string `json:"relative_path"`
}

// ExistingRootDir resolves an existing owner-checked index without creating or repairing directories.
// 1. Historical evidence extraction cannot allocate a new execution environment or inspect another task.
func ExistingRootDir(params RootDirParams) (string, error) {
	recordDir := taskRootRecordDir(params)
	record, err := readTaskRootRecord(recordDir)
	if err != nil {
		return "", err
	}
	root, err := validateTaskRootRecord(params, recordDir, record)
	if err != nil {
		return "", err
	}
	owner, err := ReadEnvRootOwner(root)
	if err != nil {
		return "", err
	}
	if owner.WorkspaceID != params.WorkspaceID || owner.TaskID != params.TaskID {
		return "", fmt.Errorf("task environment ownership changed")
	}
	return root, nil
}

// ResolveRootDir 返回分配给某任务的那一个物理环境根目录。第一个调用方会
// 把可读路径冻结在一个只以稳定 id 为键的索引里；之后即便可读标签被改名或
// 新增，后续认领仍保持该路径不变。
func ResolveRootDir(params RootDirParams) (string, error) {
	proposed := PredictRootDir(params)
	if proposed == "" {
		return "", nil
	}

	recordDir := taskRootRecordDir(params)
	record, err := readTaskRootRecord(recordDir)
	if err == nil {
		return validateTaskRootRecord(params, recordDir, record)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	// 索引不存在：要么是新任务，要么是稳定索引引入前就存在的旧根目录。
	candidate, err := findOwnedTaskRoot(params)
	if err != nil {
		return "", err
	}
	if candidate == "" {
		candidate = proposed
	}
	relative, err := filepath.Rel(params.WorkspacesRoot, candidate)
	if err != nil {
		return "", fmt.Errorf("execenv: make task root relative: %w", err)
	}
	record = taskRootRecord{
		WorkspaceID:  params.WorkspaceID,
		TaskID:       params.TaskID,
		RelativePath: relative,
	}
	if err := installTaskRootRecord(recordDir, record); err != nil {
		return "", err
	}

	// 别的认领者可能带着不同的可读标签抢先完成了原子安装。因此总是重新
	// 读权威记录，而不是直接返回我们自己的提议值。
	record, err = readTaskRootRecord(recordDir)
	if err != nil {
		return "", err
	}
	return validateTaskRootRecord(params, recordDir, record)
}

// taskRootRecordDir 返回某任务稳定索引记录的存放目录。键只由稳定 id 派生，
// 不含任何可读标签。
func taskRootRecordDir(params RootDirParams) string {
	return filepath.Join(
		params.WorkspacesRoot,
		taskRootIndexDir,
		stableIdentityKey(params.WorkspaceID+"\x00"+params.TaskID),
	)
}

// stableIdentityKey 把任意身份串哈希成固定长度的十六进制键（取前 16 字节），
// 用作不泄露原串的目录名。
func stableIdentityKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

// readTaskRootRecord 读取并解码一条稳定索引记录。
func readTaskRootRecord(recordDir string) (taskRootRecord, error) {
	recordPath := filepath.Join(recordDir, taskRootRecordFile)
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return taskRootRecord{}, err
	}
	var record taskRootRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return taskRootRecord{}, fmt.Errorf("execenv: decode task root record %s: %w", recordPath, err)
	}
	return record, nil
}

// installTaskRootRecord 原子地写入一条索引记录：先在父目录下建临时目录、
// 写 root.json，再整体 rename 到最终目录名，避免读到半成品。
func installTaskRootRecord(recordDir string, record taskRootRecord) error {
	parent := filepath.Dir(recordDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("execenv: create task root index: %w", err)
	}
	tmpDir, err := os.MkdirTemp(parent, taskRootPendingPrefix)
	if err != nil {
		return fmt.Errorf("execenv: create task root record: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("execenv: encode task root record: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, taskRootRecordFile), data, 0o644); err != nil {
		return fmt.Errorf("execenv: write task root record: %w", err)
	}
	if err := os.Rename(tmpDir, recordDir); err != nil {
		// 一个完整且非空的目录才算原子安装成功。若目标目录已存在，说明另一
		// 认领者赢了，它的记录才是权威。
		if _, readErr := readTaskRootRecord(recordDir); readErr == nil {
			return nil
		}
		return fmt.Errorf("execenv: install task root record: %w", err)
	}
	return nil
}

// validateTaskRootRecord 对任何它无法担保的东西一律"失败关闭"：一个无法证明
// 哪个根目录属于自己的任务，绝不能退回去"提议一个新根"，因为原目录可能还
// 存活着工作。这种拒绝会一直持续到人工介入，所以每条错误都点名 recordDir——
// 那是唯一需要检查或删除的路径。
func validateTaskRootRecord(params RootDirParams, recordDir string, record taskRootRecord) (string, error) {
	if record.WorkspaceID != params.WorkspaceID || record.TaskID != params.TaskID {
		return "", fmt.Errorf("execenv: task root record %s belongs to workspace %s task %s, not workspace %s task %s",
			recordDir, record.WorkspaceID, record.TaskID, params.WorkspaceID, params.TaskID)
	}
	relative := filepath.Clean(record.RelativePath)
	if relative == "." || filepath.IsAbs(relative) {
		return "", fmt.Errorf("execenv: task root record %s holds invalid relative path %q", recordDir, record.RelativePath)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == ".." || parts[1] == ".." {
		return "", fmt.Errorf("execenv: task root record %s holds invalid relative path %q", recordDir, record.RelativePath)
	}
	if !validTaskRootSegment(parts[0], params.WorkspaceID, true) || !validTaskRootSegment(parts[1], params.TaskID, false) {
		return "", fmt.Errorf("execenv: task root record %s points at %q, which does not match its stable identity", recordDir, record.RelativePath)
	}
	return filepath.Join(params.WorkspacesRoot, relative), nil
}

// validTaskRootSegment 校验路径段是否属于给定 id：workspace 段允许等于完整
// id，task 段允许等于 taskKey 短键，两者都允许"可读前缀-taskKey"后缀形式。
func validTaskRootSegment(segment, id string, workspace bool) bool {
	segment = strings.ToLower(segment)
	id = strings.ToLower(id)
	key := strings.ToLower(taskKey(id))
	if workspace && segment == id {
		return true
	}
	if !workspace && segment == key {
		return true
	}
	return strings.HasSuffix(segment, "-"+key)
}

// ValidateEnvRootOwnerPath 证明 envRoot 是 workspacesRoot 下、由 owner 命名
// 的那个"两级任务根目录"。GC 调用方在每次改动前都调用它：配置可能把
// WorkspacesRoot 指向任意用户拥有的目录树，而"目录很旧"或"缺元数据"并不能
// 证明它是 daemon 建的。
func ValidateEnvRootOwnerPath(workspacesRoot, envRoot string, owner EnvRootOwner) error {
	if strings.TrimSpace(workspacesRoot) == "" || strings.TrimSpace(envRoot) == "" {
		return errors.New("execenv: workspaces root and env root are required")
	}
	if owner.WorkspaceID == "" || owner.TaskID == "" {
		return errors.New("execenv: env root owner must name both workspace and task")
	}

	relative, err := filepath.Rel(workspacesRoot, envRoot)
	if err != nil {
		return fmt.Errorf("execenv: make env root relative: %w", err)
	}
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) {
		return fmt.Errorf("execenv: env root %s is not a task directory below %s", envRoot, workspacesRoot)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == ".." || parts[1] == ".." {
		return fmt.Errorf("execenv: env root %s is not exactly two levels below %s", envRoot, workspacesRoot)
	}
	if !validTaskRootSegment(parts[0], owner.WorkspaceID, true) {
		return fmt.Errorf("execenv: workspace directory %q does not match owner %s", parts[0], owner.WorkspaceID)
	}
	if !validTaskRootSegment(parts[1], owner.TaskID, false) {
		return fmt.Errorf("execenv: task directory %q does not match owner %s", parts[1], owner.TaskID)
	}
	return nil
}

// RemoveRootDirRecord 在 GC 已回收一个终态任务根后，删除其稳定索引。它先
// 验证记录仍指向 envRoot，确保一次过期的清理永远不会删掉别的任务的身份。
func RemoveRootDirRecord(workspacesRoot, envRoot string, owner EnvRootOwner) error {
	if workspacesRoot == "" || envRoot == "" || owner.WorkspaceID == "" || owner.TaskID == "" {
		return nil
	}
	params := RootDirParams{
		WorkspacesRoot: workspacesRoot,
		WorkspaceID:    owner.WorkspaceID,
		TaskID:         owner.TaskID,
	}
	recordDir := taskRootRecordDir(params)
	record, err := readTaskRootRecord(recordDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	resolved, err := validateTaskRootRecord(params, recordDir, record)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Clean(envRoot) {
		return fmt.Errorf("execenv: task root record points to %s, not reclaimed root %s", resolved, envRoot)
	}
	if err := os.RemoveAll(recordDir); err != nil {
		return fmt.Errorf("execenv: remove task root record: %w", err)
	}
	// 索引目录本身刻意保留。若最后一条记录消失时删掉它，会与
	// installTaskRootRecord 竞争——后者先 MkdirAll(parent) 再
	// MkdirTemp(parent, ...)：若删除恰好落在两步之间，认领会吃到 ENOENT
	// 而使任务失败，为了一个空目录做这种交换不划算。
	return nil
}

// PruneTaskRootIndex 清理两类残留：被弃置、从未发布的暂存目录，以及"物理
// 环境根从未出现"的稳定记录。完整记录只有在 eligible 确认任务已终态或
// 不可达时才删除；仍在运行/非终态的任务即使根目录缺失，也保留其冻结路径。
//
// 最小宽限期保护了发布前那短短的时间窗，即使运维把通用孤儿 TTL 配成 0 也
// 不会误删。
func PruneTaskRootIndex(workspacesRoot string, retention time.Duration, now time.Time, eligible func(workspaceID, taskID string) bool) (removed int, err error) {
	indexDir := filepath.Join(workspacesRoot, taskRootIndexDir)
	entries, readErr := os.ReadDir(indexDir)
	if errors.Is(readErr, os.ErrNotExist) {
		return 0, nil
	}
	if readErr != nil {
		return 0, fmt.Errorf("execenv: read task root index: %w", readErr)
	}

	pruneAge := retention
	if pruneAge < taskRootIndexMinPruneAge {
		pruneAge = taskRootIndexMinPruneAge
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		entryPath := filepath.Join(indexDir, entry.Name())
		age, ok := taskRootRecordAge(entryPath, now)
		if !ok || age <= pruneAge {
			continue
		}

		if strings.HasPrefix(entry.Name(), taskRootPendingPrefix) {
			if removeErr := os.RemoveAll(entryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove stale task root pending entry %s: %w", entryPath, removeErr))
				continue
			}
			removed++
			continue
		}

		record, recordErr := readTaskRootRecord(entryPath)
		if errors.Is(recordErr, os.ErrNotExist) {
			if removeErr := os.RemoveAll(entryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove stale empty task root record %s: %w", entryPath, removeErr))
				continue
			}
			removed++
			continue
		}
		if recordErr != nil {
			errs = append(errs, fmt.Errorf("read stale task root record %s: %w", entryPath, recordErr))
			continue
		}
		params := RootDirParams{
			WorkspacesRoot: workspacesRoot,
			WorkspaceID:    record.WorkspaceID,
			TaskID:         record.TaskID,
		}
		resolved, validateErr := validateTaskRootRecord(params, entryPath, record)
		if validateErr != nil {
			errs = append(errs, fmt.Errorf("validate stale task root record %s: %w", entryPath, validateErr))
			continue
		}
		if _, statErr := os.Stat(resolved); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("inspect env root for record %s: %w", entryPath, statErr))
			continue
		}
		if eligible == nil || !eligible(record.WorkspaceID, record.TaskID) {
			continue
		}
		if removeErr := os.RemoveAll(entryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove stale task root record %s: %w", entryPath, removeErr))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// findOwnedTaskRoot 收养在稳定索引存在之前创建的旧根目录：owner 标记才是
// 权威依据，可读后缀只用来收窄扫描范围。
func findOwnedTaskRoot(params RootDirParams) (string, error) {
	workspaceEntries, err := os.ReadDir(params.WorkspacesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("execenv: scan existing task roots: %w", err)
	}
	workspaceSuffix := strings.ToLower(taskKey(params.WorkspaceID))
	var found string
	for _, workspaceEntry := range workspaceEntries {
		if !workspaceEntry.IsDir() || strings.HasPrefix(workspaceEntry.Name(), ".") {
			continue
		}
		name := workspaceEntry.Name()
		if name != params.WorkspaceID && !strings.HasSuffix(strings.ToLower(name), "-"+workspaceSuffix) {
			continue
		}
		workspaceDir := filepath.Join(params.WorkspacesRoot, name)
		taskEntries, readErr := os.ReadDir(workspaceDir)
		if readErr != nil {
			return "", fmt.Errorf("execenv: read candidate workspace root %s: %w", workspaceDir, readErr)
		}
		for _, taskEntry := range taskEntries {
			if !taskEntry.IsDir() {
				continue
			}
			taskName := strings.ToLower(taskEntry.Name())
			taskSuffix := strings.ToLower(taskKey(params.TaskID))
			if taskName != taskSuffix && !strings.HasSuffix(taskName, "-"+taskSuffix) {
				continue
			}
			candidate := filepath.Join(workspaceDir, taskEntry.Name())
			owner, readErr := ReadEnvRootOwner(candidate)
			if readErr != nil {
				return "", fmt.Errorf("execenv: read candidate env root owner for %s: %w", candidate, readErr)
			}
			if owner.TaskID == "" {
				// 目录没有 owner 标记：只有确认它空无一物才可能属于某个旧任务。
				hasWork, inspectErr := envRootHoldsWork(candidate)
				if inspectErr != nil {
					return "", fmt.Errorf("execenv: inspect candidate env root %s: %w", candidate, inspectErr)
				}
				if hasWork {
					return "", fmt.Errorf("execenv: candidate env root %s holds files but has no owner", candidate)
				}
			} else if owner.TaskID != params.TaskID {
				continue
			}
			if owner.WorkspaceID != "" && owner.WorkspaceID != params.WorkspaceID {
				continue
			}
			if found != "" && found != candidate {
				return "", fmt.Errorf("execenv: task %s owns multiple env roots", params.TaskID)
			}
			found = candidate
		}
	}
	return found, nil
}

// taskRootRecordAge 用记录里的文件来给记录计时：root.json 在暂存目录内只写
// 一次、永不被重写，所以其 mtime 就是安装时间。若暂存目录在写入前就死了、
// 没有文件，则回退到目录本身，让这类残留仍可被回收。
func taskRootRecordAge(recordDir string, now time.Time) (time.Duration, bool) {
	if info, err := os.Stat(filepath.Join(recordDir, taskRootRecordFile)); err == nil {
		return now.Sub(info.ModTime()), true
	}
	info, err := os.Stat(recordDir)
	if err != nil {
		return 0, false
	}
	return now.Sub(info.ModTime()), true
}

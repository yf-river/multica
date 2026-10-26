package execenv

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pelletier/go-toml/v2"
	"modernc.org/sqlite"
)

type CodexNativeState struct {
	SQLiteHome string
	ResumePath string
}

var errCodexNativeStateInUse = errors.New("Codex native state source is still in use by an external process")

// CLI 主进程结束后，辅助进程可能仍在收尾；只等待，不终止缺少所有权记录的进程。
func waitForCodexNativeStateIdle(ctx context.Context, home string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := ensureCodexNativeStateIdle(home)
		if !errors.Is(err, errCodexNativeStateInUse) {
			return err
		}
		select {
		case <-waitCtx.Done():
			return errors.Join(err, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// NativeStateDir 与 rollout 共用会话生命周期，按 runtime 隔离原生数据库。
func CodexNativeStateDir(store, runtimeID string) string {
	sum := sha256.Sum256([]byte(runtimeID))
	return filepath.Join(store, ".native-state", hex.EncodeToString(sum[:]))
}

// PrepareCodexNativeState 的调用者持有会话 writer lock 与原任务目录占用。
// 来源仅限已验证归属的任务 home；用户配置的外部 sqlite_home 由原生 CLI 自行管理。
func PrepareCodexNativeState(ctx context.Context, home, source, store, runtimeID, resumeID string, retire func(context.Context, string) error) (CodexNativeState, error) {
	var result CodexNativeState
	if home == "" || store == "" || runtimeID == "" {
		return result, errors.New("Codex native state requires a task home, conversation store and runtime")
	}
	if err := privateCodexNativeDirectory(home); err != nil {
		return result, err
	}
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return result, err
	}
	var settings map[string]any
	if err := toml.Unmarshal(config, &settings); err != nil {
		return result, err
	}
	if configured, _ := settings["sqlite_home"].(string); configured != "" {
		return result, nil
	}
	if source != "" {
		if err := privateCodexNativeDirectory(source); err != nil {
			return result, err
		}
	}
	state := CodexNativeStateDir(store, runtimeID)
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		return result, err
	}
	if info, err := os.Lstat(state); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, errors.New("Codex native state directory is not owned storage")
		}
		data, err := os.ReadFile(filepath.Join(state, "multica-state.json"))
		var marker struct {
			Version int `json:"version"`
		}
		if err != nil || json.Unmarshal(data, &marker) != nil || marker.Version != 1 {
			return result, errors.New("Codex native state has no completed snapshot")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	} else {
		// 有状态的当前 home 优先；否则只用服务端指定的原任务。
		selected := source
		files, err := codexNativeDatabases(home)
		if err != nil {
			return result, err
		}
		if len(files) > 0 {
			selected = home
		} else if selected != "" {
			files, err = codexNativeDatabases(selected)
			if err != nil {
				return result, err
			}
		}
		if resumeID != "" && len(files) == 0 {
			return result, errors.New("Codex native state source is unavailable; refusing to reset the resumed Goal")
		}
		if selected != "" && retire != nil {
			if err := retire(ctx, selected); err != nil {
				return result, err
			}
		}
		if selected != "" {
			if err := waitForCodexNativeStateIdle(ctx, selected); err != nil {
				return result, err
			}
		}
		if _, err := restoreCodexNativeRollouts(ctx, home, source, store, resumeID); err != nil {
			return result, err
		}
		temporary, err := os.MkdirTemp(filepath.Dir(state), ".snapshot-")
		if err != nil {
			return result, err
		}
		defer os.RemoveAll(temporary)
		for _, name := range files {
			if err := snapshotCodexDatabase(ctx, filepath.Join(selected, name), filepath.Join(temporary, name)); err != nil {
				return result, fmt.Errorf("snapshot Codex native database %s: %w", name, err)
			}
		}
		if err := relocateCodexNativeSnapshot(ctx, temporary, store); err != nil {
			return result, err
		}
		marker, _ := json.Marshal(map[string]any{"version": 1, "source_home": selected})
		if err := os.WriteFile(filepath.Join(temporary, "multica-state.json"), marker, 0600); err != nil {
			return result, err
		}
		if err := os.Rename(temporary, state); err != nil {
			return result, err
		}
	}
	result.SQLiteHome = state
	result.ResumePath, err = restoreCodexNativeRollouts(ctx, home, source, store, resumeID)
	return result, err
}

func restoreCodexNativeRollouts(ctx context.Context, home, source, store, resumeID string) (string, error) {
	if source != "" && source != home {
		if err := preserveCodexNativeRollouts(ctx, source, store); err != nil {
			return "", err
		}
	}
	if err := preserveCodexNativeRollouts(ctx, home, store); err != nil {
		return "", err
	}
	if resumeID != "" {
		// 恢复结果须指向持久 rollout；分页线程按 ID 恢复，不传旧索引拒绝的新 path。
		paths := findCodexRollouts(store, resumeID)
		if len(paths) == 0 && source != "" {
			for _, path := range findCodexRollouts(filepath.Join(source, "sessions"), resumeID) {
				relative, err := filepath.Rel(filepath.Join(source, "sessions"), path)
				if err != nil || !filepath.IsLocal(relative) {
					return "", errors.New("Codex resume rollout is outside its task store")
				}
				destination := filepath.Join(store, relative)
				if err := copyCodexNativeRollout(ctx, path, destination); err != nil {
					return "", err
				}
			}
			paths = findCodexRollouts(store, resumeID)
		}
		if len(paths) == 1 && supportedCodexNativeRollout(paths[0]) {
			if err := verifyCodexNativeRollout(paths[0], resumeID); err != nil {
				return "", err
			}
			return paths[0], nil
		} else if len(paths) > 1 {
			return "", errors.New("Codex resume has multiple authoritative rollouts")
		}
		return "", errors.New("Codex native resume has no supported persistent rollout")
	}
	return "", nil
}

// 0.164 的分页历史保存在 thread_history SQLite 中，rollout 不能重建它。
// 只调整尚未发布的 state_5 快照的文件位置，不修改来源、Goal、历史内容或原生 schema。
func relocateCodexNativeSnapshot(ctx context.Context, snapshot, store string) error {
	names, err := codexNativeDatabases(snapshot)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasPrefix(name, "state_") {
			version, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "state_"), ".sqlite"))
			if err != nil || version > 5 {
				return errors.New("Codex native snapshot index version is unsupported")
			}
		}
	}
	path := filepath.Join(snapshot, "state_5.sqlite")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	database, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		return err
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id,rollout_path FROM threads WHERE archived=0")
	if err != nil {
		return errors.New("Codex native snapshot index format is unsupported")
	}
	type location struct{ id, old, current string }
	var locations []location
	for rows.Next() {
		var item location
		if err := rows.Scan(&item.id, &item.old); err != nil {
			_ = rows.Close()
			return err
		}
		matches := findCodexRollouts(store, item.id)
		if len(matches) != 1 || !supportedCodexNativeRollout(matches[0]) {
			_ = rows.Close()
			return errors.New("Codex native snapshot has history outside its persistent conversation store")
		}
		item.current = matches[0]
		if err := verifyCodexNativeRollout(item.current, item.id); err != nil {
			_ = rows.Close()
			return err
		}
		locations = append(locations, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range locations {
		updated, err := tx.ExecContext(ctx, "UPDATE threads SET rollout_path=? WHERE id=? AND rollout_path=?", item.current, item.id, item.old)
		if err != nil {
			return err
		}
		if count, err := updated.RowsAffected(); err != nil || count != 1 {
			return errors.New("Codex native snapshot index changed during relocation")
		}
	}
	return tx.Commit()
}

func verifyCodexNativeRollout(path, id string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var input io.Reader = file
	if strings.HasSuffix(path, ".jsonl.zst") {
		decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(64<<20))
		if err != nil {
			return fmt.Errorf("read compressed Codex rollout metadata: %w", err)
		}
		defer decoder.Close()
		input = decoder
	}
	var metadata struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.NewDecoder(io.LimitReader(input, 32<<20)).Decode(&metadata) == nil && metadata.Type == "session_meta" && metadata.Payload.ID == id {
		return nil
	}
	return errors.New("Codex native rollout metadata does not match its indexed thread")
}

func supportedCodexNativeRollout(path string) bool {
	return strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".jsonl.zst")
}

// 原任务的真目录先保留为备份；原路径连接持久存储，兼容原生索引内的旧绝对路径。
func preserveCodexNativeRollouts(ctx context.Context, home, store string) error {
	path := filepath.Join(home, "sessions")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return createDirLink(store, path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		storeInfo, statErr := os.Stat(store)
		targetInfo, targetErr := os.Stat(target)
		if sameCodexPath(target, store) || (statErr == nil && targetErr == nil && os.SameFile(storeInfo, targetInfo)) {
			return nil
		}
		return errors.New("Codex native rollout source is not its conversation store")
	}
	if !info.IsDir() {
		return errors.New("Codex native sessions path is not a directory")
	}
	if err := filepath.WalkDir(path, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("Codex task rollout contains an unsupported link")
		}
		relative, err := filepath.Rel(path, source)
		if err != nil || !filepath.IsLocal(relative) {
			return errors.New("Codex task rollout is outside its source")
		}
		destination := filepath.Join(store, relative)
		if existing, err := os.Stat(destination); err == nil {
			original, statErr := entry.Info()
			if statErr == nil && os.SameFile(existing, original) {
				return nil
			}
			if statErr == nil && original.Size() == existing.Size() {
				same, err := sameCodexNativeFile(ctx, source, destination)
				if err != nil {
					return err
				}
				if same {
					return nil
				}
			}
			return errors.New("Codex rollout destination already has independent history")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return copyCodexNativeRollout(ctx, source, destination)
	}); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(home, ".sessions-backup-")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	if err := createDirLink(store, path); err != nil {
		return errors.Join(err, os.Rename(backup, path))
	}
	return nil
}

func sameCodexNativeFile(ctx context.Context, left, right string) (bool, error) {
	var sums []string
	for _, path := range []string{left, right} {
		file, err := os.Open(path)
		if err != nil {
			return false, err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, &codexContextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return false, err
		}
		sums = append(sums, hex.EncodeToString(hash.Sum(nil)))
	}
	return sums[0] == sums[1], nil
}

func privateCodexNativeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Codex native state source is not a task directory")
	}
	return nil
}

func codexNativeDatabases(home string) ([]string, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sqlite") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, errors.New("Codex native database is not a regular task file")
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func snapshotCodexDatabase(ctx context.Context, source, destination string) error {
	u := url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	database, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	connection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	// Backup 保留页与 rowid；VACUUM 可能重排未显式声明 INTEGER PRIMARY KEY 的行。
	if err := connection.Raw(func(raw any) (err error) {
		backuper, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("Codex state snapshot driver has no SQLite backup support")
		}
		backup, err := backuper.NewBackup((&url.URL{Scheme: "file", Path: destination}).String())
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, backup.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(128)
			if err != nil || !more {
				return err
			}
		}
	}); err != nil {
		return err
	}
	if err := os.Chmod(destination, 0600); err != nil {
		return err
	}
	verification, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: destination, RawQuery: "mode=ro"}).String())
	if err != nil {
		return err
	}
	defer verification.Close()
	var integrity string
	if err := verification.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("Codex native snapshot integrity check failed")
	}
	return nil
}

func copyCodexNativeRollout(ctx context.Context, source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(destination), ".rollout-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	_, copyErr := io.Copy(output, &codexContextReader{ctx: ctx, reader: input})
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(output.Name(), destination)
}

type codexContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *codexContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

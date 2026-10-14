package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/service"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// prepareSkillFiles 保存不可变对象。台账先于上传落库，失败和崩溃遗留由清理任务回收。
func (h *Handler) prepareSkillFiles(ctx context.Context, workspaceID, skillID pgtype.UUID, files []CreateSkillFileRequest) error {
	if len(files) > skillpkg.MaxFiles {
		return fmt.Errorf("skill exceeds %d supporting files", skillpkg.MaxFiles)
	}
	seen := map[string]bool{}
	total := int64(0)
	existing := map[string]db.SkillFile{}
	if skillID.Valid {
		rows, err := h.Queries.ListSkillFiles(ctx, skillID)
		if err != nil {
			return err
		}
		for _, f := range rows {
			existing[f.Path] = f
		}
	}
	// 先验证整个请求，再写对象，避免无效包产生不必要的上传。
	for i := range files {
		f := &files[i]
		if skillpkg.IsReservedContentPath(f.Path) {
			continue
		}
		if !skillpkg.SafePath(f.Path) {
			return fmt.Errorf("invalid supporting file path: %s", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("duplicate file path: %s", f.Path)
		}
		seen[f.Path] = true
		f.Mode = skillpkg.FileMode(f.Mode)
		if f.Kind == "binary" && f.data == nil {
			old, ok := existing[f.Path]
			if !ok || uuidToString(old.ID) != f.ID {
				for _, candidate := range existing {
					if uuidToString(candidate.ID) == f.ID {
						old = candidate
						ok = true
						break
					}
				}
			}
			if !ok || old.Kind != "binary" || f.ID != uuidToString(old.ID) || f.Content != "" {
				return fmt.Errorf("binary file %s must be replaced by upload", f.Path)
			}
			f.storageKey, f.contentType, f.sizeBytes, f.sha256, f.Mode = old.StorageKey, old.ContentType, old.SizeBytes, old.Sha256, old.Mode
		} else {
			if old, ok := existing[f.Path]; ok && old.Kind == "binary" && f.data == nil {
				return fmt.Errorf("binary file %s must be preserved by id or replaced by upload", f.Path)
			}
			data := f.data
			if data == nil {
				data = []byte(f.Content)
			}
			f.Kind = skillpkg.FileKind(f.Path, data)
			if f.Kind == "binary" && f.data == nil {
				return fmt.Errorf("binary file %s requires multipart upload", f.Path)
			}
			f.sizeBytes = int64(len(data))
			f.sha256 = contentHash(string(data))
			f.contentType = skillpkg.ContentType(data)
			if f.Kind == "binary" {
				f.Content = ""
			} else {
				f.Content = string(data)
			}
		}
		if f.sizeBytes > skillpkg.MaxFileBytes {
			return fmt.Errorf("file %s exceeds 16 MiB", f.Path)
		}
		total += f.sizeBytes
		if total > skillpkg.MaxBundleBytes {
			return fmt.Errorf("skill exceeds 64 MiB")
		}
	}
	for i := range files {
		f := &files[i]
		if f.Kind != "binary" || f.storageKey != "" {
			continue
		}
		if h.Storage == nil {
			return fmt.Errorf("skill attachment storage unavailable")
		}
		f.storageKey = "private/skills/" + uuidToString(workspaceID) + "/" + uuid.NewString()
		if err := h.Queries.RegisterSkillAsset(ctx, db.RegisterSkillAssetParams{StorageKey: f.storageKey, WorkspaceID: workspaceID}); err != nil {
			return err
		}
		uploadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err := h.Storage.Upload(uploadCtx, f.storageKey, f.data, f.contentType, path.Base(f.Path))
		cancel()
		if err != nil {
			return fmt.Errorf("upload %s: %w", f.Path, err)
		}
	}
	return nil
}

func upsertPreparedSkillFile(ctx context.Context, q *db.Queries, skillID pgtype.UUID, f CreateSkillFileRequest) (db.SkillFile, error) {
	// 模板创建只有文本文件，无需对象存储准备。
	if f.Kind == "" {
		f.Kind = "text"
		f.Mode = skillpkg.FileMode(f.Mode)
		f.contentType = "text/plain; charset=utf-8"
		f.sizeBytes = int64(len(f.Content))
		f.sha256 = contentHash(f.Content)
	}
	return q.UpsertSkillAssetFile(ctx, db.UpsertSkillAssetFileParams{SkillID: skillID, Path: f.Path, Content: f.Content, Kind: f.Kind, StorageKey: f.storageKey, ContentType: f.contentType, SizeBytes: f.sizeBytes, Sha256: f.sha256, Mode: f.Mode})
}

func (h *Handler) UploadSkillFile(w http.ResponseWriter, r *http.Request) {
	sk, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, sk) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, skillpkg.MaxFileBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, 400, "invalid upload or file exceeds 16 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, skillpkg.MaxFileBytes+1))
	if err != nil {
		writeError(w, 400, "failed to read file")
		return
	}
	filePath := r.FormValue("path")
	if filePath == "" {
		filePath = header.Filename
	}
	mode := int32(0644)
	if r.FormValue("mode") == "755" || r.FormValue("mode") == "493" {
		mode = 0755
	}
	files := []CreateSkillFileRequest{{Path: filePath, data: data, Mode: mode}}
	if err := h.prepareSkillFiles(r.Context(), sk.WorkspaceID, sk.ID, files); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	h.saveSkillFile(w, r, sk, files[0])
}

func (h *Handler) saveSkillFile(w http.ResponseWriter, r *http.Request, sk db.Skill, file CreateSkillFileRequest) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to begin upload")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	// 锁住技能行，上传和整个文件集替换沿相同的锁顺序提交。
	if _, err = q.UpdateSkill(r.Context(), db.UpdateSkillParams{ID: sk.ID}); err != nil {
		writeError(w, 409, "skill changed during upload")
		return
	}
	rows, err := q.ListSkillFiles(r.Context(), sk.ID)
	if err != nil {
		writeError(w, 500, "failed to read file list")
		return
	}
	total := file.sizeBytes
	count := 1
	for _, f := range rows {
		if f.Path == file.Path {
			continue
		}
		count++
		if f.Kind == "binary" {
			total += f.SizeBytes
		} else {
			total += int64(len(f.Content))
		}
	}
	if count > skillpkg.MaxFiles || total > skillpkg.MaxBundleBytes {
		writeError(w, 400, "skill exceeds bundle limit")
		return
	}
	result, err := upsertPreparedSkillFile(r.Context(), q, sk.ID, file)
	if err != nil {
		writeError(w, 500, "failed to save file")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit file")
		return
	}
	writeJSON(w, 200, skillFileToResponse(result))
}

func (h *Handler) GetSkillFileContent(w http.ResponseWriter, r *http.Request) {
	sk, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "fileId"), "file id")
	if !ok {
		return
	}
	f, err := h.Queries.GetSkillFile(r.Context(), id)
	if err != nil || f.SkillID != sk.ID {
		writeError(w, 404, "skill file not found")
		return
	}
	h.serveSkillFile(w, r, f, strings.HasSuffix(r.URL.Path, "/download"))
}

func (h *Handler) serveSkillFile(w http.ResponseWriter, r *http.Request, f db.SkillFile, download bool) {
	var reader io.ReadCloser
	if f.Kind == "binary" {
		if h.Storage == nil {
			writeError(w, 503, "skill attachment storage unavailable")
			return
		}
		var err error
		reader, err = h.Storage.GetReader(r.Context(), f.StorageKey)
		if err != nil {
			writeError(w, 503, "skill attachment unavailable")
			return
		}
	} else {
		reader = io.NopCloser(strings.NewReader(f.Content))
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, skillpkg.MaxFileBytes+1))
	if err != nil || len(data) > skillpkg.MaxFileBytes {
		writeError(w, 503, "skill attachment read failed")
		return
	}
	if f.Kind == "binary" && (int64(len(data)) != f.SizeBytes || contentHash(string(data)) != f.Sha256) {
		writeError(w, 503, "skill attachment checksum mismatch")
		return
	}
	disposition := "attachment"
	switch f.ContentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		if !download {
			disposition = "inline"
		}
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(f.Path)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data)
}

// RunSkillAssetCleanup 在无人准备任务时回收超过一天的未引用对象，失败留账重试。
func (h *Handler) RunSkillAssetCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.cleanupSkillAssets(ctx); err != nil {
				slog.Warn("skill asset cleanup failed", "error", err)
			}
		}
	}
}

func (h *Handler) cleanupSkillAssets(ctx context.Context) error {
	if h.Storage == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var acquired bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(5145131)").Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	q := h.Queries.WithTx(tx)
	keys, err := q.ListUnusedSkillAssets(ctx)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err = h.Storage.DeleteObject(ctx, key); err != nil {
			return err
		}
		if err = q.DeleteSkillAsset(ctx, key); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetTaskSkillFileContent 仅向正在准备且获配该技能的任务提供指定版本的附件。
func (h *Handler) GetTaskSkillFileContent(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	task, ws, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	if ws != uuidToString(rt.WorkspaceID) || task.RuntimeID != rt.ID {
		writeError(w, 404, "task not found")
		return
	}
	if task.Status != "dispatched" && task.Status != "waiting_local_directory" {
		writeError(w, 409, "task is not preparing")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "fileId"), "file id")
	if !ok {
		return
	}
	f, err := h.Queries.GetSkillFile(r.Context(), id)
	if err != nil {
		writeError(w, 409, "skill file version changed; resolve bundle again")
		return
	}
	allowed, err := h.Queries.ListAgentSkillsByIDs(r.Context(), db.ListAgentSkillsByIDsParams{AgentID: task.AgentID, SkillIds: []pgtype.UUID{f.SkillID}})
	if err != nil {
		writeError(w, 500, "failed to authorize skill")
		return
	}
	if len(allowed) != 1 {
		writeError(w, 404, "skill file not found")
		return
	}
	if r.URL.Query().Get("sha256") != "sha256:"+f.Sha256 {
		writeError(w, 409, "skill file version changed; resolve bundle again")
		return
	}
	h.serveSkillFile(w, r, f, true)
}

func hasBinarySkillFiles(bundles []service.AgentSkillData) bool {
	for _, b := range bundles {
		for _, f := range b.Files {
			if f.Kind == "binary" {
				return true
			}
		}
	}
	return false
}

// ReplaceSkillBundle 用原生归档原子替换文件集，保留技能身份、配置和绑定。
func (h *Handler) ReplaceSkillBundle(w http.ResponseWriter, r *http.Request) {
	sk, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, sk) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, skillpkg.MaxArchiveBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, 400, "invalid archive upload")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "file is required")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, skillpkg.MaxArchiveBytes+1))
	if err != nil || len(data) > skillpkg.MaxArchiveBytes {
		writeError(w, 400, "archive exceeds 80 MiB")
		return
	}
	imported, err := parseSkillArchive(data, header.Filename)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	// 校验管理员权限已经完成，事务内再次校验技能身份。
	result, err := h.overwriteSkillWithFiles(r.Context(), skillOverwriteInput{WorkspaceID: sk.WorkspaceID, TargetSkillID: sk.ID, UserID: requestUserID(r), ExpectedName: sk.Name, Description: sk.Description, Content: imported.content, Config: json.RawMessage(sk.Config), Files: importedSkillFileRequests(imported), AllowOverwrite: func(_ string, current db.Skill) bool {
		return current.ID == sk.ID && current.WorkspaceID == sk.WorkspaceID
	}})
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

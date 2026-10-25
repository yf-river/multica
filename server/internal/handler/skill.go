// 本文件是「Skill（技能）」资源的 HTTP 处理器集合：技能 CRUD、技能文件的
// 增删改查、agent↔skill 关联（set/add/remove/enable），以及「从外部导入技能」
// ——支持三个来源：clawhub.ai、skills.sh（本质走 GitHub）、github.com 直接
// 导入，还有上传 .skill/.zip 归档的导入。
// 安全与健壮性要点：
//   - 所有写 Postgres 的文本先经 sanitizeNullBytes（去 NUL、替换非法 UTF-8）；
//   - 技能文件路径要校验防目录穿越，SKILL.md 保留给正文；
//   - 远端拉取有单文件/总量/文件数硬上限，超限直接失败（绝不静默截断）；
//   - 名字冲突按 on_conflict 策略处理（fail/overwrite/rename/skip），
//     overwrite 仅限原创建者（MUL-2701 / MUL-2800）。
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"golang.org/x/sync/errgroup"
)

// sanitizeNullBytes 让字符串安全进入 PostgreSQL TEXT 列。
//
// 是 util.SanitizeTextForPostgres 的薄别名，保留是因为约 20 个调用点都这么写。
// 共享助手统一了「可安全持久化」的定义，daemon 任务端点与 comment/skill/property
// 端点不会再对同一输入存出不同文本（GH #7098 评审）。
//
// 随本次收敛有一处行为变化：非法 UTF-8 现在替换为 U+FFFD 而非静默丢弃；
// NUL 依旧直接移除。
func sanitizeNullBytes(s string) string {
	return util.SanitizeTextForPostgres(s)
}

// --- 响应结构体 ---

// SkillResponse 是技能详情的完整线格式（含 SKILL.md 正文 content）。
type SkillResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Content     string  `json:"content"`
	Config      any     `json:"config"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// SkillSummaryResponse 是列表端点的形状：SkillResponse 除 `content` 外的全部字段。
// SKILL.md 正文常达 50–200KB，放进列表会撑爆高延迟链路下的 CLI 超时（GH
// multica-ai/multica#2174）。详情端点仍返回带 content 的完整 SkillResponse。
type SkillSummaryResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Config      any     `json:"config"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	// Enabled 只在 agent 作用域的技能响应里填充。工作区技能列表描述技能本身，
	// 故省略指派状态。
	Enabled *bool `json:"enabled,omitempty"`
}

// AgentSkillSummary 是内嵌进 Agent 负载（`GET /api/agents`、`GET
// /api/agents/{id}`）的更窄形状。agent 列表批量查询只联出渲染 UI 指派者徽章
// 所需的列；独立端点 `/api/agents/{id}/skills` 返回完整 SkillSummaryResponse，
// 供需要 source/origin 信息的调用者使用。
type AgentSkillSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// SkillFileResponse 是单个技能支持文件的完整线格式（含正文 content）。
type SkillFileResponse struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Mode        int32  `json:"mode"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	ID          string `json:"id"`
	SkillID     string `json:"skill_id"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// SkillFileMetadataResponse 是文件列表形状：SkillFileResponse 除 `content` 外的
// 全部字段，外加 size 与 hash，能回答「哪个文件让技能变这么大」而无需下载任何
// 文件。
//
// 约 600KB 的技能在每行都携带完整正文时根本无法列出——能诊断问题的命令本身
// 成了问题（GH multica-ai/multica#7498）。列表端点就该只做列表。
type SkillFileMetadataResponse struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Mode        int32  `json:"mode"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	ID          string `json:"id"`
	SkillID     string `json:"skill_id"`
	Path        string `json:"path"`
	// Size 是文件正文字节长度，在 Postgres 内计算。
	Size int64 `json:"size"`
	// ContentHash 是文件正文的十六进制 SHA-256。缓存技能文件的调用方可借此
	// 跳过未变更的下载。
	ContentHash string `json:"content_hash"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// SkillSearchCandidateResponse 是技能搜索/浏览结果里单个外部仓库技能的候选条目
// （含仓库/安装数/star 等元数据，供列表 UI 决定导入来源）。
type SkillSearchCandidateResponse struct {
	Name         string  `json:"name"`
	URL          string  `json:"url"`
	Source       string  `json:"source"`
	Repo         *string `json:"repo"`
	InstallCount *int64  `json:"install_count"`
	GitHubStars  *int64  `json:"github_stars"`
	Description  string  `json:"description"`
}

// SkillWithFilesResponse 是技能+全部文件正文的组合响应（导入/创建/更新用）。
type SkillWithFilesResponse struct {
	SkillResponse
	Files []SkillFileResponse `json:"files"`
}

// SkillWithFileMetadataResponse 是 `GET /api/skills/{id}?include=metadata` 的形状：
// 技能不带 SKILL.md 正文，其文件也不带正文。用大小与 hash 顶替被去掉的内容，
// 调用者仍能看到技能有多大、哪部分大。
type SkillWithFileMetadataResponse struct {
	SkillSummaryResponse
	// ContentSize / ContentHash 描述 `content` 本会携带的 SKILL.md 正文。
	ContentSize int64                       `json:"content_size"`
	ContentHash string                      `json:"content_hash"`
	Files       []SkillFileMetadataResponse `json:"files"`
}

// SkillImportResult 是导入类端点的结构化结果：成功（created/updated）、
// 冲突（conflict/require 决策）或跳过（skipped）。
type SkillImportResult struct {
	Status        string                  `json:"status"`
	Reason        string                  `json:"reason,omitempty"`
	Skill         *SkillWithFilesResponse `json:"skill,omitempty"`
	ExistingSkill *ExistingSkillIdentity  `json:"existing_skill,omitempty"`
}

// ExistingSkillIdentity 描述与导入技能撞名的既有技能，供客户端决定
// overwrite/rename/skip。
type ExistingSkillIdentity struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CreatedBy    string `json:"created_by,omitempty"`
	CanOverwrite bool   `json:"can_overwrite,omitempty"`
}

// writeSkillImportDuplicateConflict 返回旧的重复名冲突响应（带 existing_skill 信息）。
func writeSkillImportDuplicateConflict(w http.ResponseWriter, existing ExistingSkillIdentity) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":          "a skill with this name already exists",
		"existing_skill": existing,
	})
}

func skillToResponse(s db.Skill) SkillResponse {
	return SkillResponse{
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		Name:        s.Name,
		Description: s.Description,
		Content:     s.Content,
		Config:      decodeSkillConfig(s.Config),
		CreatedBy:   uuidToPtr(s.CreatedBy),
		CreatedAt:   timestampToString(s.CreatedAt),
		UpdatedAt:   timestampToString(s.UpdatedAt),
	}
}

func (h *Handler) existingSkillIdentityByName(ctx context.Context, workspaceID pgtype.UUID, name string) (ExistingSkillIdentity, bool, error) {
	skill, err := h.Queries.GetSkillByWorkspaceAndName(ctx, db.GetSkillByWorkspaceAndNameParams{
		WorkspaceID: workspaceID,
		Name:        name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ExistingSkillIdentity{}, false, nil
		}
		return ExistingSkillIdentity{}, false, err
	}
	return existingSkillIdentity(skill, ""), true, nil
}

func existingSkillIdentity(skill db.Skill, userID string) ExistingSkillIdentity {
	identity := ExistingSkillIdentity{
		ID:           uuidToString(skill.ID),
		Name:         skill.Name,
		CanOverwrite: canOverwriteSkillByLocalImport(userID, skill),
	}
	if skill.CreatedBy.Valid {
		identity.CreatedBy = uuidToString(skill.CreatedBy)
	}
	return identity
}

// decodeSkillConfig 解码 JSONB 的 skill.config，缺失或不可解析时默认 {}，
// 保证 API 面恒返回 JSON 对象。
func decodeSkillConfig(raw []byte) any {
	var config any
	if raw != nil {
		_ = json.Unmarshal(raw, &config)
	}
	if config == nil {
		return map[string]any{}
	}
	return config
}

func skillSummaryToResponse(
	id, workspaceID pgtype.UUID,
	name, description string,
	config []byte,
	createdBy pgtype.UUID,
	createdAt, updatedAt pgtype.Timestamptz,
) SkillSummaryResponse {
	return SkillSummaryResponse{
		ID:          uuidToString(id),
		WorkspaceID: uuidToString(workspaceID),
		Name:        name,
		Description: description,
		Config:      decodeSkillConfig(config),
		CreatedBy:   uuidToPtr(createdBy),
		CreatedAt:   timestampToString(createdAt),
		UpdatedAt:   timestampToString(updatedAt),
	}
}

func skillFileToResponse(f db.SkillFile) SkillFileResponse {
	size, hash := int64(len(f.Content)), contentHash(f.Content)
	if f.Kind == "binary" {
		size, hash = f.SizeBytes, f.Sha256
	}
	return SkillFileResponse{
		Kind: f.Kind, ContentType: f.ContentType, Mode: f.Mode, SizeBytes: size, SHA256: hash,
		ID:        uuidToString(f.ID),
		SkillID:   uuidToString(f.SkillID),
		Path:      f.Path,
		Content:   f.Content,
		CreatedAt: timestampToString(f.CreatedAt),
		UpdatedAt: timestampToString(f.UpdatedAt),
	}
}

func skillFileMetadataToResponse(f db.ListSkillFileMetadataRow) SkillFileMetadataResponse {
	return SkillFileMetadataResponse{
		Kind: f.Kind, ContentType: f.ContentType, Mode: f.Mode, SizeBytes: f.Size, SHA256: f.ContentHash,
		ID:          uuidToString(f.ID),
		SkillID:     uuidToString(f.SkillID),
		Path:        f.Path,
		Size:        f.Size,
		ContentHash: f.ContentHash,
		CreatedAt:   timestampToString(f.CreatedAt),
		UpdatedAt:   timestampToString(f.UpdatedAt),
	}
}

// contentHash 是元数据形状代替正文上报的十六进制 SHA-256。与 Postgres
// `encode(sha256(content::bytea), 'hex')` 一致，因此这里算出的 SKILL.md hash 与
// SQL 里算出的文件 hash 可比较。
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// --- 请求结构体 ---

// CreateSkillRequest 是创建技能的请求体：SKILL.md 的 name/description/content +
// 可选附带文件列表。
type CreateSkillRequest struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Content     string                   `json:"content"`
	Config      any                      `json:"config"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

// CreateSkillFileRequest 是随技能创建/更新的单个附带文件的请求体（相对路径+正文）。
type CreateSkillFileRequest struct {
	ID          string `json:"id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Mode        int32  `json:"mode,omitempty"`
	data        []byte
	storageKey  string
	contentType string
	sizeBytes   int64
	sha256      string
	Path        string `json:"path"`
	Content     string `json:"content"`
}

// UpdateSkillRequest 是更新技能的请求体：指针字段区分「未传」与「显式清空」，
// Files 传了就整体替换附带文件集合。
type UpdateSkillRequest struct {
	Name        *string                  `json:"name"`
	Description *string                  `json:"description"`
	Content     *string                  `json:"content"`
	Config      any                      `json:"config"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

// SetAgentSkillsRequest 是整体替换 agent 已启用技能集合的请求体。
type SetAgentSkillsRequest struct {
	SkillIDs []string `json:"skill_ids"`
}

// AddAgentSkillsRequest 是给 agent 追加技能的请求体。
type AddAgentSkillsRequest struct {
	SkillIDs []string `json:"skill_ids"`
}

// --- 辅助函数 ---

// validateFilePath 检查文件路径是否安全（不得穿越目录、不得为绝对路径）。
func validateFilePath(p string) bool {
	return skillpkg.SafePath(p)
}

func (h *Handler) loadSkillForUser(w http.ResponseWriter, r *http.Request, id string) (db.Skill, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Skill{}, false
	}

	skillUUID, ok := parseUUIDOrBadRequest(w, id, "skill id")
	if !ok {
		return db.Skill{}, false
	}

	skill, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{
		ID:          skillUUID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return skill, false
	}
	return skill, true
}

// --- 技能 CRUD ---

// ListSkills 处理 GET /api/skills，返回工作区技能摘要列表（不含 SKILL.md 正文）。
func (h *Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	skills, err := h.Queries.ListSkillSummariesByWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skills")
		return
	}

	resp := make([]SkillSummaryResponse, len(skills))
	for i, s := range skills {
		resp[i] = skillSummaryToResponse(
			s.ID, s.WorkspaceID, s.Name, s.Description, s.Config,
			s.CreatedBy, s.CreatedAt, s.UpdatedAt,
		)
	}

	writeJSON(w, http.StatusOK, resp)
}

// SearchSkills 处理 GET /api/skills/search?q=...，在 ClawHub 上按 q 搜索候选
// 技能（供「从社区安装」选择器使用）。上游异常时返回 502 + upstream_unavailable。
func (h *Handler) SearchSkills(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	candidates, err := searchClawHubSkills(httpClient, query)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"code":  "upstream_unavailable",
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, candidates)
}

// skill 详情与 skill 文件列表端点共享的 `include` 查询参数取值。
const (
	skillIncludeContent  = "content"
	skillIncludeMetadata = "metadata"
)

// resolveSkillInclude 读取 `?include=`。`content` 内联 SKILL.md 与每个文件正文；
// `metadata` 只返回 path/size/hash。
//
// 什么都不带的请求在两个端点都继续拿 content。调用它们的是已安装软件——技能
// 编辑器的桌面版、`skill files list --output json` 脚本读 `content` 的旧 CLI——
// 无法回溯地要求它们发查询参数。在这里翻默认值会让一次服务端部署悄悄改变
// 未升级客户端的所见，客户端侧无从阻止。
//
// 因此瘦身是自愿且随调用方走的：CLI 自己发 `include=metadata`，修复 GH #7498
// 而无需服务端与所有客户端同步发版。待发 `include=content` 的客户端老化后可
// 再翻转默认值。
func resolveSkillInclude(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch strings.TrimSpace(r.URL.Query().Get("include")) {
	case "":
		return true, true
	case skillIncludeContent:
		return true, true
	case skillIncludeMetadata:
		return false, true
	default:
		writeError(w, http.StatusBadRequest, `invalid include: expected "content" or "metadata"`)
		return false, false
	}
}

// GetSkill 处理 GET /api/skills/{id}。默认返回技能+全部文件正文（兼容旧客户端）；
// ?include=metadata 时只回元数据（SKILL.md 大小/hash + 文件 path/size/hash）。
func (h *Handler) GetSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	includeContent, ok := resolveSkillInclude(w, r)
	if !ok {
		return
	}
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}

	if !includeContent {
		h.writeSkillMetadata(w, r, skill)
		return
	}

	files, err := h.Queries.ListSkillFiles(r.Context(), skill.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skill files")
		return
	}

	fileResps := make([]SkillFileResponse, len(files))
	for i, f := range files {
		fileResps[i] = skillFileToResponse(f)
	}

	writeJSON(w, http.StatusOK, SkillWithFilesResponse{
		SkillResponse: skillToResponse(skill),
		Files:         fileResps,
	})
}

// writeSkillMetadata 响应 GET /api/skills/{id}?include=metadata。技能行已加载
// （loadSkillForUser 需要它做租户检查），因此 SKILL.md 的大小与 hash 无需额外
// 成本；只有文件正文值得单独走一次元数据查询。
func (h *Handler) writeSkillMetadata(w http.ResponseWriter, r *http.Request, skill db.Skill) {
	files, err := h.Queries.ListSkillFileMetadata(r.Context(), skill.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skill files")
		return
	}

	fileResps := make([]SkillFileMetadataResponse, len(files))
	for i, f := range files {
		fileResps[i] = skillFileMetadataToResponse(f)
	}

	writeJSON(w, http.StatusOK, SkillWithFileMetadataResponse{
		SkillSummaryResponse: skillSummaryToResponse(
			skill.ID, skill.WorkspaceID, skill.Name, skill.Description,
			skill.Config, skill.CreatedBy, skill.CreatedAt, skill.UpdatedAt,
		),
		ContentSize: int64(len(skill.Content)),
		ContentHash: contentHash(skill.Content),
		Files:       fileResps,
	})
}

// CreateSkill 处理 POST /api/skills。请求者须为工作区成员；名字必填、文件路径
// 需安全。同名冲突（含并发插入竞态）返回 409。创建带文件的技能走
// createSkillWithFiles。
func (h *Handler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	creatorUUID := parseUUID(creatorID)

	var req CreateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	for _, f := range req.Files {
		if !validateFilePath(f.Path) {
			writeError(w, http.StatusBadRequest, "invalid file path: "+f.Path)
			return
		}
	}

	resp, err := h.createSkillWithFiles(r.Context(), skillCreateInput{
		WorkspaceID: workspaceUUID,
		CreatorID:   creatorUUID,
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
		Config:      req.Config,
		Files:       req.Files,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name already exists")
			return
		}
		slog.Warn("create skill failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create skill")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// canManageSkill 判断当前用户能否更新/删除技能。技能创建者或工作区 owner/admin
// 可管理任意技能。失败时已写好错误响应。
func (h *Handler) canManageSkill(w http.ResponseWriter, r *http.Request, skill db.Skill) bool {
	wsID := uuidToString(skill.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "skill not found", "owner", "admin", "member")
	if !ok {
		return false
	}
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	isSkillCreator := skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == requestUserID(r)
	if !isAdmin && !isSkillCreator {
		writeError(w, http.StatusForbidden, "only the skill creator can manage this skill")
		return false
	}
	return true
}

// canOverwriteSkillByLocalImport 判断 userID 是否可经「runtime 本地技能重新导入」
// 覆盖该技能。这里有意比 canManageSkill 更窄：只有原创建者可经重新导入覆盖。
// 想改非自己创建技能的工作区 owner/admin 请在应用内编辑。见 MUL-2701 / MUL-2800。
func canOverwriteSkillByLocalImport(userID string, skill db.Skill) bool {
	return skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == userID
}

// UpdateSkill 处理 PUT /api/skills/{id}。门控 canManageSkill；名字/描述/正文写入
// 前经 sanitizeNullBytes；提供 Files 时整体替换文件集（跳过保留路径 SKILL.md）。
// 行更新、文件替换与 skill:updated 事件同一事务。名字冲突返回 409。
func (h *Handler) UpdateSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}

	var req UpdateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	for _, f := range req.Files {
		if !validateFilePath(f.Path) {
			writeError(w, http.StatusBadRequest, "invalid file path: "+f.Path)
			return
		}
	}

	if err := h.prepareSkillFiles(r.Context(), skill.WorkspaceID, skill.ID, req.Files); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)

	// 1. Keep the pre-edit text so a prompt change made by a running task is
	//    recorded as a reviewable diff, not only as a new value.
	beforeContent, beforeDescription := skill.Content, skill.Description
	params := db.UpdateSkillParams{
		ID: parseUUID(id),
	}
	if req.Name != nil {
		params.Name = pgtype.Text{String: sanitizeNullBytes(*req.Name), Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: sanitizeNullBytes(*req.Description), Valid: true}
	}
	if req.Content != nil {
		params.Content = pgtype.Text{String: sanitizeNullBytes(*req.Content), Valid: true}
	}
	if req.Config != nil {
		config, _ := json.Marshal(req.Config)
		params.Config = config
	}

	updatedSkill, err := qtx.UpdateSkill(r.Context(), params)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name already exists")
			return
		}
		slog.Warn("update skill failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update skill")
		return
	}

	// 2. Record the text edits on the running task's result. Placed after the
	//    update so a rejected edit leaves no trace in the run log.
	h.recordRunTextChange(r.Context(), qtx, r, uuidToString(updatedSkill.WorkspaceID),
		runTextTarget{Kind: "skill", ID: uuidToString(updatedSkill.ID), Name: updatedSkill.Name},
		"content", beforeContent, updatedSkill.Content)
	h.recordRunTextChange(r.Context(), qtx, r, uuidToString(updatedSkill.WorkspaceID),
		runTextTarget{Kind: "skill", ID: uuidToString(updatedSkill.ID), Name: updatedSkill.Name},
		"description", beforeDescription, updatedSkill.Description)
	skill = updatedSkill
	// 若提供了 Files，整体替换所有文件。
	var fileResps []SkillFileResponse
	if req.Files != nil {
		if err := qtx.DeleteSkillFilesBySkill(r.Context(), skill.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete old skill files")
			return
		}
		fileResps = make([]SkillFileResponse, 0, len(req.Files))
		for _, f := range req.Files {
			// SKILL.md 保留给技能主正文（skill.Content）。
			if skillpkg.IsReservedContentPath(f.Path) {
				continue
			}
			sf, err := upsertPreparedSkillFile(r.Context(), qtx, skill.ID, f)
			if err != nil {
				slog.Warn("upsert skill file failed", append(logger.RequestAttrs(r), "error", err)...)
				writeError(w, http.StatusInternalServerError, "failed to upsert skill file")
				return
			}
			fileResps = append(fileResps, skillFileToResponse(sf))
		}
	} else {
		files, _ := qtx.ListSkillFiles(r.Context(), skill.ID)
		fileResps = make([]SkillFileResponse, len(files))
		for i, f := range files {
			fileResps[i] = skillFileToResponse(f)
		}
	}

	resp := SkillWithFilesResponse{
		SkillResponse: skillToResponse(skill),
		Files:         fileResps,
	}
	wsID := h.resolveWorkspaceID(r)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventSkillUpdated,
		IdempotencyKey: "skill:updated:" + uuidToString(skill.ID) + ":" + skill.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "skill:" + uuidToString(skill.ID), WorkspaceID: wsID, ActorType: actorType, ActorID: actorID,
		Payload: map[string]any{"skill": resp},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record skill event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, resp)
}

// DeleteSkill 处理 DELETE /api/skills/{id}。门控 canManageSkill。事务内先清标签
// 指派再删技能并记录 skill:deleted 事件；成功返回 204。
func (h *Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DeleteSkillLabelAssignmentsBySkill(r.Context(), skill.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove skill label assignments")
		return
	}
	if err := qtx.DeleteSkill(r.Context(), db.DeleteSkillParams{
		ID:          skill.ID,
		WorkspaceID: skill.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete skill")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(skill.WorkspaceID))
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventSkillDeleted,
		IdempotencyKey: "skill:deleted:" + uuidToString(skill.ID),
		StreamKey:      "skill:" + uuidToString(skill.ID), WorkspaceID: uuidToString(skill.WorkspaceID), ActorType: actorType, ActorID: actorID,
		Payload: map[string]any{"skill_id": uuidToString(skill.ID)},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record skill event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit skill deletion")
		return
	}
	h.publishEvent(event)
	w.WriteHeader(http.StatusNoContent)
}

// --- 技能导入 ---

// ImportSkillRequest 是 URL 导入的请求体：url + on_conflict 冲突策略。
type ImportSkillRequest struct {
	URL        string `json:"url"`
	OnConflict string `json:"on_conflict,omitempty"`
}

const (
	importOnConflictFail      = "fail"      // 撞名直接报冲突
	importOnConflictOverwrite = "overwrite" // 撞名则覆盖（仅原创建者）
	importOnConflictRename    = "rename"    // 撞名则加 -N 后缀改名导入
	importOnConflictSkip      = "skip"      // 撞名则跳过并报告
)

const maxImportRenameAttempts = 50

func validImportOnConflict(strategy string) bool {
	switch strategy {
	case "", importOnConflictFail, importOnConflictOverwrite, importOnConflictRename, importOnConflictSkip:
		return true
	}
	return false
}

// 每次导入的包级限额，镜像本地 runtime 导入器，URL 导入不能偷运其余栈会拒绝的
// 负载。fetchRawFile 执行单文件上限；importedSkill.addFile 执行包级上限。
const (
	maxImportFileSize  = skillpkg.MaxFileBytes   // 单文件 1 MiB
	maxImportTotalSize = skillpkg.MaxBundleBytes // 支持文件合计 64 MiB
	maxImportFileCount = skillpkg.MaxFiles       // 支持文件数量上限
)

// importedSkill 保存从外部源抽取出来的数据。
type importedSkill struct {
	name        string
	description string
	content     string // SKILL.md 正文
	files       []importedFile
	bundleSize  int            // 用于限额检查的文件内容字节累计
	origin      map[string]any // 写入 skill.config.origin，让 UI 能展示来源
}

type importedFile struct {
	mode    int32
	path    string
	content string
}

// errImportCapExceeded 标记由单文件/包级限额导致的错误。这类错误必须中止导入
// ——静默丢文件会产生一个看似完整实则残缺的技能。
var errImportCapExceeded = errors.New("import cap exceeded")

// errImportSourceUnavailable 标记读取上游源的瞬时失败（如 GitHub tree API
// 限流）。导入无法安全继续但应重试，因此映射为重试型 HTTP 状态而非永久错误。
var errImportSourceUnavailable = errors.New("import source temporarily unavailable")

// isCapError 报告 err 是否为（或包装了）errImportCapExceeded。
func isCapError(err error) bool {
	return errors.Is(err, errImportCapExceeded)
}

// addFile 追加一个支持文件，同时执行包级限额。文件数或总字节预算将被超出时
// 返回错误，让调用方中止导入而不是静默截断。
//
// 内容先保留原始字节，入库时由 prepareSkillFiles 区分文本与对象。
func (s *importedSkill) addFile(path, content string) error {
	if !validateFilePath(path) {
		return fmt.Errorf("invalid file path: %s", path)
	}
	for _, f := range s.files {
		if f.path == path {
			return fmt.Errorf("duplicate file path: %s", path)
		}
	}
	if len(content) > maxImportFileSize {
		return fmt.Errorf("%w: file %s too large", errImportCapExceeded, path)
	}
	if len(s.files) >= maxImportFileCount {
		return fmt.Errorf("%w: import bundle exceeds %d file limit", errImportCapExceeded, maxImportFileCount)
	}
	if s.bundleSize+len(content) > maxImportTotalSize {
		return fmt.Errorf("%w: import bundle exceeds %d byte limit", errImportCapExceeded, maxImportTotalSize)
	}
	s.bundleSize += len(content)
	s.files = append(s.files, importedFile{path: path, content: content})
	return nil
}

// --- ClawHub 类型 ---

var clawHubAPIBase = "https://clawhub.ai/api/v1"

const clawHubSearchStatsLimit = 10

type clawhubSearchResponse struct {
	Results []clawhubSearchResult `json:"results"`
}

type clawhubSearchResult struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary"`
	OwnerHandle string `json:"ownerHandle"`
}

type clawhubSkillStats struct {
	InstallsAllTime int64 `json:"installsAllTime"`
	InstallsCurrent int64 `json:"installsCurrent"`
}

type clawhubGetSkillResponse struct {
	Skill         clawhubSkill          `json:"skill"`
	LatestVersion *clawhubLatestVersion `json:"latestVersion"`
}

type clawhubSkill struct {
	Slug        string            `json:"slug"`
	DisplayName string            `json:"displayName"`
	Summary     string            `json:"summary"`
	Tags        map[string]string `json:"tags"`
	Stats       clawhubSkillStats `json:"stats"`
}

type clawhubLatestVersion struct {
	Version string `json:"version"`
}

type clawhubVersionDetailResponse struct {
	Version clawhubVersionDetail `json:"version"`
}

type clawhubVersionDetail struct {
	Version string             `json:"version"`
	Files   []clawhubFileEntry `json:"files"`
}

type clawhubFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// --- GitHub 类型（用于 skills.sh）---

type githubContentEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"` // "file" 或 "dir"
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
}

type githubRepoInfo struct {
	DefaultBranch string `json:"default_branch"`
}

type githubTreeResponse struct {
	Tree      []githubTreeEntry `json:"tree"`
	Truncated bool              `json:"truncated"`
}

type githubTreeEntry struct {
	Mode string `json:"mode"`
	Path string `json:"path"`
	Type string `json:"type"` // "blob" 或 "tree"
	Size int64  `json:"size"` // blob 字节大小（tree 条目缺席/为 0）
}

// fetchGitHubDefaultBranch 返回 GitHub 仓库的默认分支。API 调用失败时退回 "main"。
func fetchGitHubDefaultBranch(ctx context.Context, httpClient *http.Client, owner, repo string) string {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo))
	resp, err := doGitHubAPIGet(ctx, httpClient, apiURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return "main"
	}
	defer resp.Body.Close()

	var info githubRepoInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.DefaultBranch == "" {
		return "main"
	}
	return info.DefaultBranch
}

// --- URL 来源识别 ---

// importSource 标识 URL 指向的来源。
type importSource int

const (
	sourceClawHub importSource = iota
	sourceSkillsSh
	sourceGitHub
)

// detectImportSource 从 URL 判断来源。返回来源与（带 scheme 的）规范化 URL。
func detectImportSource(raw string) (importSource, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", fmt.Errorf("empty URL")
	}

	normalized := raw
	if !strings.HasPrefix(normalized, "http://") && !strings.HasPrefix(normalized, "https://") {
		normalized = "https://" + normalized
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return 0, "", fmt.Errorf("invalid URL: %w", err)
	}

	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "skills.sh" || host == "www.skills.sh":
		return sourceSkillsSh, normalized, nil
	case host == "clawhub.ai" || host == "www.clawhub.ai":
		return sourceClawHub, normalized, nil
	case host == "github.com" || host == "www.github.com":
		return sourceGitHub, normalized, nil
	default:
		// 无 host（裸 slug）时默认按 clawhub 处理
		if !strings.Contains(raw, "/") || !strings.Contains(raw, ".") {
			return sourceClawHub, raw, nil
		}
		return 0, "", fmt.Errorf("unsupported source: %s (supported: clawhub.ai, skills.sh, github.com)", host)
	}
}

// --- ClawHub 导入 ---

// parseClawHubSlug 从 clawhub.ai URL 抽取技能 slug。
func parseClawHubSlug(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	// /{owner}/{slug}——取最后一段作为 slug
	if len(parts) == 2 {
		return parts[1], nil
	}
	if len(parts) == 1 && parts[0] != "" {
		return parts[0], nil
	}
	// 裸 slug（无路径）
	if raw == parsed.Host || parsed.Path == "" || parsed.Path == "/" {
		return "", fmt.Errorf("missing skill slug in URL")
	}
	return "", fmt.Errorf("could not extract skill slug from URL: %s", raw)
}

func searchClawHubSkills(httpClient *http.Client, query string) ([]SkillSearchCandidateResponse, error) {
	searchURL := clawHubAPIBase + "/search?q=" + url.QueryEscape(query)
	resp, err := httpClient.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to reach ClawHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClawHub search returned status %d", resp.StatusCode)
	}

	var searchResp clawhubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse ClawHub search response")
	}

	candidates := make([]SkillSearchCandidateResponse, 0, len(searchResp.Results))
	for i, result := range searchResp.Results {
		if result.Slug == "" {
			continue
		}
		candidate := SkillSearchCandidateResponse{
			Name:        result.DisplayName,
			URL:         buildClawHubSkillURL(result.OwnerHandle, result.Slug),
			Source:      "clawhub.ai",
			Description: result.Summary,
		}
		if candidate.Name == "" {
			candidate.Name = result.Slug
		}
		if i < clawHubSearchStatsLimit {
			if count, ok := fetchClawHubInstallCount(httpClient, result.Slug); ok {
				candidate.InstallCount = &count
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func buildClawHubSkillURL(ownerHandle, slug string) string {
	if ownerHandle == "" {
		return "https://clawhub.ai/" + url.PathEscape(slug)
	}
	return "https://clawhub.ai/" + url.PathEscape(ownerHandle) + "/" + url.PathEscape(slug)
}

func fetchClawHubInstallCount(httpClient *http.Client, slug string) (int64, bool) {
	detailURL := clawHubAPIBase + "/skills/" + url.PathEscape(slug)
	resp, err := httpClient.Get(detailURL)
	if err != nil {
		slog.Warn("clawhub search: failed to fetch skill details", "slug", slug, "error", err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("clawhub search: skill details returned non-200", "slug", slug, "status", resp.StatusCode)
		return 0, false
	}
	var detail clawhubGetSkillResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		slog.Warn("clawhub search: failed to parse skill details", "slug", slug, "error", err)
		return 0, false
	}
	if detail.Skill.Stats.InstallsAllTime > 0 {
		return detail.Skill.Stats.InstallsAllTime, true
	}
	return detail.Skill.Stats.InstallsCurrent, true
}

func fetchFromClawHub(ctx context.Context, httpClient *http.Client, rawURL string) (*importedSkill, error) {
	slug, err := parseClawHubSlug(rawURL)
	if err != nil {
		return nil, err
	}

	apiBase := clawHubAPIBase

	// 1. 拉取技能元数据
	skillReq, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/skills/"+url.PathEscape(slug), nil)
	if err != nil {
		return nil, err
	}
	skillResp, err := httpClient.Do(skillReq)
	if err != nil {
		return nil, fmt.Errorf("failed to reach ClawHub: %w", err)
	}
	defer skillResp.Body.Close()

	if skillResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("skill not found on ClawHub: %s", slug)
	}
	if skillResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClawHub returned status %d", skillResp.StatusCode)
	}

	var chResp clawhubGetSkillResponse
	if err := json.NewDecoder(skillResp.Body).Decode(&chResp); err != nil {
		return nil, fmt.Errorf("failed to parse ClawHub response")
	}
	chSkill := chResp.Skill

	// 2. 确定最新版本并拉取文件清单
	latestVersion := ""
	if v, ok := chSkill.Tags["latest"]; ok {
		latestVersion = v
	} else if chResp.LatestVersion != nil {
		latestVersion = chResp.LatestVersion.Version
	}

	var filePaths []string
	if latestVersion != "" {
		vURL := fmt.Sprintf("%s/skills/%s/versions/%s", apiBase, url.PathEscape(slug), url.PathEscape(latestVersion))
		vReq, verr := http.NewRequestWithContext(ctx, http.MethodGet, vURL, nil)
		if verr != nil {
			return nil, verr
		}
		vResp, err := httpClient.Do(vReq)
		if err == nil {
			defer vResp.Body.Close()
			if vResp.StatusCode == http.StatusOK {
				var vDetail clawhubVersionDetailResponse
				if err := json.NewDecoder(vResp.Body).Decode(&vDetail); err == nil {
					for _, f := range vDetail.Version.Files {
						filePaths = append(filePaths, f.Path)
					}
				}
			}
		}
	}

	// 3. 逐个下载文件
	result := &importedSkill{
		name:        chSkill.DisplayName,
		description: chSkill.Summary,
		origin: map[string]any{
			"type":       "clawhub",
			"source_url": rawURL,
			"slug":       slug,
		},
	}
	if result.name == "" {
		result.name = slug
	}

	for _, fp := range filePaths {
		fileURL := fmt.Sprintf("%s/skills/%s/file?path=%s", apiBase, url.PathEscape(slug), url.QueryEscape(fp))
		if latestVersion != "" {
			fileURL += "&version=" + url.QueryEscape(latestVersion)
		}
		body, err := fetchRawFile(ctx, httpClient, fileURL)
		if err != nil {
			// 超限必须中止：静默丢文件会产出看似完整实则残缺的包。SKILL.md 是
			// 承重的，任何关于它的失败同样致命。
			if isCapError(err) || fp == "SKILL.md" {
				return nil, fmt.Errorf("clawhub import: %s: %w", fp, err)
			}
			// 取消的 context（整体截止/客户端断开）同样致命：跳过剩余每个文件
			// 会把半填的包当作成功持久化。
			if ctx.Err() != nil {
				return nil, fmt.Errorf("clawhub import: fetch aborted at %s: %w", fp, ctx.Err())
			}
			return nil, fmt.Errorf("clawhub import: %s: %w", fp, err)
		}
		if fp == "SKILL.md" {
			result.content = string(body)
			continue
		}
		if err := result.addFile(fp, string(body)); err != nil {
			return nil, err
		}
	}

	if result.content == "" {
		return nil, fmt.Errorf("clawhub import: SKILL.md is empty or missing for %s", slug)
	}

	return result, nil
}

// --- skills.sh 导入 ---

// parseSkillsShParts 从 skills.sh URL 抽取 owner、repo、skill-name。
// URL 格式：https://skills.sh/{owner}/{repo}/{skill-name}
func parseSkillsShParts(raw string) (owner, repo, skillName string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("expected URL format: skills.sh/{owner}/{repo}/{skill-name}, got: %s", parsed.Path)
	}
	return parts[0], parts[1], parts[2], nil
}

func fetchFromSkillsSh(ctx context.Context, httpClient *http.Client, rawURL string) (*importedSkill, error) {
	owner, repo, skillName, err := parseSkillsShParts(rawURL)
	if err != nil {
		return nil, err
	}

	// skills.sh URL 映射到 GitHub 仓库。技能目录解析与支持文件枚举都由一次递归
	// tree 拉取驱动。它把过去「每个目录一次 contents API 调用」——大 monorepo
	// （如 api-gateway-skill）会产生数百次串行请求、把请求推到反代网关超时
	// （504）之外——坍缩为一次调用，还可在下载任何文件前先用 tree 元数据做
	// 限额检查。
	defaultBranch := fetchGitHubDefaultBranch(ctx, httpClient, owner, repo)
	rawPrefix := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(defaultBranch))

	tree, truncated, treeErr := fetchGitHubTree(ctx, httpClient, owner, repo, defaultBranch)
	if treeErr != nil {
		// tree 拉取失败（通常是 GitHub API 限流，同样会击穿 contents API）。
		// 没有 tree 就无法安全解析哪个目录是技能：裸探测+根 SKILL.md 兜底会在
		// 仓库名与 slug 撞名时（api-gateway-skill 案例）重新选中仓库根，而同一
		// 限流会让爬取为空——最终把「错误的 SKILL.md + 零支持文件」当成成功导入
		// 持久化。改为返回可重试错误，不保存任何错误内容。
		slog.Warn("skills.sh import: repository tree fetch failed",
			"owner", owner, "repo", repo, "error", treeErr)
		return nil, fmt.Errorf("%w: could not read the %s/%s repository tree (usually GitHub API rate limiting — set GITHUB_TOKEN on the server or retry): %v",
			errImportSourceUnavailable, owner, repo, treeErr)
	}

	skillDir, skillMdBody, err := resolveSkillDirFromTree(ctx, httpClient, owner, repo, defaultBranch, rawPrefix, skillName, tree, truncated)
	if err != nil {
		return nil, err
	}

	result := newSkillsShImportedSkill(skillMdBody, skillName, rawURL, owner, repo)

	if truncated {
		// tree 不完整，无法驱动枚举；退回到限定在已解析技能目录里的旧式逐目录
		// 爬取。
		if err := addSupportingFilesViaCrawl(ctx, httpClient, result, owner, repo, defaultBranch, skillDir); err != nil {
			return nil, err
		}
		return result, nil
	}

	if err := addSupportingFilesFromTree(ctx, httpClient, result, tree, rawPrefix, skillDir); err != nil {
		return nil, err
	}
	return result, nil
}

// newSkillsShImportedSkill 从拉取到的 SKILL.md 正文构建 importedSkill 外壳
// （名字、描述、内容、来源信息）。
func newSkillsShImportedSkill(skillMdBody []byte, skillName, rawURL, owner, repo string) *importedSkill {
	name, description := skillpkg.ParseSkillFrontmatter(string(skillMdBody))
	if name == "" {
		name = skillName
	}
	return &importedSkill{
		name:        name,
		description: description,
		content:     string(skillMdBody),
		origin: map[string]any{
			"type":       "skills_sh",
			"source_url": rawURL,
			"owner":      owner,
			"repo":       repo,
			"skill":      skillName,
		},
	}
}

// addSupportingFilesViaCrawl 是旧式逐目录枚举路径，仅作为递归 tree 被截断时的
// 兜底（github.com 导入器也在用）。tree 拉取失败不再走这里——那会返回可重试
// 错误。它经 contents API 列出 skillDir、递归子目录并逐个下载。对真实的列举
// 失败保持宽容（只带 SKILL.md 返回，与 GitHub API 限流下的旧行为一致），但把
// context 被取消视为致命，避免爬取中途的截止/断线把残缺包当作成功持久化。
func addSupportingFilesViaCrawl(ctx context.Context, httpClient *http.Client, result *importedSkill, owner, repo, ref, skillDir string) error {
	apiURL := buildGitHubContentsURL(owner, repo, skillDir, ref)
	dirResp, err := doGitHubAPIGet(ctx, httpClient, apiURL)
	if err != nil || dirResp.StatusCode != http.StatusOK {
		if dirResp != nil {
			dirResp.Body.Close()
		}
		// context 被取消（整体截止/客户端断开）必须中止，不能吞成「无支持文件」，
		// 否则会把残缺包当成功持久化。
		if ctx.Err() != nil {
			return fmt.Errorf("github import: directory listing aborted: %w", ctx.Err())
		}
		return fmt.Errorf("%w: directory listing failed", errImportSourceUnavailable)
	}
	defer dirResp.Body.Close()

	var entries []githubContentEntry
	if err := json.NewDecoder(dirResp.Body).Decode(&entries); err != nil {
		// context 被取消会以解码错误的形式出现（截止或断线发生在响应体读取期间）。
		// 视为致命而非吞掉——否则导入被保存为「SKILL.md 有效但零支持文件」。
		if ctx.Err() != nil {
			return fmt.Errorf("github import: directory listing read aborted: %w", ctx.Err())
		}
		return fmt.Errorf("decode skill directory: %w", err)
	}

	var allFiles []githubContentEntry
	if err := collectGitHubFiles(ctx, httpClient, entries, &allFiles, apiURL); err != nil {
		return err
	}
	// collectGitHubFiles 对子目录列举失败保持宽容；若爬取途中 context 被取消，
	// 收集到的集合是不完整的，因此这里中止。
	if ctx.Err() != nil {
		return fmt.Errorf("github import: directory crawl aborted: %w", ctx.Err())
	}

	basePath := ""
	if skillDir != "" {
		basePath = skillDir + "/"
	}
	for _, entry := range allFiles {
		if skillpkg.IsReservedContentPath(strings.TrimPrefix(entry.Path, basePath)) {
			continue
		}
		if entry.DownloadURL == "" {
			return fmt.Errorf("missing download URL: %s", entry.Path)
		}
		body, err := fetchRawFile(ctx, httpClient, entry.DownloadURL)
		if err != nil {
			if isCapError(err) {
				return fmt.Errorf("github import: %s: %w", entry.Path, err)
			}
			if ctx.Err() != nil {
				return fmt.Errorf("github import: fetch aborted at %s: %w", entry.Path, ctx.Err())
			}
			return fmt.Errorf("github import: %s: %w", entry.Path, err)
		}
		relPath := strings.TrimPrefix(entry.Path, basePath)
		if err := result.addFile(relPath, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// fetchGitHubTree 用一次 API 调用拉取某个 ref 的完整递归 git tree。每个 blob
// 条目都带路径与字节大小，导入器既可据此解析技能目录，也能在无需逐目录 contents
// 爬取（过去大 monorepo 下每目录一次 API 调用、数百次串行请求，正是把 skills.sh
// 导入推过反代网关超时 504 的原因）的情况下执行导入限额。
func fetchGitHubTree(ctx context.Context, httpClient *http.Client, owner, repo, ref string) (entries []githubTreeEntry, truncated bool, err error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	resp, err := doGitHubAPIGet(ctx, httpClient, apiURL)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var tree githubTreeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, false, err
	}
	return tree.Tree, tree.Truncated, nil
}

// resolveSkillDirFromTree 从已拉取的仓库 tree 解析技能目录与它的 SKILL.md 正文。
// 优先选择经 frontmatter 验证的最具体匹配（目录名匹配技能名的 SKILL.md），仅当
// 更深的匹配都不存在时才考虑仓库根——这样目录名与技能名恰巧撞名的根 SKILL.md
// 不会再捕获整个仓库。frontmatter 匹配无果时按路径接受惯用位置（保留 tree 导入
// 器之前的宽容语义）。tree 被截断时退回有界的逐前缀列举。
func resolveSkillDirFromTree(ctx context.Context, httpClient *http.Client, owner, repo, defaultBranch, rawPrefix, skillName string, tree []githubTreeEntry, truncated bool) (string, []byte, error) {
	skillPaths := extractSkillMdPaths(tree)
	preferred, remaining := partitionSkillMdPaths(skillName, skillPaths)
	if dir, body, ok := findMatchingSkillDirByFrontmatter(ctx, httpClient, rawPrefix, skillName, preferred); ok {
		return dir, body, nil
	}
	if !truncated {
		if dir, body, ok := findMatchingSkillDirByFrontmatter(ctx, httpClient, rawPrefix, skillName, remaining); ok {
			return dir, body, nil
		}
		// frontmatter 匹配无果。退回按路径接受，让位于惯用位置（skills/<name>/
		// SKILL.md 等）的技能即使其 frontmatter 名与 URL slug 非逐字节一致也能导入
		// ——例如 slug `foo` 配 `name: Foo`。这恢复了 tree 导入器之前的语义，并与
		// 受限兜底已接受的规则一致，只用已拿到的 tree（至多多一次 raw 拉取，绝无
		// 目录爬取）。放在 frontmatter 之后，裸仓库根在此永不入围——撞名修复保持
		// 完整。
		if dir, body, ok := acceptConventionalSkillDir(ctx, httpClient, rawPrefix, skillName, skillPaths, true); ok {
			return dir, body, nil
		}
		return "", nil, skillMdNotFoundError(owner, repo, skillName)
	}

	slog.Warn("github import: repository tree listing truncated", "owner", owner, "repo", repo, "branch", defaultBranch)
	if dir, body, ok := findSkillDirFromConventionalPrefixes(ctx, httpClient, owner, repo, defaultBranch, rawPrefix, skillName); ok {
		return dir, body, nil
	}
	// 与未截断分支相同的按路径接受，带显示名 frontmatter 的惯用技能无论 GitHub
	// 是否截断 tree 都同样导入。此处 tree 无法证明路径不存在，因此助手直接探测
	// 每个候选。
	if dir, body, ok := acceptConventionalSkillDir(ctx, httpClient, rawPrefix, skillName, skillPaths, false); ok {
		return dir, body, nil
	}
	return "", nil, fmt.Errorf("repository %s/%s tree is too large to scan exhaustively for skill %s", owner, repo, skillName)
}

// conventionalSkillMdPaths 返回导入器对给定技能 slug 按路径接受（不查 frontmatter
// 名）的 SKILL.md 位置。顺序有意义：镜像 tree 导入器之前的探测顺序。
func conventionalSkillMdPaths(skillName string) []string {
	return []string{
		"skills/" + skillName + "/SKILL.md",
		".claude/skills/" + skillName + "/SKILL.md",
		"plugin/skills/" + skillName + "/SKILL.md",
		skillName + "/SKILL.md",
	}
}

// acceptConventionalSkillDir 按惯用路径（skills/<name>/SKILL.md 等）接受技能，
// 不要求 frontmatter 名匹配，恢复 tree 导入器之前基于路径的宽容接受。它绝不
// 接受裸仓库根——与 slug 撞名的根 SKILL.md 只能经 frontmatter 阶段被选中——
// 撞名修复保持完整。
//
// treeComplete 为 true 时，未截断的 tree 能证明哪些惯用路径存在，缺席候选直接
// 跳过、不产生网络调用。tree 被截断时无法证明缺席，因此直接探测每个候选
// （缺失候选 404 即被跳过）。
func acceptConventionalSkillDir(ctx context.Context, httpClient *http.Client, rawPrefix, skillName string, treeSkillPaths []string, treeComplete bool) (string, []byte, bool) {
	present := make(map[string]struct{}, len(treeSkillPaths))
	for _, p := range treeSkillPaths {
		present[p] = struct{}{}
	}
	for _, candidate := range conventionalSkillMdPaths(skillName) {
		if treeComplete {
			if _, ok := present[candidate]; !ok {
				continue // 完整 tree 证明该路径不存在
			}
		}
		body, err := fetchRawFile(ctx, httpClient, buildRawGitHubURL(rawPrefix, candidate))
		if err != nil {
			// 完整 tree 下路径已被证明存在，拉取失败出乎意料、值得留痕；截断
			// tree 下这只是普通探测未命中。
			if treeComplete {
				slog.Warn("skills.sh import: conventional SKILL.md fetch failed", "path", candidate, "error", err)
			}
			continue
		}
		return skillDirFromSkillFilePath(candidate), body, true
	}
	return "", nil, false
}

// treeDownloadConcurrency 限制基于 tree 收集期间并行下载的支持文件数。小到单次
// 导入不会一次向 GitHub 打开数百个 socket，大到对完整（256 文件）包仍能把下载
// 阶段压在整个导入截止之前。
const treeDownloadConcurrency = 8

// addSupportingFilesFromTree 从单次递归 git tree 枚举 skillDir 下的支持文件：
// 先纯用 tree 元数据算术式执行单文件/数量/总字节上限（超限技能快速失败并给出
// 清晰错误而非超时），再并发下载存活文件并按稳定路径顺序追加。取代旧式逐目录
// contents 爬取（collectGitHubFiles）。
func addSupportingFilesFromTree(ctx context.Context, httpClient *http.Client, result *importedSkill, tree []githubTreeEntry, rawPrefix, skillDir string) error {
	basePath := ""
	if skillDir != "" {
		basePath = skillDir + "/"
	}

	// 选出 skillDir 下合格的支持文件 blob，镜像下载循环/addFile 本会应用的过滤：
	// 跳过技能自身 SKILL.md、LICENSE 文件与二进制资源（addFile 反正会丢）。过滤
	// 放在这里能让下面的算术限额检查与实际导入内容一致。
	type treeFile struct {
		mode     int32
		repoPath string
		relPath  string
		size     int64
	}
	var eligible []treeFile
	for _, entry := range tree {
		if entry.Type != "blob" {
			continue
		}
		if basePath != "" && !strings.HasPrefix(entry.Path, basePath) {
			continue
		}
		relPath := strings.TrimPrefix(entry.Path, basePath)
		if relPath == "" {
			continue
		}
		if skillpkg.IsReservedContentPath(relPath) || isIgnoredArchiveEntry(relPath) {
			continue
		}
		mode := int32(0644)
		if entry.Mode == "100755" {
			mode = 0755
		}
		if entry.Mode == "120000" {
			return fmt.Errorf("unsupported symbolic link: %s", relPath)
		}
		eligible = append(eligible, treeFile{repoPath: entry.Path, relPath: relPath, size: entry.size(), mode: mode})
	}

	// 稳定顺序，导入结果与下载时序无关，可复现。
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].relPath < eligible[j].relPath })

	// 基于 tree 元数据的算术限额预检——拒绝超限包无需任何下载。
	if len(eligible) > maxImportFileCount {
		return fmt.Errorf("%w: import bundle would contain %d files, exceeding the %d file limit", errImportCapExceeded, len(eligible), maxImportFileCount)
	}
	var totalSize int64
	for _, f := range eligible {
		if f.size > maxImportFileSize {
			return fmt.Errorf("%w: %s is %d bytes, exceeding the %d byte per-file limit", errImportCapExceeded, f.relPath, f.size, maxImportFileSize)
		}
		totalSize += f.size
	}
	if totalSize > maxImportTotalSize {
		return fmt.Errorf("%w: import bundle is %d bytes, exceeding the %d byte limit", errImportCapExceeded, totalSize, maxImportTotalSize)
	}

	// 并发下载，然后按预排序顺序追加。
	contents := make([]string, len(eligible))
	fetched := make([]bool, len(eligible))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(treeDownloadConcurrency)
	for i, f := range eligible {
		i, f := i, f
		g.Go(func() error {
			body, err := fetchRawFile(gctx, httpClient, buildRawGitHubURL(rawPrefix, f.repoPath))
			if err != nil {
				if isCapError(err) {
					return fmt.Errorf("github import: %s: %w", f.repoPath, err)
				}
				// context 被取消（整体导入截止超时，或客户端断开）必须中止而不能吞
				// 成单文件跳过：否则每个剩余下载同样失败、g.Wait 返回 nil，用户拿到
				// 一个看似完整实则静默缺文件的包。让它冒泡，importFetchErrorResponse
				// 会把截止映射成可读的 504。
				if gctx.Err() != nil {
					return fmt.Errorf("github import: fetch aborted at %s: %w", f.repoPath, gctx.Err())
				}
				// 其余情况匹配旧循环的宽容：单个失败的支持文件记日志跳过，不致命。
				return fmt.Errorf("github import: %s: %w", f.repoPath, err)
			}
			contents[i] = string(body)
			fetched[i] = true
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	for i, f := range eligible {
		if !fetched[i] {
			continue
		}
		if err := result.addFile(f.relPath, contents[i]); err != nil {
			return err
		}
		result.files[len(result.files)-1].mode = f.mode
	}
	return nil
}

// size 返回 blob 字节大小，防御畸形 tree 响应里的负值。
func (e githubTreeEntry) size() int64 {
	if e.Size < 0 {
		return 0
	}
	return e.Size
}

// collectGitHubFiles 从 GitHub 目录列举里递归收集文件条目。
func collectGitHubFiles(ctx context.Context, httpClient *http.Client, entries []githubContentEntry, out *[]githubContentEntry, parentURL string) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if isIgnoredArchiveEntry(entry.Name) {
			continue
		}
		switch entry.Type {
		case "file":
			*out = append(*out, entry)
		case "dir":
			subURL := entry.URL
			if subURL == "" {
				parsed, err := url.Parse(parentURL)
				if err != nil {
					return err
				}
				parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + entry.Name
				subURL = parsed.String()
			}
			response, err := doGitHubAPIGet(ctx, httpClient, subURL)
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				return fmt.Errorf("%w: directory %s HTTP %d", errImportSourceUnavailable, entry.Path, response.StatusCode)
			}
			var children []githubContentEntry
			err = json.NewDecoder(response.Body).Decode(&children)
			response.Body.Close()
			if err != nil {
				return err
			}
			if err = collectGitHubFiles(ctx, httpClient, children, out, subURL); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported skill entry: %s (%s)", entry.Path, entry.Type)
		}
	}
	return nil
}

func findSkillDirFromConventionalPrefixes(ctx context.Context, httpClient *http.Client, owner, repo, defaultBranch, rawPrefix, skillName string) (string, []byte, bool) {
	prefixes := []string{"skills", ".claude/skills", "plugin/skills"}
	var skillPaths []string
	for _, prefix := range prefixes {
		paths, err := listGitHubSkillMdPaths(ctx, httpClient, owner, repo, prefix, defaultBranch)
		if err != nil {
			slog.Warn("github import: failed to list conventional skill prefix", "prefix", prefix, "error", err)
			continue
		}
		skillPaths = append(skillPaths, paths...)
	}

	preferred, remaining := partitionSkillMdPaths(skillName, skillPaths)
	if dir, body, ok := findMatchingSkillDirByFrontmatter(ctx, httpClient, rawPrefix, skillName, preferred); ok {
		return dir, body, true
	}
	return findMatchingSkillDirByFrontmatter(ctx, httpClient, rawPrefix, skillName, remaining)
}

func listGitHubSkillMdPaths(ctx context.Context, httpClient *http.Client, owner, repo, repoPath, ref string) ([]string, error) {
	apiURL := buildGitHubContentsURL(owner, repo, repoPath, ref)
	resp, err := doGitHubAPIGet(ctx, httpClient, apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var entries []githubContentEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}

	var paths []string
	collectGitHubSkillMdPaths(ctx, httpClient, entries, &paths, apiURL)
	return paths, nil
}

func collectGitHubSkillMdPaths(ctx context.Context, httpClient *http.Client, entries []githubContentEntry, out *[]string, parentURL string) {
	for _, entry := range entries {
		lower := strings.ToLower(entry.Name)
		if entry.Type == "file" {
			if lower == "skill.md" {
				*out = append(*out, entry.Path)
			}
			continue
		}
		if entry.Type != "dir" {
			continue
		}

		subURL := entry.URL
		if subURL == "" {
			parsed, err := url.Parse(parentURL)
			if err != nil {
				slog.Warn("github import: invalid parent directory url", "url", parentURL, "error", err)
				continue
			}
			parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + entry.Name
			subURL = parsed.String()
		}

		subResp, err := doGitHubAPIGet(ctx, httpClient, subURL)
		if err != nil || subResp.StatusCode != http.StatusOK {
			attrs := []any{"url", subURL}
			if subResp != nil {
				attrs = append(attrs, "status", subResp.StatusCode)
				subResp.Body.Close()
			}
			if err != nil {
				attrs = append(attrs, "error", err)
			}
			slog.Warn("github import: failed to list skill metadata subdirectory", attrs...)
			continue
		}

		var subEntries []githubContentEntry
		if err := json.NewDecoder(subResp.Body).Decode(&subEntries); err != nil {
			subResp.Body.Close()
			slog.Warn("github import: failed to decode skill metadata subdirectory", "url", subURL, "error", err)
			continue
		}
		subResp.Body.Close()
		collectGitHubSkillMdPaths(ctx, httpClient, subEntries, out, subURL)
	}
}

func extractSkillMdPaths(entries []githubTreeEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "blob" || (!strings.HasSuffix(entry.Path, "/SKILL.md") && entry.Path != "SKILL.md") {
			continue
		}
		paths = append(paths, entry.Path)
	}
	return paths
}

func partitionSkillMdPaths(skillName string, skillPaths []string) (preferred []string, remaining []string) {
	for _, skillPath := range skillPaths {
		if isLikelySkillPathMatch(skillName, skillPath) {
			preferred = append(preferred, skillPath)
			continue
		}
		remaining = append(remaining, skillPath)
	}
	return preferred, remaining
}

func findMatchingSkillDirByFrontmatter(ctx context.Context, httpClient *http.Client, rawPrefix, skillName string, skillPaths []string) (string, []byte, bool) {
	for _, skillPath := range skillPaths {
		body, err := fetchRawFile(ctx, httpClient, buildRawGitHubURL(rawPrefix, skillPath))
		if err != nil {
			slog.Warn("github import: fallback SKILL.md fetch failed", "path", skillPath, "error", err)
			continue
		}
		name, _ := skillpkg.ParseSkillFrontmatter(string(body))
		if name == skillName {
			return skillDirFromSkillFilePath(skillPath), body, true
		}
	}
	return "", nil, false
}

func isLikelySkillPathMatch(skillName, skillPath string) bool {
	dir := strings.ToLower(skillDirFromSkillFilePath(skillPath))
	base := strings.ToLower(filepath.Base(dir))
	for _, hint := range skillNameHints(skillName) {
		if strings.Contains(dir, hint) || strings.Contains(base, hint) || strings.Contains(hint, base) {
			return true
		}
	}
	return false
}

func skillNameHints(skillName string) []string {
	skillName = strings.ToLower(skillName)
	parts := strings.Split(skillName, "-")
	seen := map[string]struct{}{}
	var hints []string

	addHint := func(value string) {
		value = strings.TrimSpace(value)
		if len(value) < 3 {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		hints = append(hints, value)
	}

	addHint(skillName)
	for i := 1; i < len(parts); i++ {
		addHint(strings.Join(parts[i:], "-"))
	}
	for _, part := range parts {
		addHint(part)
	}
	return hints
}

// --- GitHub 导入 ---

// errGitHubAPIBlocked 表示对 api.github.com 的探测因鉴权/限流（401/403/429）被拒，
// 而非资源真不存在。解析器把这种情况视为「不确定」，可退回乐观的 URL 切分而不
// 中止导入。
var errGitHubAPIBlocked = errors.New("github API blocked (rate limit or auth)")

// doGitHubAPIGet 对 api.github.com URL 执行 GET，并在设置 GITHUB_TOKEN 环境变量
// 时附加 bearer 头。未鉴权的 GitHub API 请求按 IP 限 60 次/小时，在共享的自托管
// 服务器上一眨眼就用完，用户在技能导入期看到的是 403。设置 GITHUB_TOKEN 可提到
// 5000 次/小时。
func doGitHubAPIGet(ctx context.Context, httpClient *http.Client, apiURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	addGitHubAuthHeader(req)
	return httpClient.Do(req)
}

func addGitHubAuthHeader(req *http.Request) {
	if req == nil {
		return
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// githubSpec 保存指向某技能（或单技能仓库）的 github.com URL 的解析结果。
type githubSpec struct {
	owner    string
	repo     string
	ref      string // 空 → 调用方解析默认分支
	skillDir string // 仓库内相对目录，"" 表示仓库根

	// refSegments 保存 /tree/ 或 /blob/ 之后、共同编码 (ref, skillDir) 的原始路径
	// 段。GitHub web URL 不划分支/标签名与仓库内路径的边界，所以当 ref 含 '/'
	// （如 "release/v2"）时，仅 segments[0] 不是 ref。fetchFromGitHub 用
	// resolveGitHubRefAndPath 遍历这些段并向 API 询问哪个前缀是真正的
	// branch/tag/commit。此切片为空时，上方 ref/skillDir 为权威（根 URL）。
	refSegments []string
	// kind 是 "tree" 或 "blob"；根 URL 为 ""。blob 要求最后一段为 SKILL.md，
	// 该段已从 refSegments 剥掉。
	kind string
}

// parseGitHubURL 从 github.com URL 抽取 owner、repo，以及 /tree|/blob 之后的原始段。
// 支持的形态：
//
//	github.com/{owner}/{repo}                                → 根，默认分支
//	github.com/{owner}/{repo}/tree/{ref}/{path...}           → ref / 技能目录
//	github.com/{owner}/{repo}/blob/{ref}/{path.../SKILL.md}  → ref / 技能目录
//
// 简单-ref 快捷方式（segments[0] 是 ref，其余是路径）存入 spec.ref/spec.skillDir；
// refSegments 也一并填充，供 fetchFromGitHub 对 API 消歧含 '/' 的 ref。
func parseGitHubURL(raw string) (githubSpec, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return githubSpec{}, fmt.Errorf("invalid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return githubSpec{}, fmt.Errorf("expected URL format: github.com/{owner}/{repo}[/tree/{ref}/{path}], got: %s", parsed.Path)
	}
	spec := githubSpec{owner: parts[0], repo: strings.TrimSuffix(parts[1], ".git")}
	if len(parts) == 2 {
		return spec, nil
	}
	kind := parts[2]
	if kind != "tree" && kind != "blob" {
		return githubSpec{}, fmt.Errorf("unsupported URL form: github.com/%s/%s/%s/... (use /tree/{ref}/... or /blob/{ref}/.../SKILL.md)", spec.owner, spec.repo, kind)
	}
	if len(parts) < 4 || parts[3] == "" {
		return githubSpec{}, fmt.Errorf("missing ref after /%s/", kind)
	}
	spec.kind = kind
	rest := parts[3:]
	if kind == "blob" {
		if !strings.EqualFold(rest[len(rest)-1], "SKILL.md") {
			return githubSpec{}, fmt.Errorf("blob URL must point to a SKILL.md file")
		}
		rest = rest[:len(rest)-1]
		if len(rest) == 0 {
			return githubSpec{}, fmt.Errorf("missing ref after /blob/")
		}
	}
	// 解码 URL 转义的段（如空格），让路径与仓库真实磁盘布局匹配。转义在
	// buildRawGitHubURL 里重新进行。
	decoded := make([]string, len(rest))
	for i, p := range rest {
		d, err := url.PathUnescape(p)
		if err != nil {
			return githubSpec{}, fmt.Errorf("invalid path segment %q: %w", p, err)
		}
		if d == "" {
			return githubSpec{}, fmt.Errorf("empty path segment in URL")
		}
		decoded[i] = d
	}
	spec.refSegments = decoded
	// 乐观切分：假设简单情形 ref 为一段。若乐观猜测验证失败（如 release/v2 型
	// ref），fetchFromGitHub 会经 API 重解并覆写这两个字段。
	spec.ref = decoded[0]
	if len(decoded) > 1 {
		spec.skillDir = strings.Join(decoded[1:], "/")
	}
	return spec, nil
}

// resolveGitHubRefAndPath 遍历解析出的 refSegments，向 GitHub commits API 询问
// 哪个前缀对应真实的 branch/tag/commit。这正是让含 '/' 的 ref（如 "release/v2"）
// 正确工作的关键：URL github.com/o/r/tree/release/v2/skills/foo 在
// (ref=release, path=v2/skills/foo) 与 (ref=release/v2, path=skills/foo) 之间有歧义，
// 因此按从长到短探测 /repos/{o}/{r}/commits/{candidate}，接受服务器确认存在的
// 第一个。
//
// 成功后覆写 spec.ref 与 spec.skillDir。失败（无候选可解析）返回列出每个试过
// 候选的单一错误。
func resolveGitHubRefAndPath(ctx context.Context, httpClient *http.Client, spec *githubSpec) error {
	if len(spec.refSegments) == 0 {
		return nil
	}
	// 先试最长前缀，让 release/v2 胜过 release。
	tried := make([]string, 0, len(spec.refSegments))
	blocked := false
	for n := len(spec.refSegments); n >= 1; n-- {
		candidate := strings.Join(spec.refSegments[:n], "/")
		tried = append(tried, candidate)
		ok, err := githubRefExists(ctx, httpClient, spec.owner, spec.repo, candidate)
		if errors.Is(err, errGitHubAPIBlocked) {
			// 401/403/429 表示无法判定 ref 是否存在。继续试更短的候选，别让一次
			// 坏探测连累常见的单段 ref 情形。
			blocked = true
			continue
		}
		if err != nil {
			// 网络/传输错误不应静默当作「ref 不存在」——冒泡让调用方可重试。
			return fmt.Errorf("validating ref %q: %w", candidate, err)
		}
		if ok {
			spec.ref = candidate
			if n == len(spec.refSegments) {
				spec.skillDir = ""
			} else {
				spec.skillDir = strings.Join(spec.refSegments[n:], "/")
			}
			return nil
		}
	}
	if blocked {
		// 每个探测要么确认 404 要么被限流，从未得到确认。退回 parseGitHubURL
		// 填好的乐观单段切分。若那是错的，后续 raw 拉取会给出更清晰的
		// 「SKILL.md 未找到」错误，而不是让整次导入栽在一个 403 上。
		slog.Warn("github import: ref resolution blocked by GitHub API (rate limit or auth); falling back to optimistic single-segment ref. Set GITHUB_TOKEN to enable disambiguation of slash-bearing refs.",
			"owner", spec.owner, "repo", spec.repo, "tried", tried)
		return nil
	}
	return fmt.Errorf("could not resolve ref in github.com/%s/%s URL — tried: %s. Make sure the branch, tag, or commit exists and that the URL is the canonical /tree/{ref}/{path} or /blob/{ref}/{path}/SKILL.md form",
		spec.owner, spec.repo, strings.Join(tried, ", "))
}

// githubRefExists 当 GitHub 把 ref 识别为 owner/repo 上的 branch、tag 或 commit
// SHA 时返回 true。用 commits 端点是因为一次调用即可接受全部三种 ref（/branches
// 或 /tags 只能匹配一种）。404 表示 ref 不存在；其它非 200 状态视为错误，调用方
// 可区分「缺失」与「API 挂了」。
func githubRefExists(ctx context.Context, httpClient *http.Client, owner, repo, ref string) (bool, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s",
		url.PathEscape(owner), url.PathEscape(repo), escapeRefPath(ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return false, err
	}
	// 按 GitHub 文档：Accept: application/vnd.github.v3.sha 在 ref 可解析时只返回
	// SHA——这是最廉价的探测方式。
	req.Header.Set("Accept", "application/vnd.github.v3.sha")
	addGitHubAuthHeader(req)
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return false, nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return false, errGitHubAPIBlocked
	default:
		return false, fmt.Errorf("github API returned status %d for ref %q", resp.StatusCode, ref)
	}
}

func fetchFromGitHub(ctx context.Context, httpClient *http.Client, rawURL string) (*importedSkill, error) {
	spec, err := parseGitHubURL(rawURL)
	if err != nil {
		return nil, err
	}
	if len(spec.refSegments) > 0 {
		// 在发出任何 raw/contents 请求前，先对 API 消歧含 '/' 的 ref（release/v2 等）。
		if err := resolveGitHubRefAndPath(ctx, httpClient, &spec); err != nil {
			return nil, err
		}
	}
	if spec.ref == "" {
		spec.ref = fetchGitHubDefaultBranch(ctx, httpClient, spec.owner, spec.repo)
	}
	rawPrefix := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
		url.PathEscape(spec.owner), url.PathEscape(spec.repo), escapeRefPath(spec.ref))

	skillMdPath := "SKILL.md"
	if spec.skillDir != "" {
		skillMdPath = spec.skillDir + "/SKILL.md"
	}
	skillMdBody, err := fetchRawFile(ctx, httpClient, buildRawGitHubURL(rawPrefix, skillMdPath))
	if err != nil {
		if spec.skillDir == "" {
			return nil, fmt.Errorf("SKILL.md not found at the root of %s/%s@%s. For multi-skill repositories, point to a specific directory using github.com/%s/%s/tree/%s/<skill-dir>",
				spec.owner, spec.repo, spec.ref, spec.owner, spec.repo, spec.ref)
		}
		return nil, fmt.Errorf("SKILL.md not found at %s in %s/%s@%s: %w",
			skillMdPath, spec.owner, spec.repo, spec.ref, err)
	}

	name, description := skillpkg.ParseSkillFrontmatter(string(skillMdBody))
	if name == "" {
		if spec.skillDir != "" {
			name = filepath.Base(spec.skillDir)
		} else {
			name = spec.repo
		}
	}

	result := &importedSkill{
		name:        name,
		description: description,
		content:     string(skillMdBody),
		origin: map[string]any{
			"type":       "github",
			"source_url": rawURL,
			"owner":      spec.owner,
			"repo":       spec.repo,
			"ref":        spec.ref,
			"path":       spec.skillDir,
		},
	}

	// 从单次递归 tree 枚举支持文件，在下载任何文件前先用 tree 元数据核对导入
	// 限额。tree 不可用或被截断时退回逐目录 contents 爬取（保持宽容，限流的列举
	// 不会毁掉一次已产出有效 SKILL.md 的导入）。
	tree, truncated, treeErr := fetchGitHubTree(ctx, httpClient, spec.owner, spec.repo, spec.ref)
	if treeErr == nil && !truncated {
		if err := addSupportingFilesFromTree(ctx, httpClient, result, tree, rawPrefix, spec.skillDir); err != nil {
			return nil, err
		}
		return result, nil
	}
	if err := addSupportingFilesViaCrawl(ctx, httpClient, result, spec.owner, spec.repo, spec.ref, spec.skillDir); err != nil {
		return nil, err
	}
	return result, nil
}

// --- 共享辅助函数 ---

// rawGitHubContentHost 承载 GitHub raw 文件内容。fetchRawFile 只对这个 host
// 附加 GITHUB_TOKEN：同一函数还会从非 GitHub 技能源（clawhub.ai、skills.sh）
// 下载文件，无条件的鉴权头会把 token 泄漏给那些第三方 host。
const rawGitHubContentHost = "raw.githubusercontent.com"

// fetchRawFile 下载 URL 并返回响应体字节。响应超过 maxImportFileSize 时返回
// 错误，绝不静默把下载了一半的技能文件截进工作区。
func fetchRawFile(ctx context.Context, httpClient *http.Client, fileURL string) ([]byte, error) {
	req, err := newRawFileRequest(ctx, fileURL)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImportFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxImportFileSize {
		return nil, fmt.Errorf("%w: file exceeds %d byte limit", errImportCapExceeded, maxImportFileSize)
	}
	return body, nil
}

// newRawFileRequest 构建 raw 技能文件的 GET 请求，仅在 URL 指向 GitHub raw
// 内容 host 时附加 GitHub 鉴权头。host 门控放在这里、独立于往返，便于无网络
// 单测——GITHUB_TOKEN 绝不能到达非 GitHub 技能 host。
func newRawFileRequest(ctx context.Context, fileURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(req.URL.Hostname(), rawGitHubContentHost) {
		addGitHubAuthHeader(req)
	}
	return req, nil
}

// escapeRefPath 逐段百分号编码 git ref，让含 '/' 的 ref（如 "release/v2"）以
// "release/v2"（保留路径分隔符）发给 GitHub，而非 "release%2Fv2"（commits/raw
// 端点不接受后者）。
func escapeRefPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func buildRawGitHubURL(rawPrefix, repoPath string) string {
	parts := strings.Split(strings.Trim(repoPath, "/"), "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(part))
	}
	if len(escaped) == 0 {
		return rawPrefix
	}
	return rawPrefix + "/" + strings.Join(escaped, "/")
}

func buildGitHubContentsURL(owner, repo, repoPath, ref string) string {
	base := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents",
		url.PathEscape(owner), url.PathEscape(repo))
	if repoPath == "" {
		return base + "?ref=" + url.QueryEscape(ref)
	}
	return base + "/" + strings.TrimPrefix(buildRawGitHubURL("", repoPath), "/") + "?ref=" + url.QueryEscape(ref)
}

func skillDirFromSkillFilePath(path string) string {
	if path == "SKILL.md" {
		return ""
	}
	return strings.TrimSuffix(path, "/SKILL.md")
}

func skillMdNotFoundError(owner, repo, skillName string) error {
	return fmt.Errorf("SKILL.md not found in repository %s/%s for skill %s", owner, repo, skillName)
}

func skillImportConflictReason() string {
	return "a skill with this name already exists; use --on-conflict overwrite to replace it or --on-conflict rename to import a copy"
}

func (h *Handler) createImportedSkillWithName(ctx context.Context, workspaceID, creatorID pgtype.UUID, name string, imported *importedSkill, config map[string]any, files []CreateSkillFileRequest) (SkillWithFilesResponse, error) {
	return h.createSkillWithFiles(ctx, skillCreateInput{
		WorkspaceID: workspaceID,
		CreatorID:   creatorID,
		Name:        name,
		Description: imported.description,
		Content:     imported.content,
		Config:      config,
		Files:       files,
	})
}

func (h *Handler) createRenamedImportedSkill(ctx context.Context, workspaceID, creatorID pgtype.UUID, baseName string, imported *importedSkill, config map[string]any, files []CreateSkillFileRequest) (SkillWithFilesResponse, error) {
	for suffix := 2; suffix < maxImportRenameAttempts+2; suffix++ {
		candidate := fmt.Sprintf("%s-%d", baseName, suffix)
		resp, err := h.createImportedSkillWithName(ctx, workspaceID, creatorID, candidate, imported, config, files)
		if err == nil {
			return resp, nil
		}
		if !isUniqueViolation(err) {
			return SkillWithFilesResponse{}, err
		}
	}
	return SkillWithFilesResponse{}, fmt.Errorf("failed to find an available renamed skill name after %d attempts", maxImportRenameAttempts)
}

func skillImportOverwriteFailure(err error) (int, string) {
	switch {
	case errors.Is(err, errSkillOverwriteNotFound):
		return http.StatusConflict, "target skill no longer exists"
	case errors.Is(err, errSkillOverwriteForbidden):
		return http.StatusForbidden, "only the skill creator can overwrite this skill"
	case errors.Is(err, errSkillOverwriteNameMismatch):
		return http.StatusConflict, "target skill name no longer matches the imported skill"
	default:
		return http.StatusInternalServerError, "failed to overwrite skill"
	}
}

func (h *Handler) resolveImportSkillConflict(w http.ResponseWriter, r *http.Request, strategy string, workspaceID string, workspaceUUID, creatorUUID pgtype.UUID, creatorID string, name string, imported *importedSkill, config map[string]any, files []CreateSkillFileRequest, existing db.Skill) {
	existingInfo := existingSkillIdentity(existing, creatorID)
	switch strategy {
	case importOnConflictSkip:
		writeJSON(w, http.StatusOK, SkillImportResult{
			Status:        "skipped",
			Reason:        "a skill with this name already exists",
			ExistingSkill: &existingInfo,
		})
	case importOnConflictOverwrite:
		if !canOverwriteSkillByLocalImport(creatorID, existing) {
			writeJSON(w, http.StatusForbidden, SkillImportResult{
				Status:        "failed",
				Reason:        "only the skill creator can overwrite this skill",
				ExistingSkill: &existingInfo,
			})
			return
		}
		resp, err := h.overwriteSkillWithFiles(r.Context(), skillOverwriteInput{
			WorkspaceID:   workspaceUUID,
			TargetSkillID: existing.ID,
			UserID:        creatorID,
			ExpectedName:  name,
			Description:   imported.description,
			Content:       imported.content,
			Config:        config,
			Files:         files,
		})
		if err != nil {
			status, reason := skillImportOverwriteFailure(err)
			if status == http.StatusInternalServerError {
				slog.Warn("overwrite imported skill failed", append(logger.RequestAttrs(r), "error", err)...)
			}
			writeJSON(w, status, SkillImportResult{
				Status:        "failed",
				Reason:        reason,
				ExistingSkill: &existingInfo,
			})
			return
		}
		writeJSON(w, http.StatusOK, SkillImportResult{Status: "updated", Skill: &resp})
	case importOnConflictRename:
		resp, err := h.createRenamedImportedSkill(r.Context(), workspaceUUID, creatorUUID, name, imported, config, files)
		if err != nil {
			slog.Warn("create renamed skill failed", append(logger.RequestAttrs(r), "error", err)...)
			writeJSON(w, http.StatusInternalServerError, SkillImportResult{
				Status:        "failed",
				Reason:        "failed to create renamed skill",
				ExistingSkill: &existingInfo,
			})
			return
		}
		writeJSON(w, http.StatusCreated, SkillImportResult{
			Status:        "created",
			Reason:        "renamed to avoid an existing skill",
			Skill:         &resp,
			ExistingSkill: &existingInfo,
		})
	default:
		writeJSON(w, http.StatusConflict, SkillImportResult{
			Status:        "conflict",
			Reason:        skillImportConflictReason(),
			ExistingSkill: &existingInfo,
		})
	}
}

// --- 导入处理器 ---

// ImportSkill 处理 POST /api/skills/import。两种入参形态：multipart/form-data
// 上传 .skill/.zip 归档（走 importSkillFromArchive），或 JSON 的 URL 导入。
// 需工作区成员；on_conflict 决定撞名策略。全程置于 importFetchTimeout 之下的
// 整体截止内。
func (h *Handler) ImportSkill(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	creatorUUID := parseUUID(creatorID)

	// 上传的技能归档（.skill / .zip）以 multipart/form-data 到达；hosted URL 导入
	// 以 JSON 到达。两者经 finishSkillImport 汇合到同一「创建+冲突」尾巴。
	if isMultipartForm(r) {
		h.importSkillFromArchive(w, r, workspaceID, workspaceUUID, creatorUUID, creatorID)
		return
	}

	var req ImportSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validImportOnConflict(req.OnConflict) {
		writeError(w, http.StatusBadRequest, "on_conflict must be one of: fail, overwrite, rename, skip")
		return
	}
	structuredResult := req.OnConflict != ""
	strategy := req.OnConflict
	if strategy == "" {
		strategy = importOnConflictFail
	}

	source, normalized, err := detectImportSource(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}

	// 把整个服务端拉取约束在比 API 前的反向代理/CDN 网关超时更短的整体截止下。
	// 上游慢时，导入返回清晰错误而不是被代理用 504 切断；客户端断开会取消在途
	// 拉取，而不是让它孤儿式继续跑。
	ctx, cancel := context.WithTimeout(r.Context(), importFetchTimeout)
	defer cancel()

	var imported *importedSkill
	switch source {
	case sourceClawHub:
		imported, err = fetchFromClawHub(ctx, httpClient, normalized)
	case sourceSkillsSh:
		imported, err = fetchFromSkillsSh(ctx, httpClient, normalized)
	case sourceGitHub:
		imported, err = fetchFromGitHub(ctx, httpClient, normalized)
	}
	if err != nil {
		status, msg := importFetchErrorResponse(ctx, err)
		writeError(w, status, msg)
		return
	}

	h.finishSkillImport(w, r, workspaceID, workspaceUUID, creatorUUID, creatorID, strategy, structuredResult, imported)
}

// importFetchTimeout 限制从上游源抓取技能文件的总耗时。它刻意低于反向代理网关
// 超时，让慢或过大的源以可读的 API 错误呈现，而不是代理 504。
const importFetchTimeout = 45 * time.Second

// importFetchErrorResponse 把拉取失败映射为 HTTP 状态与消息。超限→413（技能
// 太大无法导入）、截止耗尽→504、瞬时不可用源→可重试 503，其余保持 502。
func importFetchErrorResponse(ctx context.Context, err error) (int, string) {
	if isCapError(err) {
		return http.StatusRequestEntityTooLarge, err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
		return http.StatusGatewayTimeout, "skill import timed out fetching source files; the skill may be too large or the source too slow"
	}
	if errors.Is(err, errImportSourceUnavailable) {
		return http.StatusServiceUnavailable, err.Error()
	}
	return http.StatusBadGateway, err.Error()
}

// importedSkillFileRequests 把抓取到的包内支持文件映射成 CreateSkillFileRequest，
// 丢弃路径过不了 validateFilePath 的条目。
func importedSkillFileRequests(imported *importedSkill) []CreateSkillFileRequest {
	files := make([]CreateSkillFileRequest, 0, len(imported.files))
	for _, f := range imported.files {
		if !validateFilePath(f.path) {
			continue
		}
		files = append(files, CreateSkillFileRequest{
			Path: f.path, Content: f.content, Mode: f.mode, data: []byte(f.content),
		})
	}
	return files
}

// finishSkillImport 执行所有技能导入共享的尾巴——无论包来自 hosted URL 还是上传
// 的归档（.skill / .zip）。它把抽取出的文件映射为 CreateSkillFileRequest、把来源
// 信息记入 config.origin，然后创建技能；撞名按 on_conflict 策略路由。
func (h *Handler) finishSkillImport(w http.ResponseWriter, r *http.Request, workspaceID string, workspaceUUID, creatorUUID pgtype.UUID, creatorID, strategy string, structuredResult bool, imported *importedSkill) {
	files := importedSkillFileRequests(imported)

	// 把来源信息持久化进 skill.config.origin，让列表/详情 UI 能展示
	// 「从 GitHub / ClawHub / Skills.sh 导入」并回链到源。
	config := map[string]any{}
	if imported.origin != nil {
		config["origin"] = imported.origin
	}
	name := sanitizeNullBytes(imported.name)

	if structuredResult {
		if existing, found, lerr := h.lookupSkillByName(r.Context(), workspaceUUID, name); lerr != nil {
			slog.Warn("look up existing skill failed", append(logger.RequestAttrs(r), "error", lerr)...)
			writeJSON(w, http.StatusInternalServerError, SkillImportResult{
				Status: "failed",
				Reason: "failed to check for existing skill",
			})
			return
		} else if found {
			h.resolveImportSkillConflict(w, r, strategy, workspaceID, workspaceUUID, creatorUUID, creatorID, name, imported, config, files, existing)
			return
		}
	}

	resp, err := h.createImportedSkillWithName(r.Context(), workspaceUUID, creatorUUID, name, imported, config, files)
	if err != nil {
		if isUniqueViolation(err) {
			if structuredResult {
				if existing, found, lerr := h.lookupSkillByName(r.Context(), workspaceUUID, name); lerr == nil && found {
					h.resolveImportSkillConflict(w, r, strategy, workspaceID, workspaceUUID, creatorUUID, creatorID, name, imported, config, files, existing)
					return
				}
			}
			if existing, found, findErr := h.existingSkillIdentityByName(r.Context(), workspaceUUID, name); findErr == nil && found {
				writeSkillImportDuplicateConflict(w, existing)
			} else {
				writeError(w, http.StatusConflict, "a skill with this name already exists")
			}
			return
		}
		slog.Warn("create skill failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create skill")
		return
	}
	if structuredResult {
		writeJSON(w, http.StatusCreated, SkillImportResult{Status: "created", Skill: &resp})
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// --- 技能文件端点 ---

// ListSkillFiles 处理 GET /api/skills/{id}/files。?include=metadata 时返回
// path/size/hash（不下载正文），否则返回全部文件正文。
func (h *Handler) ListSkillFiles(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	includeContent, ok := resolveSkillInclude(w, r)
	if !ok {
		return
	}
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}

	if !includeContent {
		metadata, err := h.Queries.ListSkillFileMetadata(r.Context(), skill.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list skill files")
			return
		}
		resp := make([]SkillFileMetadataResponse, len(metadata))
		for i, f := range metadata {
			resp[i] = skillFileMetadataToResponse(f)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	files, err := h.Queries.ListSkillFiles(r.Context(), skill.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skill files")
		return
	}

	resp := make([]SkillFileResponse, len(files))
	for i, f := range files {
		resp[i] = skillFileToResponse(f)
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpsertSkillFile 处理 PUT /api/skills/{id}/files。门控 canManageSkill；路径需
// 安全且非保留的 SKILL.md；写入前经 sanitizeNullBytes。
func (h *Handler) UpsertSkillFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}

	var req CreateSkillFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !validateFilePath(req.Path) {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}
	if skillpkg.IsReservedContentPath(req.Path) {
		writeError(w, http.StatusBadRequest, "SKILL.md is reserved for the primary skill content")
		return
	}

	files := []CreateSkillFileRequest{req}
	if err := h.prepareSkillFiles(r.Context(), skill.WorkspaceID, skill.ID, files); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	h.saveSkillFile(w, r, skill, files[0])
}

// DeleteSkillFile 处理 DELETE /api/skills/{id}/files/{fileId}。门控 canManageSkill；
// 校验该文件确属刚授权过的父技能。成功返回 204。
func (h *Handler) DeleteSkillFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}

	fileID := chi.URLParam(r, "fileId")
	fileUUID, ok := parseUUIDOrBadRequest(w, fileID, "file id")
	if !ok {
		return
	}
	// 校验该文件确属刚授权过的父技能——防止借 URL 参数删除属于别的技能的文件。
	file, err := h.Queries.GetSkillFile(r.Context(), fileUUID)
	if err != nil || uuidToString(file.SkillID) != uuidToString(skill.ID) {
		writeError(w, http.StatusNotFound, "skill file not found")
		return
	}
	if err := h.Queries.DeleteSkillFile(r.Context(), file.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete skill file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Agent-Skill 关联 ---

// ListAgentSkills 处理 GET /api/agents/{id}/skills，返回该 agent 绑定的技能
// 摘要（enabled 反映 agent_skill 行的启用状态）。
func (h *Handler) ListAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}

	skills, err := h.Queries.ListAgentSkillSummaries(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent skills")
		return
	}

	resp := make([]SkillSummaryResponse, len(skills))
	for i, s := range skills {
		resp[i] = skillSummaryToResponse(
			s.ID, s.WorkspaceID, s.Name, s.Description, s.Config,
			s.CreatedBy, s.CreatedAt, s.UpdatedAt,
		)
		resp[i].Enabled = &s.Enabled
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetAgentSkills 处理 PUT /api/agents/{id}/skills——整体替换 agent 绑定的技能
// 集合。门控 canManageAgent；所有 skill 必须属于该工作区。事务内先清后加并记录
// 事件。
func (h *Handler) SetAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	var req SetAgentSkillsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skillUUIDs, ok := parseUUIDSliceOrBadRequest(w, req.SkillIDs, "skill_ids")
	if !ok {
		return
	}
	if !h.validateAgentSkillIDsInWorkspace(w, r, agent, skillUUIDs) {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)

	if err := qtx.RemoveAllAgentSkills(r.Context(), agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear agent skills")
		return
	}

	for _, skillID := range skillUUIDs {
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{
			AgentID: agent.ID,
			SkillID: skillID,
		}); err != nil {
			slog.Warn("add agent skill failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to add agent skill")
			return
		}
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAgentSkillsDomainEvent(agent, actorType, actorID, "set", skillUUIDs))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent skill event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	h.writeUpdatedAgentSkills(w, r, agent, updatedEvent)
}

// AddAgentSkills 处理 POST /api/agents/{id}/skills——增量绑定技能。门控与
// 校验同 SetAgentSkills。
func (h *Handler) AddAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	var req AddAgentSkillsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skillUUIDs, ok := parseUUIDSliceOrBadRequest(w, req.SkillIDs, "skill_ids")
	if !ok {
		return
	}
	if !h.validateAgentSkillIDsInWorkspace(w, r, agent, skillUUIDs) {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)
	for _, skillID := range skillUUIDs {
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{
			AgentID: agent.ID,
			SkillID: skillID,
		}); err != nil {
			slog.Warn("add agent skill failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to add agent skill")
			return
		}
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAgentSkillsDomainEvent(agent, actorType, actorID, "add", skillUUIDs))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent skill event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	h.writeUpdatedAgentSkills(w, r, agent, updatedEvent)
}

// SetAgentSkillEnabled 处理 PATCH /api/agents/{id}/skills/{skillId}——启用或
// 停用某条 agent-skill 绑定。门控 canManageAgent；绑定不存在返回 404。
func (h *Handler) SetAgentSkillEnabled(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	skillID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "skillId"), "skill_id")
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	rows, err := qtx.SetAgentSkillEnabled(r.Context(), db.SetAgentSkillEnabledParams{
		AgentID: agent.ID,
		SkillID: skillID,
		Enabled: *req.Enabled,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update agent skill")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "agent skill not found")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAgentSkillsDomainEvent(agent, actorType, actorID, "enable", []pgtype.UUID{skillID}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent skill event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	h.writeUpdatedAgentSkills(w, r, agent, updatedEvent)
}

// RemoveAgentSkill 处理 DELETE /api/agents/{id}/skills/{skillId}——解除一条
// agent-skill 绑定。门控 canManageAgent。
func (h *Handler) RemoveAgentSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	skillID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "skillId"), "skill_id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.RemoveAgentSkill(r.Context(), db.RemoveAgentSkillParams{
		AgentID: agent.ID,
		SkillID: skillID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove agent skill")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAgentSkillsDomainEvent(agent, actorType, actorID, "remove", []pgtype.UUID{skillID}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent skill event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}
	h.writeUpdatedAgentSkills(w, r, agent, updatedEvent)
}

// validateAgentSkillIDsInWorkspace 校验待绑定技能都属该 agent 所在工作区且存在
// （去重后逐个查）；不合法时已写好 404 响应。
func (h *Handler) validateAgentSkillIDsInWorkspace(w http.ResponseWriter, r *http.Request, agent db.Agent, skillUUIDs []pgtype.UUID) bool {
	seen := map[string]struct{}{}
	for _, skillID := range skillUUIDs {
		key := uuidToString(skillID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{
			ID:          skillID,
			WorkspaceID: agent.WorkspaceID,
		}); err != nil {
			writeError(w, http.StatusNotFound, "skill not found")
			return false
		}
	}
	return true
}

// writeUpdatedAgentSkills 重载 agent 的最新技能列表写入事件负载并发布，然后以
// 该列表作为变更端点响应。
func (h *Handler) writeUpdatedAgentSkills(w http.ResponseWriter, r *http.Request, agent db.Agent, event events.Event) {
	skills, err := h.Queries.ListAgentSkillSummaries(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent skills")
		return
	}

	resp := make([]SkillSummaryResponse, len(skills))
	for i, s := range skills {
		resp[i] = skillSummaryToResponse(
			s.ID, s.WorkspaceID, s.Name, s.Description, s.Config,
			s.CreatedBy, s.CreatedAt, s.UpdatedAt,
		)
		resp[i].Enabled = &s.Enabled
	}
	if event.Payload != nil {
		if payload, ok := event.Payload.(map[string]any); ok {
			payload["skills"] = resp
		}
		h.publishEvent(event)
	}
	writeJSON(w, http.StatusOK, resp)
}

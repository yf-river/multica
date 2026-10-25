// 本文件实现「从源头刷新技能」端点：按技能存储的 config.origin 溯源重新下载并
// 就地覆盖。来源 URL 钉在技能自身（管理源不可注入任意内容），资格校验发生在任何
// 网络请求之前（不给越权调用者烧 45s 抓取预算）。身份/绑定保留，正文与文件换成
// 上游最新状态。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// errSkillNotRefreshable 标记一个「存储溯源无法再抓取」的技能：手工创建的技能、
// 归档上传（没有 origin）、runtime-local 副本，或来源 URL 与其类型不再匹配的
// 手工编辑 origin。
var errSkillNotRefreshable = errors.New("this skill was not imported from a refreshable source (GitHub, skills.sh, or ClawHub)")

// skillOriginRef 是重跑一次导入所需的最小溯源信息。
type skillOriginRef struct {
	Type      string
	SourceURL string
}

// parseSkillOrigin 从技能的 JSONB config 块里解出 config.origin.{type,source_url}。
// config 中没有可解码的 origin 对象时 ok=false。
func parseSkillOrigin(config []byte) (skillOriginRef, bool) {
	if len(config) == 0 {
		return skillOriginRef{}, false
	}
	var decoded struct {
		Origin struct {
			Type      string `json:"type"`
			SourceURL string `json:"source_url"`
		} `json:"origin"`
	}
	if err := json.Unmarshal(config, &decoded); err != nil {
		return skillOriginRef{}, false
	}
	if decoded.Origin.Type == "" {
		return skillOriginRef{}, false
	}
	return skillOriginRef{
		Type:      decoded.Origin.Type,
		SourceURL: strings.TrimSpace(decoded.Origin.SourceURL),
	}, true
}

// refreshableOriginSource 把存储的 origin 类型映射到它的 source_url 必须解析到
// 的 importSource。无法重抓的 origin（manual、runtime_local、归档上传）返回
// ok=false。
func refreshableOriginSource(originType string) (importSource, bool) {
	switch originType {
	case "github":
		return sourceGitHub, true
	case "skills_sh":
		return sourceSkillsSh, true
	case "clawhub":
		return sourceClawHub, true
	default:
		return 0, false
	}
}

// fetchImportedSkillFromOrigin 按技能存储的溯源重跑一次导入抓取。任何网络调用前
// 先校验资格：不可刷新的 origin 类型、空的 source_url、或 source_url 解析不到该
// origin 自己的来源（手工改过的 config）都会在不触达上游的情况下返回
// errSkillNotRefreshable。
func fetchImportedSkillFromOrigin(ctx context.Context, httpClient *http.Client, origin skillOriginRef) (*importedSkill, error) {
	expected, ok := refreshableOriginSource(origin.Type)
	if !ok || origin.SourceURL == "" {
		return nil, errSkillNotRefreshable
	}
	source, normalized, err := detectImportSource(origin.SourceURL)
	if err != nil || source != expected {
		return nil, errSkillNotRefreshable
	}
	switch source {
	case sourceClawHub:
		return fetchFromClawHub(ctx, httpClient, normalized)
	case sourceSkillsSh:
		return fetchFromSkillsSh(ctx, httpClient, normalized)
	case sourceGitHub:
		return fetchFromGitHub(ctx, httpClient, normalized)
	}
	return nil, errSkillNotRefreshable
}

// mergeSkillConfigOrigin 只改写技能 config 的 origin 键，保留用户可能经 API/CLI
// 设置的任何其它键。这刻意比「导入即整体覆盖」的路径更窄：刷新只动溯源，不碰
// 用户配置。
func mergeSkillConfigOrigin(existingConfig []byte, origin map[string]any) map[string]any {
	config := map[string]any{}
	if len(existingConfig) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(existingConfig, &decoded); err == nil && decoded != nil {
			config = decoded
		}
	}
	if origin != nil {
		config["origin"] = origin
	}
	return config
}

// RefreshSkill 从技能存储的 config.origin 源重新下载并就地覆盖技能。身份
// （id、created_by、created_at）与 agent_skill 绑定保留；name、description、
// content、files、config.origin 替换为上游最新状态。与仅限创建者的导入覆盖路径
// 不同，刷新走 canManageSkill 策略（工作区 owner/admin 或创建者）：来源 URL 钉在
// 技能自身，因此刷新的 admin 无法注入任意内容。
func (h *Handler) RefreshSkill(w http.ResponseWriter, r *http.Request) {
	skill, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 任何上游抓取之前先做权限校验，越权调用者烧不掉 45s 的抓取预算。镜像
	// canManageSkill，保留 member 行用于在覆盖事务内复查权限。
	workspaceID := uuidToString(skill.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "skill not found", "owner", "admin", "member")
	if !ok {
		return
	}
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	isCreator := skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == userID
	if !isAdmin && !isCreator {
		writeError(w, http.StatusForbidden, "only the skill creator or a workspace admin can update this skill from its source")
		return
	}

	origin, ok := parseSkillOrigin(skill.Config)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, errSkillNotRefreshable.Error())
		return
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(r.Context(), importFetchTimeout)
	defer cancel()

	imported, err := fetchImportedSkillFromOrigin(ctx, httpClient, origin)
	if err != nil {
		if errors.Is(err, errSkillNotRefreshable) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		status, msg := importFetchErrorResponse(ctx, err)
		writeError(w, status, msg)
		return
	}

	newName := sanitizeNullBytes(imported.name)
	if newName != skill.Name {
		slog.Info("skill refresh: adopting upstream rename",
			"skill_id", uuidToString(skill.ID), "old_name", skill.Name, "new_name", newName)
	}

	resp, err := h.overwriteSkillWithFiles(r.Context(), skillOverwriteInput{
		WorkspaceID:   skill.WorkspaceID,
		TargetSkillID: skill.ID,
		UserID:        userID,
		NewName:       newName,
		AllowOverwrite: func(uid string, s db.Skill) bool {
			return isAdmin || (s.CreatedBy.Valid && uuidToString(s.CreatedBy) == uid)
		},
		Description: imported.description,
		Content:     imported.content,
		Config:      mergeSkillConfigOrigin(skill.Config, imported.origin),
		Files:       importedSkillFileRequests(imported),
	})
	if err != nil {
		switch {
		case errors.Is(err, errSkillOverwriteNotFound):
			writeError(w, http.StatusNotFound, "skill not found")
		case errors.Is(err, errSkillOverwriteForbidden):
			writeError(w, http.StatusForbidden, "only the skill creator or a workspace admin can update this skill from its source")
		case errors.Is(err, errSkillOverwriteNameConflict):
			writeError(w, http.StatusConflict, "a skill named \""+newName+"\" already exists in this workspace")
		default:
			slog.Warn("update skill from source failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to update skill from source")
		}
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

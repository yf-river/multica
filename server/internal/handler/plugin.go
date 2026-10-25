// 本文件：Plugin v1（工作区级插件安装）的 HTTP 接口层。功能开关打开后才可用（否则 503）。
// 覆盖两步安装（preview 读 scope 清单 → install 落库）、列安装、启停、改配置、卸载。错误
// 一律经 writePluginError 按 service.PluginError.Kind 映射状态码，避免在 handler 里字符串
// 匹配错误消息。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// pluginsV1Enabled 判断 Plugin v1 功能开关是否打开（该特性还处于功能开关保护期）。
func (h *Handler) pluginsV1Enabled(ctx context.Context) bool {
	return featureflags.PluginsV1Enabled(ctx, h.FeatureFlags)
}

// requirePluginsV1 是所有 plugin 路由的统一闸门：功能没开就直接 503，省得每个 handler 各写一遍。
func (h *Handler) requirePluginsV1(w http.ResponseWriter, r *http.Request) bool {
	if h.pluginsV1Enabled(r.Context()) {
		return true
	}
	writeFeatureDisabled(w, "plugin_api_disabled", "Plugin management is not enabled")
	return false
}

// writePluginError 把 service 层错误映射成 HTTP 状态码。Kind 的存在让 handler 不必靠
// 字符串匹配错误消息来挑状态码。
func writePluginError(w http.ResponseWriter, err error, fallback string) {
	var pluginErr *service.PluginError
	if !errors.As(err, &pluginErr) {
		writeError(w, http.StatusInternalServerError, fallback)
		return
	}
	switch pluginErr.Kind {
	case service.PluginErrorInvalid:
		writeError(w, http.StatusBadRequest, pluginErr.Message)
	case service.PluginErrorNotFound:
		writeError(w, http.StatusNotFound, pluginErr.Message)
	case service.PluginErrorConflict:
		writeError(w, http.StatusConflict, pluginErr.Message)
	case service.PluginErrorForbidden:
		writeError(w, http.StatusForbidden, pluginErr.Message)
	case service.PluginErrorIncompatible:
		writeError(w, http.StatusUnprocessableEntity, pluginErr.Message)
	case service.PluginErrorQuota:
		writeError(w, http.StatusInsufficientStorage, pluginErr.Message)
	default:
		writeError(w, http.StatusBadGateway, pluginErr.Message)
	}
}

// pluginInstallationResponse 是插件安装记录对外的 JSON 结构，永远不会带机密值：`config`
// 按构造只含非机密字段（SetConfig 把机密导进自己那张加密表），已配置的机密以名字出现在
// `configured_secrets`。
type pluginInstallationResponse struct {
	ID          string `json:"id"`
	PluginKey   string `json:"plugin_key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
	// 本次安装所绑定的已发布版本。绑定后任何东西都不会在工作区脚下悄悄变：升级等于指向
	// 另一个版本 id，那是第二次同意。
	PackageVersionID  string                      `json:"package_version_id"`
	Enabled           bool                        `json:"enabled"`
	GrantedScopes     []string                    `json:"granted_scopes"`
	ConfigSchema      []service.PluginConfigField `json:"config_schema"`
	Config            map[string]any              `json:"config"`
	ConfiguredSecrets []string                    `json:"configured_secrets"`
	Surfaces          []plugincontract.Surface    `json:"surfaces"`
	Hooks             []pluginHookResponse        `json:"hooks"`
	Resources         []plugincontract.Resource   `json:"resources"`
	CreatedAt         string                      `json:"created_at"`
	UpdatedAt         string                      `json:"updated_at"`
}

// pluginHookResponse 省略 input_schema：设置页要列的是「这个 hook 是什么、谁能触发」，原始
// 的 JSON Schema 在那儿只是噪音。
type pluginHookResponse struct {
	Key         string                      `json:"key"`
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Triggers    []string                    `json:"triggers"`
	Events      []string                    `json:"events,omitempty"`
	Transport   string                      `json:"transport"`
	Schedule    *pluginHookScheduleResponse `json:"schedule,omitempty"`
}

// pluginHookScheduleResponse 是定时型 hook（cron）的调度信息：下次执行时间按 UTC 算好给出。
type pluginHookScheduleResponse struct {
	Cron      string `json:"cron"`
	Timezone  string `json:"timezone"`
	NextRunAt string `json:"next_run_at,omitempty"`
}

// pluginInstallationPayload 把一条安装记录装配成对外结构：解析 manifest、查已配置机密名、
// 解析 config 与授权 scope、补齐每个 hook 的 cron 调度信息。
func (h *Handler) pluginInstallationPayload(ctx context.Context, installation db.PluginInstallation) (pluginInstallationResponse, error) {
	manifest, err := service.ParseInstallationManifest(installation)
	if err != nil {
		return pluginInstallationResponse{}, err
	}
	secrets, err := h.PluginService.ConfiguredSecretKeys(ctx, installation.ID)
	if err != nil {
		return pluginInstallationResponse{}, err
	}

	config := map[string]any{}
	if len(installation.Config) > 0 {
		_ = json.Unmarshal(installation.Config, &config)
	}
	var granted []string
	if len(installation.GrantedScopes) > 0 {
		_ = json.Unmarshal(installation.GrantedScopes, &granted)
	}
	if granted == nil {
		granted = []string{}
	}
	scheduleRows, err := h.Queries.ListPluginHookSchedulesByInstallation(ctx, installation.ID)
	if err != nil {
		return pluginInstallationResponse{}, err
	}
	schedules := make(map[string]db.PluginHookSchedule, len(scheduleRows))
	for _, schedule := range scheduleRows {
		schedules[schedule.HookKey] = schedule
	}

	hooks := make([]pluginHookResponse, 0, len(manifest.Contributes.Hooks))
	for _, hook := range manifest.Contributes.Hooks {
		response := pluginHookResponse{
			Key:         hook.Key,
			Name:        hook.Name,
			Description: hook.Description,
			Triggers:    hook.Triggers,
			Events:      hook.Events,
			Transport:   hook.Transport.Type,
		}
		if schedule, ok := schedules[hook.Key]; ok {
			response.Schedule = &pluginHookScheduleResponse{
				Cron:     schedule.CronExpression,
				Timezone: schedule.Timezone,
			}
			if schedule.NextRunAt.Valid {
				response.Schedule.NextRunAt = schedule.NextRunAt.Time.UTC().Format(timeFormatRFC3339)
			}
		}
		hooks = append(hooks, response)
	}
	surfaces := manifest.Contributes.Surfaces
	if surfaces == nil {
		surfaces = []plugincontract.Surface{}
	}
	resources := manifest.Contributes.Resources
	if resources == nil {
		resources = []plugincontract.Resource{}
	}

	return pluginInstallationResponse{
		ID:                uuidToString(installation.ID),
		PluginKey:         installation.PluginKey,
		Name:              manifest.Name,
		Description:       manifest.Description,
		Version:           installation.Version,
		PackageVersionID:  uuidToString(installation.PackageVersionID),
		Enabled:           installation.Enabled,
		GrantedScopes:     granted,
		ConfigSchema:      service.ConfigFieldsForManifest(manifest),
		Config:            config,
		ConfiguredSecrets: secrets,
		Surfaces:          surfaces,
		Hooks:             hooks,
		Resources:         resources,
		CreatedAt:         installation.CreatedAt.Time.UTC().Format(timeFormatRFC3339),
		UpdatedAt:         installation.UpdatedAt.Time.UTC().Format(timeFormatRFC3339),
	}, nil
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"

// ListPlugins 处理 GET /api/workspaces/{id}/plugins——列出工作区已安装的插件
// （功能开关关闭时 503）。
func (h *Handler) ListPlugins(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace_id")
	if !ok {
		return
	}
	installations, err := h.Queries.ListWorkspacePluginInstallations(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list Plugins")
		return
	}
	payloads := make([]pluginInstallationResponse, 0, len(installations))
	for _, installation := range installations {
		payload, err := h.pluginInstallationPayload(r.Context(), installation)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list Plugins")
			return
		}
		payloads = append(payloads, payload)
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": payloads})
}

// previewPluginRequest 是预览接口的请求体：只带要装的插件版本 id。
type previewPluginRequest struct {
	VersionID string `json:"version_id"`
}

// PreviewPlugin 处理 POST /api/workspaces/{id}/plugins/preview——两步安装的第一步。
// 什么都不落库：响应存在的意义就是让管理员在同意前先读一遍 scope 清单。
func (h *Handler) PreviewPlugin(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace_id")
	if !ok {
		return
	}
	var req previewPluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	preview, err := h.PluginService.PreviewPlugin(r.Context(), workspaceID, req.VersionID)
	if err != nil {
		writePluginError(w, err, "failed to read the Plugin manifest")
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// installPluginRequest 是安装接口的请求体：插件版本 id + 管理员授权给它的 scope 列表。
type installPluginRequest struct {
	VersionID     string   `json:"version_id"`
	GrantedScopes []string `json:"granted_scopes"`
}

// InstallPlugin 处理 POST /api/workspaces/{id}/plugins——两步安装的第二步：落库一条安装
// 记录（操作者记为该工作区成员）。
func (h *Handler) InstallPlugin(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	workspaceIDString := workspaceIDFromURL(r, "id")
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDString, "workspace_id")
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, workspaceIDString)
	if !ok {
		return
	}
	var req installPluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	installation, err := h.PluginService.InstallPlugin(r.Context(), workspaceID, member.UserID, req.VersionID, req.GrantedScopes)
	if err != nil {
		writePluginError(w, err, "failed to install the Plugin")
		return
	}
	payload, err := h.pluginInstallationPayload(r.Context(), installation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to install the Plugin")
		return
	}
	writeJSON(w, http.StatusCreated, payload)
}

// configurePluginRequest 是配置接口的请求体：一组键值对（非机密字段；机密走各自接口）。
type configurePluginRequest struct {
	Values map[string]any `json:"values"`
}

// ConfigurePlugin 处理 PUT /api/workspaces/{id}/plugins/{installationId}/config——更新插件
// 的非机密配置。
func (h *Handler) ConfigurePlugin(w http.ResponseWriter, r *http.Request) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	var req configurePluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	updated, err := h.PluginService.SetConfig(r.Context(), installation, req.Values)
	if err != nil {
		writePluginError(w, err, "failed to configure the Plugin")
		return
	}
	payload, err := h.pluginInstallationPayload(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to configure the Plugin")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// EnablePlugin 处理 POST /api/workspaces/{id}/plugins/{installationId}/enable——启用插件。
func (h *Handler) EnablePlugin(w http.ResponseWriter, r *http.Request) {
	h.setPluginEnabled(w, r, true)
}

// DisablePlugin 处理 POST /api/workspaces/{id}/plugins/{installationId}/disable——停用插件。
func (h *Handler) DisablePlugin(w http.ResponseWriter, r *http.Request) {
	h.setPluginEnabled(w, r, false)
}

// setPluginEnabled 是启停两个入口的公共实现：置 enabled 位并返回更新后的安装记录。
func (h *Handler) setPluginEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	updated, err := h.PluginService.SetEnabled(r.Context(), installation, enabled)
	if err != nil {
		writePluginError(w, err, "failed to update the Plugin")
		return
	}
	payload, err := h.pluginInstallationPayload(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the Plugin")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// UninstallPlugin 处理 DELETE /api/workspaces/{id}/plugins/{installationId}——卸载插件，
// 成功返回 204。
func (h *Handler) UninstallPlugin(w http.ResponseWriter, r *http.Request) {
	installation, ok := h.pluginInstallationFromURL(w, r)
	if !ok {
		return
	}
	if err := h.PluginService.Uninstall(r.Context(), installation); err != nil {
		writePluginError(w, err, "failed to uninstall the Plugin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pluginInstallationFromURL 从 URL 解析出工作区 + installationId，并加载安装记录（属于
// 别的工作区/不存在时经 writePluginError 回 404 等）。
func (h *Handler) pluginInstallationFromURL(w http.ResponseWriter, r *http.Request) (db.PluginInstallation, bool) {
	if !h.requirePluginsV1(w, r) {
		return db.PluginInstallation{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace_id")
	if !ok {
		return db.PluginInstallation{}, false
	}
	installation, err := h.PluginService.InstallationForWorkspace(r.Context(), workspaceID, chi.URLParam(r, "installationId"))
	if err != nil {
		writePluginError(w, err, "failed to load the Plugin")
		return db.PluginInstallation{}, false
	}
	return installation, true
}

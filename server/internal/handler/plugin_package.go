// 本文件：插件发布端点。作者上传一个制品 bundle，Multica 存储它。不再有「按 URL 安装」，
// 插件代码也没有第二条路能进读者浏览器：设置页从这里发布，安装时指定的是这个端点创建的
// 某个版本。
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// Publishing endpoints.
//
// An author uploads an artifact bundle and Multica stores it. There is no
// install-by-URL any more, and no second way for plugin code to reach a reader's
// browser: the settings page publishes here, and installing names a version this
// endpoint created.

// ListPluginPackages 处理 GET /api/workspaces/{id}/plugins/packages——列出工作区发布过的
// 插件包（功能开关关闭时 503）。
func (h *Handler) ListPluginPackages(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace_id")
	if !ok {
		return
	}
	packages, err := h.PluginService.ListPackages(r.Context(), workspaceID)
	if err != nil {
		writePluginError(w, err, "failed to list published Plugins")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": packages})
}

// PublishPluginPackage 处理 POST /api/workspaces/{id}/plugins/packages。
//
// multipart/form-data，带一个 `bundle` 文件：一个装着 manifest 和它提到的每个文件的 zip。
// 入库前做完整校验——缺某个 surface 入口的包在这里就失败，而不是几周后在读者浏览器里炸。
func (h *Handler) PublishPluginPackage(w http.ResponseWriter, r *http.Request) {
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

	// 先限流再让 multipart 读取器看到：否则 ParseMultipartForm 会先把一个不限大小的上传
	// 吐盘成临时文件。
	r.Body = http.MaxBytesReader(w, r.Body, plugincontract.MaxBundleSize+multipartOverheadBytes)
	if err := r.ParseMultipartForm(plugincontract.MaxBundleSize + multipartOverheadBytes); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "the Plugin package upload is too large or malformed")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeError(w, http.StatusBadRequest, "a Plugin package file is required")
		return
	}
	defer file.Close()

	archive, err := io.ReadAll(io.LimitReader(file, plugincontract.MaxBundleSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read the Plugin package")
		return
	}
	if len(archive) > plugincontract.MaxBundleSize {
		writeError(w, http.StatusRequestEntityTooLarge, "the Plugin package is too large")
		return
	}

	published, err := h.PluginService.PublishBundle(r.Context(), workspaceID, member.UserID, archive)
	if err != nil {
		writePluginError(w, err, "failed to publish the Plugin")
		return
	}
	writeJSON(w, http.StatusCreated, published)
}

// multipartOverheadBytes 是给表单自身边界与头留的余量，让恰好等于大小上限的 bundle 不会
// 因为外面套着的信封而被误拒。
const multipartOverheadBytes = 64 * 1024

type publishLocalPluginRequest struct {
	// Name 是 MULTICA_PLUGIN_DIR 下的一个目录。
	Name string `json:"name"`
}

// PublishLocalPluginPackage 处理 POST /api/workspaces/{id}/plugins/packages/local。
//
// 开发通道：直接从 operator 已经托管的一个目录发布，改 surface 不必每次改完都打 zip 上传。
// 它产出的仍是普通不可变版本——不存在会让「这份代码是不是冻结的」取决于插件怎么装的
// 热加载路径。
func (h *Handler) PublishLocalPluginPackage(w http.ResponseWriter, r *http.Request) {
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
	var req publishLocalPluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	published, err := h.PluginService.PublishLocalBundle(r.Context(), workspaceID, member.UserID, strings.TrimSpace(req.Name))
	if err != nil {
		writePluginError(w, err, "failed to publish the Plugin")
		return
	}
	writeJSON(w, http.StatusCreated, published)
}

// DeletePluginPackage 处理 DELETE /api/workspaces/{id}/plugins/packages/{packageId}——
// 删除已发布的插件包，成功返回 204。
func (h *Handler) DeletePluginPackage(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace_id")
	if !ok {
		return
	}
	if err := h.PluginService.DeletePackage(r.Context(), workspaceID, chi.URLParam(r, "packageId")); err != nil {
		writePluginError(w, err, "failed to delete the published Plugin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

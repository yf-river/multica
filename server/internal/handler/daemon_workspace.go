// 本文件实现 daemon 的「最小 workspace 列表」端点 GET /api/daemon/workspaces：每个元素
// 只回 id + name 两列，供本机 daemon（internal/daemon）做 workspace 同步——它据此知道
// 自己该为哪些 workspace 建本地执行目录、注册 runtime。
//
// 返回体用 ETag + If-None-Match 做条件请求（配套 Cache-Control: private, no-cache）：
// daemon 端会缓存上一次拿到的列表与 ETag，内容没变时服务器回 304，省掉整段 body 的
// 传输。配合旧服务器回退，细节见 internal/daemon/client.go 的 ListWorkspaces。
//
// 同一端点按调用方身份返回不同范围：
//   - 用户级凭据（PAT/JWT/mcn_ 云节点 PAT，中间件已盖上 X-User-ID）：返回该用户隶属的
//     全部 workspace。
//   - workspace 作用域的 daemon token（mdt_）：只返回它绑定的那一个 workspace——token
//     在 DaemonAuth 中间件里已被限死在某个 workspace，绝不能借本端点探知别的 workspace。
package handler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// DaemonWorkspaceResponse 是列表里单个 workspace 的最小投影：id + name，没有其它
// 成员/项目等详情——本端点只服务「daemon 想知道有哪些 workspace」这一个用途。
type DaemonWorkspaceResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListDaemonWorkspaces 处理 GET /api/daemon/workspaces。无请求体；成功返回 200 与
// JSON 数组（不是包一层 {workspaces: ...}），成员为空时是 [] 而非 null。
//
// 身份分流：
//   - 请求带 X-User-ID（用户级凭据路径）→ 按该用户查其隶属的全部 workspace；
//     数据库出错回 500。
//   - 否则按 daemon token 路径：从 context 取中间件绑定的 workspace
//     （DaemonWorkspaceIDFromContext）。取不到（两路身份都没有）回 401；取到了但该
//     workspace 已不存在回 404。
//
// 条件请求：先算整个响应数组的弱 ETag（对序列化字节做 sha256），客户端 If-None-Match
// 相同则只回 304 状态、不重传 body。
func (h *Handler) ListDaemonWorkspaces(w http.ResponseWriter, r *http.Request) {
	var resp []DaemonWorkspaceResponse
	if userID := requestUserID(r); userID != "" {
		rows, err := h.Queries.ListDaemonWorkspaces(r.Context(), parseUUID(userID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list daemon workspaces")
			return
		}
		resp = make([]DaemonWorkspaceResponse, len(rows))
		for i, row := range rows {
			resp[i] = daemonWorkspaceToResponse(row.ID, row.Name)
		}
	} else {
		workspaceID := middleware.DaemonWorkspaceIDFromContext(r.Context())
		if workspaceID == "" {
			writeError(w, http.StatusUnauthorized, "daemon workspace identity required")
			return
		}
		row, err := h.Queries.GetDaemonWorkspace(r.Context(), parseUUID(workspaceID))
		if err != nil {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		resp = []DaemonWorkspaceResponse{daemonWorkspaceToResponse(row.ID, row.Name)}
	}

	etag := daemonWorkspacesETag(resp)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// daemonWorkspaceToResponse 把一条 workspace 查询行（pgtype.UUID + name）换算成
// 对外 JSON 形态（UUID 转标准字符串）。
func daemonWorkspaceToResponse(id pgtype.UUID, name string) DaemonWorkspaceResponse {
	return DaemonWorkspaceResponse{ID: uuidToString(id), Name: name}
}

// daemonWorkspacesETag 对整个响应数组的 JSON 序列化字节做 sha256，返回带 W/ 前缀的
// 弱 ETag。同一内容必然得出同一哈希，因此内容没变时客户端可以拿它命中 304。
func daemonWorkspacesETag(workspaces []DaemonWorkspaceResponse) string {
	data, _ := json.Marshal(workspaces)
	sum := sha256.Sum256(data)
	return fmt.Sprintf(`W/"%x"`, sum)
}

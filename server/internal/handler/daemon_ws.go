// 本文件实现 daemon 长连接的 WebSocket 升级入口 GET /api/daemon/ws（挂在 DaemonAuth
// 中间件之下，见 router.go）。它不自己做协议解析——只把 HTTP 请求里的身份与授权范围
// 翻译成 daemonws.ClientIdentity，然后交给 h.DaemonHub.HandleWebSocket 完成升级并启动
// 读写泵。
//
// 授权范围有两类来源，可单独也可叠加：
//   - runtime_ids（URL 查询参数，支持 runtime_id/runtime_ids 两种拼写、逗号分隔）：逐
//     个校验调用方对目标 runtime 有访问权（requireDaemonRuntimeAccess）；daemon token
//     额外要求该 runtime 属于同一个 daemon。校验通过后这些 runtime 构成连接可收推送的
//     范围。
//   - 用户身份（用户级凭据，X-User-ID）：没有 runtime 参数也要能给用户级连接建立——
//     服务器按 user 维度投递的通知（如 workspace 变更提示）走这条通道。
//
// 两者都缺（既没有 runtime_ids 也不是用户身份）回 400。hub 不可用（未初始化）回 503。
//
// 校验拒绝的行为值得注意：对任一 runtime 校验失败都会立刻中止整条握手并已写错误响应
// （4xx/5xx），而不是跳过坏的保留好的——宁可整条连接不建，也不把一份只过了一半校验的
// runtime 集合交给 hub 建立订阅。
package handler

import (
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// DaemonWebSocket 处理 GET /api/daemon/ws：把 daemon（或用户）的 WS 升级请求转成
// daemonws.ClientIdentity 后交给 hub。真正的升级、帧读写、心跳与 RPC 分派都在
// daemonws 包里，本方法只负责「鉴权范围 → identity」这一件事。
//
// 流程：先确认 hub 就绪（否则 503）→ 解析并逐个校验请求的 runtime_ids → 收集这些
// runtime 所属的 workspace（去重）→ 用第一个 workspace 作 primary、连同 daemon id /
// user id / 客户端版本与能力头，一起打包成 identity 交给 hub。
func (h *Handler) DaemonWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.DaemonHub == nil {
		writeError(w, http.StatusServiceUnavailable, "daemon websocket unavailable")
		return
	}

	runtimeIDs := parseRuntimeIDs(r)
	userID := requestUserID(r)
	if len(runtimeIDs) == 0 && userID == "" {
		writeError(w, http.StatusBadRequest, "runtime_ids or user identity required")
		return
	}

	workspaceIDs := make([]string, 0, len(runtimeIDs))
	seenWorkspaceIDs := make(map[string]struct{}, len(runtimeIDs))
	for _, runtimeID := range runtimeIDs {
		rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
		if !ok {
			return
		}
		// daemon token 作用域兜底：即便 requireDaemonRuntimeAccess 放行了 workspace
		// 归属，这条 runtime 也必须属于当前 daemon 自己，否则回 404——防止一台 daemon
		// 通过 WS 订阅到同 workspace 里另一台 daemon 的 runtime 推送。
		if daemonID := middleware.DaemonIDFromContext(r.Context()); daemonID != "" && rt.DaemonID.Valid && rt.DaemonID.String != daemonID {
			writeError(w, http.StatusNotFound, "runtime not found")
			return
		}
		workspaceID := uuidToString(rt.WorkspaceID)
		if workspaceID != "" {
			if _, ok := seenWorkspaceIDs[workspaceID]; !ok {
				seenWorkspaceIDs[workspaceID] = struct{}{}
				workspaceIDs = append(workspaceIDs, workspaceID)
			}
		}
	}

	primaryWorkspaceID := ""
	if len(workspaceIDs) > 0 {
		primaryWorkspaceID = workspaceIDs[0]
	}

	h.DaemonHub.HandleWebSocket(w, r, daemonws.ClientIdentity{
		DaemonID:      middleware.DaemonIDFromContext(r.Context()),
		UserID:        userID,
		WorkspaceID:   primaryWorkspaceID,
		WorkspaceIDs:  workspaceIDs,
		RuntimeIDs:    runtimeIDs,
		ClientVersion: r.Header.Get("X-Client-Version"),
		Capabilities:  r.Header.Get("X-Client-Capabilities"),
	})
}

// parseRuntimeIDs 从 URL 查询参数收集 daemon 想订阅的 runtime id 列表：兼容
// ?runtime_id=a&runtime_id=b 与 ?runtime_ids=a,b（以及两者混用），逐项 trim 空格、跳过
// 空串、按首次出现去重，保持稳定顺序——重复的 runtime id 不应让同一个 runtime 在订阅
// 列表里出现两次。
func parseRuntimeIDs(r *http.Request) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		for _, part := range strings.Split(raw, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	for _, raw := range r.URL.Query()["runtime_id"] {
		add(raw)
	}
	for _, raw := range r.URL.Query()["runtime_ids"] {
		add(raw)
	}
	return out
}

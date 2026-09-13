// 本文件是把 daemon 的 WebSocket 通用 RPC 请求（daemon:rpc_request 帧）接到既有 HTTP
// handler 上的胶水层（MUL-4257）：DaemonRPCHandler 被注册进 daemonws hub（接线见
// cmd/server/router.go 的 SetRPCHandler），按 method 分派；对当前唯一支持的
// "tasks.claim"，它构造一条进程内合成 HTTP 请求去驱动既有的机器级批量认领 handler
// ClaimTasksByRuntime，把后者的鉴权/组装/落库逻辑原样复用一遍——不经过真实网络，而是
// 用一个内存版 http.ResponseWriter（rpcResponseCapture）接住响应。
//
// 复用能成立的前提：WS 连接在建立时已经鉴权并钉死了身份（daemon + 它授权的一组
// runtime/workspace，见 daemon_ws.go），这里把那份身份翻译成 HTTP 路径认的样子
// （daemon context 或 X-User-ID 头），被复用 handler 里的 per-runtime daemon/
// workspace 校验看到的范围就与走 HTTP 时完全一致。
//
// 未识别的 method 回 404；hub 会把错误折成非 2xx 的 rpc_response 帧发给 daemon，daemon
// 随即回退到 HTTP claim——这是设计使然，WS RPC 只是少一次网络往返的加速，不是正确性
// 的依赖。
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// rpcResponseCapture 是一个仅存在于内存中的最小 http.ResponseWriter：只收集响应头、
// 状态码与 body 字节，让一次 WS RPC 可以零网络往返地复用现成的 HTTP handler。
// WriteHeader 只记状态不写任何东西；Write 在没先调过 WriteHeader 时默认按 200 处理
// （与 net/http 的真实语义一致），并把字节追加进 body 缓冲。
type rpcResponseCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *rpcResponseCapture) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *rpcResponseCapture) WriteHeader(code int) { w.status = code }

func (w *rpcResponseCapture) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

// DaemonRPCHandler 是接入 daemonws hub 的 RPCHandler（MUL-4257，接线见 router.go）。
// 它把一条通用 daemon:rpc_request 分派给匹配的 HTTP handler，用一条携带「WS 连接身份」
// 的合成进程内请求去驱动后者，从而完整复用其鉴权 / 载荷组装 / 收尾落库逻辑。连接在
// 建立时已鉴权并钉死到自己的 daemon + runtime 集合（见 daemon_ws.go），所以被复用
// handler 的 per-runtime daemon/workspace 检查看到的范围与 HTTP 路径一致。
func (h *Handler) DaemonRPCHandler(ctx context.Context, identity daemonws.ClientIdentity, method string, body json.RawMessage) (int, json.RawMessage, error) {
	switch method {
	case "tasks.claim":
		return h.rpcClaimTasks(ctx, identity, body)
	default:
		return http.StatusNotFound, nil, fmt.Errorf("unknown rpc method %q", method)
	}
}

// rpcClaimTasks 处理 "tasks.claim" RPC：把 body 原样作为 POST /api/daemon/tasks/claim
// 的请求体交给 ClaimTasksByRuntime。空 body 视为 {}（HTTP 客户端通常也是这么发的）。
func (h *Handler) rpcClaimTasks(ctx context.Context, identity daemonws.ClientIdentity, body json.RawMessage) (int, json.RawMessage, error) {
	if len(body) == 0 {
		body = json.RawMessage("{}")
	}
	reqCtx := ctx
	// 关键的作用域翻译（见文件导读）：daemon token 的连接是「单 workspace」作用域——
	// 把 daemon context 钉上去（用该连接的主 workspace + daemon id），让被复用 handler
	// 的 daemon_id / workspace 校验与 HTTP 路径行为完全一致。而 PAT/云节点连接（没有
	// daemon id）是「按用户经 X-User-ID + workspace 成员身份」授权的，绝不能在这里钉
	// 单个 workspace——那会把该用户其余 workspace 的认领一起挡掉。
	if identity.DaemonID != "" {
		reqCtx = middleware.WithDaemonContext(reqCtx, identity.PrimaryWorkspaceID(), identity.DaemonID)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "/api/daemon/tasks/claim", bytes.NewReader(body))
	if err != nil {
		return http.StatusInternalServerError, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if identity.UserID != "" {
		req.Header.Set("X-User-ID", identity.UserID)
	}
	// 能力 / 版本头在连接建立时捕获（见 daemon_ws.go），这里转发给被复用的 handler，
	// 让它遵守与 HTTP 路径一致的能力闸门与版本分叉。
	if identity.Capabilities != "" {
		req.Header.Set("X-Client-Capabilities", identity.Capabilities)
	}
	if identity.ClientVersion != "" {
		req.Header.Set("X-Client-Version", identity.ClientVersion)
	}

	rec := &rpcResponseCapture{}
	h.ClaimTasksByRuntime(rec, req)
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	return status, json.RawMessage(rec.body.Bytes()), nil
}

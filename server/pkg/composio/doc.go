// Package composio 是一个小巧、独立的 Go SDK，封装了 Composio v3.1 的 REST API。
//
// 它刻意保持自包含：唯一依赖的第三方库是 github.com/go-resty/resty/v2，
// 不 import 任何 Multica 自己的包——别的 Go 服务可以直接复用，
// 甚至不改一行代码就能把它抽成独立模块。
//
// # MVP 所需的面
//
// 这套 SDK 只覆盖 Composio 集成 MVP 真正会用到的接口（见 MUL-3715 / MUL-3720），
// 刻意做得极简——只有第一阶段产品实际调用的端点接了进来：
//
//   - Connect Link         — POST /connected_accounts/link
//   - MCP Session          — POST /tool_router/session
//   - Connected Accounts   — GET /connected_accounts、
//     POST /connected_accounts/{id}/revoke、DELETE /connected_accounts/{id}
//   - Toolkits             — GET /toolkits、GET /toolkits/{slug}
//   - Tool Execute         — POST /tools/execute/{tool_slug}
//   - Webhook              — HMAC-SHA256 签名校验
//
// 以后要加更多面（auth configs、triggers、proxy execute 等）随时可以，
// 不需要改动现有的类型定义。
//
// # 快速上手
//
//	client, err := composio.NewClient(composio.Options{
//	    APIKey: os.Getenv("COMPOSIO_API_KEY"),
//	})
//	if err != nil { return err }
//
//	link, err := client.CreateLink(ctx, composio.CreateLinkRequest{
//	    AuthConfigID: "ac_abc",
//	    UserID:       "u_123",
//	    CallbackURL:  "https://app.example.com/composio/callback",
//	})
//	// 把用户重定向到 link.RedirectURL，让他完成授权
//
//	session, err := client.CreateSession(ctx, composio.CreateSessionRequest{
//	    UserID: "u_123",
//	})
//	// agent 运行时现在可以连 session.MCP.URL，并带上 composio.MCPAuthHeaders(...)
//
// # 错误
//
// 所有非 2xx 响应都会以 *APIError 返回，里面带上游返回的状态码、slug 和 message；
// 网络传输层的错误则由 resty 原样返回，调用方照常用 errors.Is/As 判断。
//
// # Webhook 校验
//
// [VerifyWebhook] 负责校验 Composio 随每次 webhook 投递附上的 HMAC-SHA256
// 签名，并支持配置重放容忍窗口。
// 详见 https://docs.composio.dev/docs/setting-up-triggers/subscribing-to-events#verifying-signatures
package composio

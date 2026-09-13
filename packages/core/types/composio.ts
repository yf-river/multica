// 本文件定义 Composio（第三方工具集成平台）相关的 API 类型。

/** GET /api/integrations/composio/toolkits 返回的一个 Composio 工具包。
 *
 * 线上结构对应 `server/internal/handler/integrations_composio.go` 里的
 * `ComposioToolkitResponse`。后端只返回启用了项目鉴权配置的工具包。 */
export interface ComposioToolkit {
  slug: string;
  name: string;
  logo?: string;
  category?: string;
}

/** 某用户已连接的 Composio 账号，GET /api/integrations/composio/connections
 * 返回。对应服务端的 `ComposioConnectionResponse`。 */
export interface ComposioConnection {
  id: string;
  toolkit_slug: string;
  /** 连接生命周期状态。`expired` 时 UI 展示「重新连接」入口；后端要等
   * Stage 4 的 webhook 处理落地（MUL-3719）后才会发出该值，但客户端
   * 提前渲染了这个分支。 */
  status: "active" | "expired" | "revoked" | string;
  connected_at: string;
  last_used_at?: string | null;
}

/** POST /api/integrations/composio/connect/init 的响应——浏览器被重定向到的
 * Composio 托管连接链接。 */
export interface ComposioConnectInitResponse {
  redirect_url: string;
}

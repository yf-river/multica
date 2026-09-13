// 本文件封装 WebSocket 客户端：负责连接、鉴权、事件分发、自动重连与断线回调。
import type { WSMessage, WSEventType } from "../types/events";
import { type Logger, noopLogger } from "../logger";

type EventHandler = (payload: unknown, actorId?: string, actorType?: string) => void;

// 限制「无法解析的帧」写进日志的长度上限。畸形或恶意服务器可能刷出任意大的垃圾数据，
// 而 warn 处理器可能是 console / IPC 桥，我们不想撑爆它的缓冲。
const UNPARSEABLE_LOG_MAX_CHARS = 200;

// 重连退避参数。固定延迟会在服务器重启后造成大量客户端同时重连（惊群效应）；
// 指数退避加抖动能把重连尝试在时间上摊开。客户端会无限重试（延迟封顶在
// RECONNECT_MAX_DELAY_MS），因为 web/desktop UI 目前还没有可见的断线状态或手动重试入口。
const RECONNECT_BASE_DELAY_MS = 1_000;
const RECONNECT_MAX_DELAY_MS = 30_000;

// 把无法解析的数据压缩成一段可控长度的摘要，用于日志输出。
function summarizeUnparseable(data: unknown): string {
  const text = typeof data === "string" ? data : String(data);
  if (text.length <= UNPARSEABLE_LOG_MAX_CHARS) return text;
  return `${text.slice(0, UNPARSEABLE_LOG_MAX_CHARS)}… (truncated, ${text.length} chars total)`;
}

/** WS 客户端身份标识。通过升级 URL 上的 `client_platform` / `client_version` /
 *  `client_os` 查询参数发给服务器——浏览器无法在 WebSocket 握手时设置自定义头，
 *  所以查询参数是唯一可移植的通道。 */
export interface WSClientIdentity {
  platform?: string;
  version?: string;
  os?: string;
}

/**
 * WebSocket 客户端。
 *
 * 负责：连接与 token 鉴权、按事件类型分发消息、断线自动重连（指数退避 + 抖动）、
 * 以及连接恢复后触发已注册的回调。
 */
export class WSClient {
  private ws: WebSocket | null = null;
  private baseUrl: string;
  private token: string | null = null;
  private workspaceSlug: string | null = null;
  private cookieAuth = false;
  private identity: WSClientIdentity | undefined;
  private handlers = new Map<WSEventType, Set<EventHandler>>();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempt = 0;
  private hasConnectedBefore = false;
  // 每次连接只告警一次。一个不合规的帧在一个会话里可能重复数百次，
  // 所以只记录第一次丢弃并抑制后续。每次 connect() 时重置，让新连接再记一次。
  private badFrameLogged = false;
  private onReconnectCallbacks = new Set<() => void>();
  private anyHandlers = new Set<(msg: WSMessage) => void>();
  private logger: Logger;

  /**
   * @param url WebSocket 服务地址（如 wss://...）
   * @param options 可选：logger、是否用 cookie 鉴权、客户端身份标识
   */
  constructor(
    url: string,
    options?: {
      logger?: Logger;
      cookieAuth?: boolean;
      identity?: WSClientIdentity;
    },
  ) {
    this.baseUrl = url;
    this.logger = options?.logger ?? noopLogger;
    this.cookieAuth = options?.cookieAuth ?? false;
    this.identity = options?.identity;
  }

  /** 设置鉴权凭据：token 与当前 workspace slug。 */
  setAuth(token: string | null, workspaceSlug: string) {
    this.token = token;
    this.workspaceSlug = workspaceSlug;
  }

  /** 建立（或重连）WebSocket 连接。 */
  connect() {
    this.badFrameLogged = false;
    const url = new URL(this.baseUrl);
    // token 绝不以 URL 查询参数发送——它会被代理、CDN 和浏览器历史记录日志化。
    // cookie 模式下 HttpOnly cookie 会随升级请求自动带上；token 模式下 token 在
    // 连接建立后作为第一条 WebSocket 消息投递。
    if (this.workspaceSlug)
      url.searchParams.set("workspace_slug", this.workspaceSlug);
    if (this.identity?.platform)
      url.searchParams.set("client_platform", this.identity.platform);
    if (this.identity?.version)
      url.searchParams.set("client_version", this.identity.version);
    if (this.identity?.os)
      url.searchParams.set("client_os", this.identity.os);

    this.ws = new WebSocket(url.toString());

    this.ws.onopen = () => {
      if (!this.cookieAuth && this.token) {
        this.ws!.send(
          JSON.stringify({ type: "auth", payload: { token: this.token } }),
        );
        return;
      }

      this.onAuthenticated();
    };

    this.ws.onmessage = (event) => {
      let msg: WSMessage;
      try {
        msg = JSON.parse(event.data as string) as WSMessage;
      } catch {
        this.logger.warn(
          "ws: received unparseable message",
          summarizeUnparseable(event.data),
        );
        return;
      }
      // 信任边界：帧必须是一个携带字符串 type 的对象。服务器协议对每帧都保证这一点，
      // 但一个不合规帧（代理 / 浏览器扩展注入的越界帧，或裸 JSON 标量）必须在此降级为
      // 空操作。没有这道防线，每个下游消费者（onAny 分发器和每个 ws.on 订阅者）都会在
      // 坏形状上运行；realtime sync 里的 `msg.type.split(...)` 曾从 onmessage 里抛出
      // 未捕获的 TypeError，表现为铺天盖地的全局 `$exception` 事件（MUL-3418）。
      // 所以在边界校验一次，下游就信任这个形状。
      if (!msg || typeof (msg as { type?: unknown }).type !== "string") {
        if (!this.badFrameLogged) {
          this.badFrameLogged = true;
          this.logger.warn(
            "ws: dropping frame without a string type",
            summarizeUnparseable(event.data),
          );
        }
        return;
      }
      if ((msg as any).type === "auth_ack") {
        this.onAuthenticated();
        return;
      }
      this.logger.debug("received", msg.type);
      const eventHandlers = this.handlers.get(msg.type);
      if (eventHandlers) {
        for (const handler of eventHandlers) {
          handler(msg.payload, msg.actor_id, msg.actor_type);
        }
      }
      for (const handler of this.anyHandlers) {
        handler(msg);
      }
    };

    this.ws.onclose = () => {
      this.scheduleReconnect();
    };

    this.ws.onerror = () => {
      // 忽略——onclose 会处理重连；dev 下 StrictMode 双重触发的错误是预期且无害的。
    };
  }

  /**
   * 以指数退避 + 抖动调度一次重连尝试。无限重试（延迟封顶），因为 web/desktop UI
   * 目前还没有可见的断线状态或手动重试入口。
   */
  private scheduleReconnect() {
    const base = Math.min(
      RECONNECT_BASE_DELAY_MS * 2 ** this.reconnectAttempt,
      RECONNECT_MAX_DELAY_MS,
    );
    // ±20% 抖动，让同时断线的客户端不至于整齐划一地同时重连。
    const jitter = base * 0.2 * (Math.random() * 2 - 1);
    const delay = Math.round(
      Math.min(base + jitter, RECONNECT_MAX_DELAY_MS),
    );

    this.reconnectAttempt++;
    this.logger.warn(
      `ws: disconnected, reconnecting in ${delay}ms (attempt ${this.reconnectAttempt})`,
    );
    this.reconnectTimer = setTimeout(() => this.connect(), delay);
  }

  // 鉴权通过后的收尾：重置重连计数，若属于「恢复连接」则触发 onReconnect 回调。
  private onAuthenticated() {
    this.logger.info("connected");
    const recoveredConnection = this.hasConnectedBefore || this.reconnectAttempt > 0;
    this.reconnectAttempt = 0;
    if (recoveredConnection) {
      for (const cb of this.onReconnectCallbacks) {
        try {
          cb();
        } catch {
          // 单个回调抛错不应中断其余回调的重连通知
        }
      }
    }
    this.hasConnectedBefore = true;
  }

  /** 主动断开：清空定时器、移除处理器，阻止 onclose 再触发重连。 */
  disconnect() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      // 关闭前先摘掉处理器，防止 onclose 又调度一次重连
      this.ws.onclose = null;
      this.ws.onerror = null;
      this.ws.close();
      this.ws = null;
    }
    this.hasConnectedBefore = false;
    this.reconnectAttempt = 0;
    this.handlers.clear();
    this.anyHandlers.clear();
    this.onReconnectCallbacks.clear();
  }

  /** 订阅某个事件类型；返回一个取消订阅的函数。 */
  on(event: WSEventType, handler: EventHandler) {
    if (!this.handlers.has(event)) {
      this.handlers.set(event, new Set());
    }
    this.handlers.get(event)!.add(handler);
    return () => {
      this.handlers.get(event)?.delete(handler);
    };
  }

  /** 订阅所有事件；返回一个取消订阅的函数。 */
  onAny(handler: (msg: WSMessage) => void) {
    this.anyHandlers.add(handler);
    return () => {
      this.anyHandlers.delete(handler);
    };
  }

  /** 注册断线重连成功后的回调；返回一个取消注册的函数。 */
  onReconnect(callback: () => void) {
    this.onReconnectCallbacks.add(callback);
    return () => {
      this.onReconnectCallbacks.delete(callback);
    };
  }

  /** 发送一条消息（仅当连接处于 OPEN 状态时才会真正发出）。 */
  send(message: WSMessage) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(message));
    }
  }
}

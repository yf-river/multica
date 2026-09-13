import { api } from "@multica/core/api";

/**
 * plugins（插件）模块：宿主一侧的桥。
 *
 * 插件 surface（iframe 内的插件文档）提出请求，宿主（本模块）代表登录用户
 * 自己的会话执行调用并把结果返回。插件本身不持有任何凭据，所以这是插件
 * surface 进入 Multica 的唯一通道。
 *
 * 身份由三样东西共同绑定：预期的外层 frame 窗口、一次性 launch challenge、
 * 以及访客创建的 MessagePort。内层插件 frame 因 sandbox 而不透明，所以
 * origin 对判断它没有用处。
 */

const BRIDGE_PROTOCOL_VERSION = 2;
const BRIDGE_CONNECT_MESSAGE = "multica:plugin-bridge-connect";

type BridgeMethod = "GET" | "POST" | "PATCH" | "PUT" | "DELETE";

type BridgeRequest =
  | { id: string; kind: "action"; method: BridgeMethod; path: string; body?: unknown }
  | { id: string; kind: "ui.resize"; height: number };

/** surface 可以命名的路径白名单。其余路径在到达 fetch 之前就被拒绝。 */
const ALLOWED_PATHS: RegExp[] = [
  /^\/context$/,
  /^\/issues\/[^/]+$/,
  /^\/issues\/[^/]+\/comments$/,
  /^\/storage\/(workspace|user)$/,
  /^\/storage\/(workspace|user)\/[^/]+$/,
  // surface 调用它自己插件的 hook：即 `ui` 触发器。服务端仍会校验 manifest
  // 确实声明过该 hook，而且安装实例就是本桥为之创建的那个——surface 不能
  // 在这里写出别的插件的 hook 名来调用它。
  /^\/hooks\/[^/]+$/,
];

const MAX_RESIZE_PX = 4000;

function isAllowedPath(path: string): boolean {
  return ALLOWED_PATHS.some((pattern) => pattern.test(path));
}

const ALLOWED_METHODS = new Set(["GET", "POST", "PATCH", "PUT", "DELETE"]);

function isBridgeRequest(value: unknown): value is BridgeRequest {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate.id !== "string") return false;
  if (candidate.kind === "ui.resize") return typeof candidate.height === "number";
  return (
    candidate.kind === "action" &&
    typeof candidate.path === "string" &&
    // 白名单而非「是个字符串」：任意动词会一路走到 fetch，只能靠服务端的
    // 405 来拦它。
    typeof candidate.method === "string" && ALLOWED_METHODS.has(candidate.method)
  );
}

/** 创建宿主桥所需的参数。 */
export interface SurfaceBridgeOptions {
  installationId: string;
  /** 一次性凭据，只在携带这份托管文档 URL 时有效。 */
  bridgeToken: string;
  /** 挂载在哪个 issue 上；会转发给 /context，让 surface 知道自己的位置。 */
  issueId?: string;
  /** surface 请求改变自身高度时的回调。 */
  onResize?: (height: number) => void;
}

/** 宿主桥暴露给调用方的接口。 */
export interface SurfaceBridge {
  /**
   * 在受信任的外层文档被赋值之前先挂上监听器。信道由访客创建，宿主只在
   * 所有 launch 证明都匹配时才接受它的 port。
   */
  connect(frame: HTMLIFrameElement, theme: Record<string, string>): void;
  /** 向已经连接的 frame 推送一次主题变更。 */
  pushTheme(theme: Record<string, string>): void;
  /** 关闭桥并清理监听器与端口。 */
  close(): void;
}

/** 建立一个宿主侧桥实例；详见文件顶部说明。 */
export function createSurfaceBridge(options: SurfaceBridgeOptions): SurfaceBridge {
  let port: MessagePort | null = null;
  let closed = false;
  let connected = false;
  const connectListeners: Array<(event: MessageEvent) => void> = [];

  const handle = async (request: BridgeRequest, port: MessagePort) => {
    if (request.kind === "ui.resize") {
      // 限幅：surface 想要一千万像素高的 frame，要么是 bug，要么是想把页面
      // 其余部分挤出视口。
      options.onResize?.(Math.min(Math.max(0, request.height), MAX_RESIZE_PX));
      return;
    }

    if (!isAllowedPath(request.path)) {
      port.postMessage({ id: request.id, ok: false, status: 400, error: `unsupported path ${request.path}` });
      return;
    }

    try {
      const result = await api.callPluginAction(options.installationId, {
        method: request.method,
        path: request.path,
        body: request.body,
        issueId: options.issueId,
      });
      port.postMessage({ id: request.id, ok: true, status: 200, data: result });
    } catch (error) {
      // 状态码对 surface 有意义：403 意味着「管理员没授予这个权限」，这对
      // 插件作者来说和 404 是完全不同的修法。
      const status = typeof (error as { status?: number })?.status === "number" ? (error as { status: number }).status : 500;
      const message = error instanceof Error ? error.message : "plugin action failed";
      port.postMessage({ id: request.id, ok: false, status, error: message });
    }
  };

  return {
    connect(frame, theme) {
      if (closed) return;
      const onConnect = (event: MessageEvent) => {
        const data = event.data as { type?: string; version?: number; challenge?: string } | null;
        if (data?.type !== BRIDGE_CONNECT_MESSAGE) return;
        // 外层 frame 是受信任的宿主代码。它只在自己的 source、协议与
        // challenge 检查都通过后，才把内层 port 转发上来。
        if (!frame.contentWindow || event.source !== frame.contentWindow) return;
        if (data.version !== BRIDGE_PROTOCOL_VERSION || data.challenge !== options.bridgeToken) return;
        const guestPort = event.ports?.[0];
        if (!guestPort || closed || connected) return;

        connected = true;
        window.removeEventListener("message", onConnect);
        port = guestPort;
        port.onmessage = (portEvent) => {
          if (!isBridgeRequest(portEvent.data)) return;
          void handle(portEvent.data, guestPort);
        };
        port.start();
        port.postMessage({ kind: "theme", theme });
      };
      connectListeners.push(onConnect);
      window.addEventListener("message", onConnect);
    },
    pushTheme(theme) {
      port?.postMessage({ kind: "theme", theme });
    },
    close() {
      closed = true;
      for (const listener of connectListeners.splice(0)) window.removeEventListener("message", listener);
      port?.close();
      port = null;
    },
  };
}

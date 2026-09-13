/**
 * plugins（插件）模块：构造插件 surface 的「受信任外层文档」。
 *
 * 通过桥转发设计令牌使插件页面看起来原生，并生成包裹单个不受信任插件文档的
 * 宿主侧 HTML（含严格 CSP 与外层/内层双层 frame 隔离）。也负责把主题变量
 * 从 DOM 读成令牌表。
 */

/** 通过桥转发给 surface 的设计令牌名——有了它们，插件界面才显得原生。 */
export const SURFACE_THEME_TOKENS = [
  "--background",
  "--foreground",
  "--muted",
  "--muted-foreground",
  "--border",
  "--primary",
  "--primary-foreground",
  "--destructive",
  "--radius",
  "--text-caption",
  "--text-body",
] as const;

/** 桥的握手消息类型常量（与 surface-bridge.ts 对应）。 */
export const SURFACE_BRIDGE_CONNECT_MESSAGE = "multica:plugin-bridge-connect";
/** 桥协议版本号（与 surface-bridge.ts 对应）。 */
export const SURFACE_BRIDGE_PROTOCOL_VERSION = 2;

/** 从指定元素的计算样式里读出已定义的令牌，供推送主题使用。 */
export function readThemeTokens(element: Element | null): Record<string, string> {
  if (!element || typeof getComputedStyle !== "function") return {};
  const computed = getComputedStyle(element);
  const tokens: Record<string, string> = {};
  for (const name of SURFACE_THEME_TOKENS) {
    const value = computed.getPropertyValue(name).trim();
    if (value) tokens[name] = value;
  }
  return tokens;
}

/** 把字符串转义成可在 HTML 属性里安全使用的形式。 */
function escapeAttribute(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/** UTF-8 安全的 base64 编码（原生 btoa 只能吃 Latin-1）。 */
function encodeUTF8(value: string): string {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let index = 0; index < bytes.length; index += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000));
  }
  return btoa(binary);
}

/** 构造外层 frame 文档所需的输入。 */
export interface SurfaceFrameDocumentInput {
  /** Multica 专属无 cookie 内容域上的短期 URL。 */
  url: string;
  /** 同时嵌入两份受信任文档的一次性凭据——绝不进入插件代码。 */
  bridgeToken: string;
}

/**
 * 为单个不受信任的插件文档构造受信任的外层 frame 文档。
 *
 * 外层 frame 存在是为了让它的 `frame-src` 只作用于这一个插件。整页范围的安全
 * 策略还得放行所有附件预览域名；这个更宽的白名单会让恶意的 surface 导航到
 * 那些域名。这里内层 frame 允许的网络跳转只能是回到提供它的那个 Multica
 * 内容域。
 *
 * 外层 frame 由宿主编写、与应用同源。内层 frame 仍然只有
 * `sandbox="allow-scripts"`、没有 `allow-same-origin`，所以两个插件各自得到
 * 互不透明的 origin，无法互相检查对方。
 */
export function buildSurfaceFrameDocument({ url, bridgeToken }: SurfaceFrameDocumentInput): string {
  const parsed = new URL(url);
  if ((parsed.protocol !== "https:" && parsed.protocol !== "http:") || !bridgeToken) {
    throw new Error("invalid plugin surface launch");
  }

  const config = encodeUTF8(JSON.stringify({ url: parsed.toString(), bridgeToken }));
  const csp = [
    "default-src 'none'",
    "script-src 'unsafe-inline'",
    "style-src 'unsafe-inline'",
    `frame-src ${parsed.origin}`,
    "connect-src 'none'",
    "img-src 'none'",
    "font-src 'none'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
  ].join("; ");

  return `<!doctype html>
<html>
<head>
<meta http-equiv="Content-Security-Policy" content="${escapeAttribute(csp)}">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
html, body, #multica-plugin-root, iframe { width: 100%; height: 100%; margin: 0; padding: 0; border: 0; }
body { overflow: hidden; background: transparent; }
</style>
</head>
<body>
<div id="multica-plugin-root"></div>
<script>
(function () {
  var encoded = ${JSON.stringify(config)};
  var binary = atob(encoded);
  var bytes = new Uint8Array(binary.length);
  for (var index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  var config = JSON.parse(new TextDecoder().decode(bytes));
  var child = document.createElement("iframe");
  child.title = "Plugin content";
  child.setAttribute("sandbox", "allow-scripts");

  var state = "launching";
  var loadCount = 0;

  function stopSurface(type) {
    if (state === "terminal") return;
    state = "terminal";
    // 连接建立后，port 的所有权已转移给宿主；这个终态信号让宿主关闭桥并
    // 卸载本包裹层。
    parent.postMessage({ type: type }, "*");
  }

  document.addEventListener("securitypolicyviolation", function (event) {
    if (event.effectiveDirective === "frame-src" || event.effectiveDirective === "child-src") {
      stopSurface("multica:plugin-surface-navigation-blocked");
    }
  });

  child.addEventListener("load", function () {
    loadCount += 1;
    if (loadCount > 1) stopSurface("multica:plugin-surface-navigated");
  });

  window.addEventListener("message", function (event) {
    if (!child.contentWindow || event.source !== child.contentWindow) return;
    var data = event.data || {};
    if (data.type === "multica:plugin-surface-error" || data.type === "multica:plugin-surface-navigated") {
      stopSurface(data.type);
      return;
    }
    if (data.type !== ${JSON.stringify(SURFACE_BRIDGE_CONNECT_MESSAGE)} ||
        data.version !== ${SURFACE_BRIDGE_PROTOCOL_VERSION} ||
        data.challenge !== config.bridgeToken || state !== "launching" || !event.ports[0]) return;

    state = "connected";
    parent.postMessage({
      type: ${JSON.stringify(SURFACE_BRIDGE_CONNECT_MESSAGE)},
      version: ${SURFACE_BRIDGE_PROTOCOL_VERSION},
      challenge: config.bridgeToken
    }, "*", [event.ports[0]]);
    config.bridgeToken = "";
  });

  child.src = config.url;
  config.url = "";
  document.getElementById("multica-plugin-root").appendChild(child);
})();
</script>
</body>
</html>`;
}

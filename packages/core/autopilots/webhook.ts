// 本文件提供自动驾驶 webhook 触发器 URL 的拼装与脱敏展示工具。

import type { AutopilotTrigger } from "../types";

/**
 * 拼装一个 webhook 触发器可用的绝对 webhook URL。
 *
 * 解析顺序：
 *  1. trigger.webhook_url——仅当服务端设置了 MULTICA_PUBLIC_URL 时存在。
 *     这是可用时的权威形式。
 *  2. apiBaseUrl + webhook_path——分离源客户端和自托管场景，服务端没有铸出
 *     绝对 URL，但客户端知道自己的 API origin。
 *  3. currentOrigin + webhook_path——当 getBaseUrl() 为空时的浏览器兜底
 *     （例如同源 Next.js 开发）。
 *
 * 当触发器还没有 token / path（尚未写回缓存的新触发器，或非 webhook 触发器）时返回 null。
 *
 * @param params - 触发器字段、API 基地址与当前 origin
 * @returns 可用的绝对 webhook URL，否则 null
 */
export function buildAutopilotWebhookUrl(params: {
  trigger: Pick<AutopilotTrigger, "kind" | "webhook_token" | "webhook_path" | "webhook_url">;
  apiBaseUrl?: string;
  currentOrigin?: string;
}): string | null {
  const { trigger, apiBaseUrl, currentOrigin } = params;

  if (trigger.kind !== "webhook") return null;

  if (typeof trigger.webhook_url === "string" && trigger.webhook_url) {
    return trigger.webhook_url;
  }

  const path =
    (typeof trigger.webhook_path === "string" && trigger.webhook_path) ||
    (trigger.webhook_token ? `/api/webhooks/autopilots/${trigger.webhook_token}` : null);
  if (!path) return null;

  const base = stripTrailingSlash(apiBaseUrl) || stripTrailingSlash(currentOrigin);
  if (!base) return path; // 兜底——相对路径在浏览器内依然可用
  return base + path;
}

function stripTrailingSlash(s: string | undefined): string {
  if (!s) return "";
  return s.endsWith("/") ? s.slice(0, -1) : s;
}

/** 固定宽度的掩码——从不从 token 推导长度，因此掩码不泄露任何长度信息。 */
const WEBHOOK_URL_MASK = "••••••••••••";

/**
 * 把 webhook URL 的秘密部分打码用于展示。
 *
 * 只有末尾的 token 段是凭据：任何持有它的人都能触发该自动驾驶。origin 和
 * `/api/webhooks/autopilots/` 前缀不携带秘密，所以保持可读，让值仍能被识别为
 * 这个触发器的 URL，同时隐藏秘密。当 URL 没有可分离的末段时，回退为裸掩码。
 *
 * @param url - 完整 webhook URL
 * @returns 打码后的 URL
 */
export function maskAutopilotWebhookUrl(url: string): string {
  const cut = url.lastIndexOf("/");
  if (cut < 0 || cut === url.length - 1) return WEBHOOK_URL_MASK;
  return url.slice(0, cut + 1) + WEBHOOK_URL_MASK;
}

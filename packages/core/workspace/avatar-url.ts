// 本文件提供「公开文件 URL」的解析：把服务端下发的相对路径（如头像地址）
// 结合 API 基地址解析成可加载的绝对 URL。

import { api } from "../api";

/**
 * 以给定的基地址解析公开文件 URL：相对路径（以 / 开头）会拼上基地址，
 * 已是绝对 URL 的原样返回。
 *
 * @param rawUrl - 原始 URL（可为空）
 * @param baseUrl - 基地址（例如 API origin）
 * @returns 可用的绝对 URL；无原始 URL 时返回 null
 */
export function resolvePublicFileUrlWithBase(rawUrl: string | null | undefined, baseUrl: string): string | null {
  if (!rawUrl) return null;
  if (!rawUrl.startsWith("/")) return rawUrl;
  const trimmedBaseUrl = baseUrl.replace(/\/+$/, "");
  return `${trimmedBaseUrl}${rawUrl}`;
}

/** 以当前 API 基地址解析公开文件 URL（见 resolvePublicFileUrlWithBase）。 */
export function resolvePublicFileUrl(rawUrl: string | null | undefined): string | null {
  return resolvePublicFileUrlWithBase(rawUrl, api.getBaseUrl());
}

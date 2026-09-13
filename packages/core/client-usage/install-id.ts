// 本文件负责安装级 ID 的读取与生成（幂等，仅当存储中无合法 UUID 时才创建）。

import type { StorageAdapter } from "../types/storage";

const INSTALL_ID_KEY = "multica_install_id";
const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function randomUUID(): string {
  if (typeof globalThis.crypto?.randomUUID === "function") {
    return globalThis.crypto.randomUUID();
  }
  const bytes = new Uint8Array(16);
  globalThis.crypto.getRandomValues(bytes);
  bytes[6] = (bytes[6]! & 0x0f) | 0x40;
  bytes[8] = (bytes[8]! & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0"));
  return `${hex.slice(0, 4).join("")}-${hex.slice(4, 6).join("")}-${hex.slice(6, 8).join("")}-${hex.slice(8, 10).join("")}-${hex.slice(10).join("")}`;
}

/**
 * 返回安装级 ID，仅当存储中没有合法 UUID 时才创建并写回。
 *
 * @param storage - 存储适配器
 * @param generate - 生成 UUID 的函数（默认用 crypto）
 * @returns 小写形式的安装 ID
 */
export function getOrCreateInstallId(
  storage: StorageAdapter,
  generate: () => string = randomUUID,
): string {
  const existing = storage.getItem(INSTALL_ID_KEY);
  if (existing && UUID_PATTERN.test(existing)) return existing.toLowerCase();

  const created = generate().toLowerCase();
  if (!UUID_PATTERN.test(created)) {
    throw new Error("install id generator returned an invalid UUID");
  }
  storage.setItem(INSTALL_ID_KEY, created);
  return created;
}

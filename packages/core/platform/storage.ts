import type { StorageAdapter } from "../types/storage";

/** SSR-safe localStorage. Works in both Next.js (SSR) and Electron (always client). */
export const defaultStorage: StorageAdapter = {
  getItem: (k) =>
    typeof window !== "undefined" ? localStorage.getItem(k) : null,
  setItem: (k, v) => {
    if (typeof window !== "undefined") localStorage.setItem(k, v);
  },
  removeItem: (k) => {
    if (typeof window !== "undefined") localStorage.removeItem(k);
  },
  keys: () => storageKeys("localStorage"),
};

/** SSR-safe sessionStorage for account-owned, tab-lifetime drafts. */
export const defaultSessionStorage: StorageAdapter = {
  getItem: (k) =>
    typeof window !== "undefined" ? sessionStorage.getItem(k) : null,
  setItem: (k, v) => {
    if (typeof window !== "undefined") sessionStorage.setItem(k, v);
  },
  removeItem: (k) => {
    if (typeof window !== "undefined") sessionStorage.removeItem(k);
  },
  keys: () => storageKeys("sessionStorage"),
};

function storageKeys(kind: "localStorage" | "sessionStorage"): string[] {
  if (typeof window === "undefined") return [];
  const storage = window[kind];
  const keys: string[] = [];
  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (key !== null) keys.push(key);
  }
  return keys;
}

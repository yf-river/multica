import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

// 下面全是给 jsdom 打补丁。纯逻辑套件会用 `// @vitest-environment node` 退出 jsdom
// 并共用本文件——那种情况下根本没有 DOM 可补，所以直接跳出而不是给每个 stub 加守卫。
if (typeof window !== "undefined") {
  // jsdom 不提供 ResizeObserver；给响应式组件打个空实现桩。
  if (typeof globalThis.ResizeObserver === "undefined") {
    globalThis.ResizeObserver = class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  }

  // jsdom 29 / Node.js 22+ 可能没有可用的 Web Storage API。
  // 缺方法时建一个正经的 localStorage mock。
  if (
    typeof globalThis.localStorage === "undefined" ||
    typeof globalThis.localStorage.getItem !== "function"
  ) {
    const store: Record<string, string> = {};
    const localStorageMock = {
      getItem: vi.fn((key: string) => store[key] ?? null),
      setItem: vi.fn((key: string, value: string) => {
        store[key] = value;
      }),
      removeItem: vi.fn((key: string) => {
        delete store[key];
      }),
      clear: vi.fn(() => {
        for (const key of Object.keys(store)) {
          delete store[key];
        }
      }),
      get length() {
        return Object.keys(store).length;
      },
      key: vi.fn((index: number) => Object.keys(store)[index] ?? null),
    };
    Object.defineProperty(globalThis, "localStorage", {
      value: localStorageMock,
      writable: true,
    });
  }
}

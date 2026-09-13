"use client";

import { useEffect } from "react";
import { captureException } from "@multica/core/analytics";

/**
 * 应用级错误边界：负责兜底整个 Web 应用未被下层捕获的错误，避免白屏。
 *
 * 当错误一路逃逸到最外层时，Next.js 会用本组件替换根布局来渲染——也就是整页白屏的场景。
 * React 会在这些错误到达 window.onerror 之前就把它们拦下来，因此 posthog-js 的自动异常捕获
 * 根本看不到它们，我们才在这里显式上报。局部（section 级）的失败由 `@multica/ui` 的
 * ErrorBoundary 就地处理，不会一路冒泡到这里。
 *
 * @param error - 抛出的错误对象；Next.js 会附带一个 digest 字符串用于服务端日志关联。
 * @param reset - 由 Next.js 注入的重试函数，点击后尝试重新渲染出错页面。
 * @returns 全屏错误提示页。
 */
export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    captureException(error, { source: "global-error", digest: error.digest });
  }, [error]);

  return (
    <html>
      <body
        style={{
          display: "flex",
          minHeight: "100vh",
          alignItems: "center",
          justifyContent: "center",
          fontFamily: "system-ui, sans-serif",
        }}
      >
        <div style={{ maxWidth: 420, textAlign: "center" }}>
          <h1 style={{ fontSize: 18, fontWeight: 600 }}>发生错误</h1>
          <p style={{ marginTop: 8, color: "#666" }}>
            The page hit an unexpected error. Try reloading.
          </p>
          <button
            type="button"
            onClick={reset}
            style={{
              marginTop: 16,
              padding: "8px 16px",
              borderRadius: 6,
              border: "1px solid #ccc",
              cursor: "pointer",
            }}
          >
            Reload
          </button>
        </div>
      </body>
    </html>
  );
}

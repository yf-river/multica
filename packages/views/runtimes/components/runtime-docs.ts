// ============================================================================
// runtimes 相关帮助文档链接（纯常量函数）
//
// 角色定位：集中维护指向官网文档（multica.ai/docs）的链接，供 UI 里的「查看
// 文档」类入口使用。文档 URL 含锚点时一律先做 encodeURIComponent，保证中文
// 锚点在浏览器地址栏里合法可用。
// ============================================================================

// 「daemon 与运行时间」文档的根链接，其它文档链接从这里派生的基准页。
export function daemonRuntimesDocsHref(): string {
  return "https://multica.ai/docs/zh/daemon-runtimes";
}

// 「自定义运行时配置」一节的直达链接：在 daemon-runtimes 页基础上拼上中文
// 锚点。锚点经 URL 编码，中文标题才能被浏览器正确解析定位。
export function customRuntimeDocsHref(): string {
  return `${daemonRuntimesDocsHref()}#${encodeURIComponent("自定义运行时配置")}`;
}

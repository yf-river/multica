/**
 * Web 应用浏览器标签页标题的约定。
 *
 * 有两个写入方且必须保持一致：静态已知路由（落地页、登录等）由 Next.js metadata
 * 渲染 `<title>`；工作区路由的名字只存在于查询缓存里，由 `WorkspaceDocumentTitle`
 * 直接写 document.title。两边都经过这里的常量，确保 `Issues | Multica` 与
 * `Changelog | Multica` 永远不会在分隔符上漂移成两种写法。
 *
 * 刻意保持纯净、不依赖 React：根布局是服务端组件，imports SITE_TITLE /
 * TITLE_TEMPLATE 供 metadata 导出使用。
 */

/** 根级兜底 —— 页面没有更具体内容可说时的标题。 */
export const SITE_TITLE = "Multica — Project Management for Human + Agent Teams";

/** 追加到每个页面标题后面。 */
export const TITLE_SUFFIX = " | Multica";

/** Next.js `metadata.title.template`；见 apps/web/app/layout.tsx。 */
export const TITLE_TEMPLATE = `%s${TITLE_SUFFIX}`;

/**
 * 放进后缀前的页面名最长长度（按码点计）。
 *
 * issue 标题在服务端是无长度上限的，而整串会进窗口标题、历史条目和书签——不止标签页。
 * 一个 600 字符的标题会让这三者都不可读。浏览器对标签页本身的截断远比这个早，
 * 所以这个上限只为了让那些次要展示面保持可读；标题里最有区分度的部分（issue key）
 * 排在最前，两种截断下都总是幸存。
 */
export const MAX_PAGE_TITLE_LENGTH = 120;

/**
 * 截断到 {@link MAX_PAGE_TITLE_LENGTH} 个码点。
 *
 * 按码点遍历而非按 UTF-16 单元切片，可以避免把一个 emoji 或其它星面字符拦腰截断
 * 成替换符——这是这里真实发生过的场景，issue 标题常常以 emoji 开头。
 */
function clipTitle(title: string): string {
  const codePoints = Array.from(title);
  if (codePoints.length <= MAX_PAGE_TITLE_LENGTH) return title;
  return `${codePoints.slice(0, MAX_PAGE_TITLE_LENGTH).join("").trimEnd()}…`;
}

/**
 * 为某个页面名拼出完整文档标题，例如 `MUL-123: Fix login` → `MUL-123: Fix login | Multica`。
 *
 * 页面名为空或纯空白时退回 {@link SITE_TITLE}，而不是渲染一个光秃秃的 ` | Multica`。
 *
 * @param pageTitle - 页面名。
 * @returns 完整文档标题。
 */
export function formatDocumentTitle(pageTitle: string | null | undefined, environmentName = ""): string {
  const trimmed = pageTitle?.trim();
  const title = trimmed ? `${clipTitle(trimmed)}${TITLE_SUFFIX}` : SITE_TITLE;
  return environmentName ? `[${environmentName}] ${title}` : title;
}

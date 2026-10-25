"use client";

import { useConfigStore } from "@multica/core/config";
import { useEffect, useMemo, useRef } from "react";
import { usePathname, useSearchParams } from "next/navigation";
import { parseTabSubject } from "@multica/core/paths";
import { useTabPresentation } from "@multica/views/layout";
import { formatDocumentTitle } from "./document-title";

/**
 * 把浏览器标签页标题设成工作区路由当前打开的内容，例如 `MUL-123: Fix login | Multica`
 * （MUL-6222）。不做的话，每个开着的 dashboard 标签页都只会显示根 metadata 标题，
 * 并排看多个 issue 时根本无法区分，直到逐个点进去。
 *
 * URL → 标题的解析不归本组件管：它用的是与「工作区文档命名」相同的纯解析器 +
 * 缓存型展示 hook，所以同一个 URL 的浏览器标签页与深链文档读起来始终一致、按用户语言
 * 展示；重命名的项目或改名的 issue 也会实时改掉标签标题。背后每个查询都只走缓存，
 * 因此本组件不会带来额外请求。
 *
 * 每个 dashboard 布局只挂载一次——整棵树只有一个写入方。某页面想要不同标题，应当
 * 让 resolveTabPresentation 认识它的 subject，而不是绕过本组件去直接写 document.title。
 */
export function WorkspaceDocumentTitle() {
  const environmentName = useConfigStore((state) => state.environmentName);
  const pathname = usePathname();
  const searchParams = useSearchParams();

  // 收件箱与聊天把当前选中项放在查询串里，subject 解析器也会读它，
  // 所以标题必须基于「完整 URL」重新计算。
  const search = searchParams.toString();
  const url = search ? `${pathname}?${search}` : pathname;

  const subject = useMemo(() => parseTabSubject(url), [url]);
  const { title } = useTabPresentation(url);

  // 未识别的 URL 落到站点标题，而不是标签条上需要的「未知页面」标签：
  // 浏览器标签页没有别的地方展示产品名。
  const documentTitle =
    formatDocumentTitle(subject.kind === "unknown" ? undefined : title, environmentName);

  const documentTitleRef = useRef(documentTitle);
  documentTitleRef.current = documentTitle;

  // 虽然真正写入的是 documentTitle，但依赖里必须带 url：app router 在导航后会
  // 用目标路由的 metadata 重新渲染并重置 document.title 为根默认值，所以两次
  // 恰好同标题的路由之间移动（/inbox → /inbox?view=archived）也得重新断言一次。
  useEffect(() => {
    const apply = () => {
      if (document.title !== documentTitleRef.current) document.title = documentTitleRef.current;
    };
    apply();
    const observer = new MutationObserver(apply);
    observer.observe(document.head, { childList: true, subtree: true, characterData: true });
    return () => observer.disconnect();
  }, [documentTitle, url]);

  // 整体离开 dashboard（登出、切换工作区、回落地页）时把标题重置回站点名，
  // 免得过期的 issue 名比页面活得更久。
  useEffect(() => {
    return () => {
      document.title = formatDocumentTitle(undefined, environmentName);
    };
  }, [environmentName]);

  return null;
}

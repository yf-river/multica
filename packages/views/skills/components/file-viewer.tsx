// 本文件职责：渲染单个 skill 文件的正文——markdown 文件默认走「预览」态（剥掉
// frontmatter、以富文本渲染），也可切到「原文」态编辑；非 markdown 文件只有原文态。
// 本组件被页面当作文件树旁的「正文区」使用。
"use client";

import { useEffect, useMemo, useRef } from "react";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { parseFrontmatter } from "@multica/core/skills/frontmatter";
import { RichContent } from "../../rich-content";
import { useT } from "../../i18n";

/**
 * 选中文件的正文以哪种形态渲染：预览（富文本）还是原文（编辑）。
 *
 * 该状态由页面持有而非本组件：它是「用户想怎么干活」的属性，跟当前打开哪个文件无关
 * ——切换文件时不能悄悄把用户弹回预览态。
 */
export type FileMode = "preview" | "raw";

/** 判断某个文件路径是否是 markdown（.md / .mdx）。 */
export function isMarkdownPath(path: string) {
  return path.endsWith(".md") || path.endsWith(".mdx");
}

/**
 * 单个 skill 文件的正文区。
 *
 * 预览时会剥掉 frontmatter，因为 `name` / `description` 已经在 Overview 标签页作为
 * 一等属性展示过了——这里再渲染一遍就成了同一份数据的第三份拷贝。
 */
export function FileViewer({
  path,
  content,
  mode,
  readOnly,
  autoFocus,
  onChange,
  onFocusHandled,
}: {
  path: string;
  content: string;
  mode: FileMode;
  readOnly: boolean;
  /** 「编辑」动作置位：等该文件的编辑器渲染好后把光标放进去。 */
  autoFocus?: boolean;
  onChange: (content: string) => void;
  /** 光标落位后回调一次，保证这个请求不会被重放。 */
  onFocusHandled?: () => void;
}) {
  const { t } = useT("skills");
  const isMd = isMarkdownPath(path);
  const editorRef = useRef<HTMLTextAreaElement>(null);

  const body = useMemo(
    () => (isMd ? parseFrontmatter(content).body : content),
    [content, isMd],
  );

  // 调用方会在「置位该标记」的同一次更新里把模式切成 raw，所以本 effect 跑起来时
  // 下方的 textarea 一定已挂载。由页面自己发起一次「请求式聚焦」而不是在元素上用
  // `autoFocus`，才能让「编辑」作用于已经打开着的文件：那条路径不换 key、不挂载
  // 任何新元素，挂载时的一次性聚焦永远不会再触发。
  useEffect(() => {
    if (autoFocus !== true) return;
    const el = editorRef.current;
    if (!el) return;
    el.focus();
    // 光标落在文件头部。跳到一份几千字符的参考文件末尾，会把用户刚点的那部分内容
    // 滚出视野。
    el.setSelectionRange(0, 0);
    onFocusHandled?.();
  }, [autoFocus, onFocusHandled]);

  // 非 markdown 文件没有可预览的内容，永远走原文态。
  if (isMd && mode === "preview") {
    return (
      <div className="h-full overflow-y-auto">
        {/* pb-24：给浮动的「保存」胶囊底下留出空间，让最后几行不至于被遮住。 */}
        <div className="mx-auto max-w-[68ch] px-6 pb-24 pt-7 sm:px-8">
          <RichContent
            content={body || t(($) => $.file_viewer.no_content)}
            density="document"
            phase="settled"
          />
        </div>
      </div>
    );
  }

  return (
    <Textarea
      ref={editorRef}
      value={content}
      readOnly={readOnly}
      onChange={(e) => onChange(e.target.value)}
      aria-label={t(($) => $.file_viewer.raw_aria, { path })}
      placeholder={
        isMd
          ? t(($) => $.file_viewer.markdown_placeholder)
          : t(($) => $.file_viewer.raw_placeholder)
      }
      className="h-full min-h-full resize-none rounded-none border-0 px-6 pb-24 pt-5 font-mono text-body leading-relaxed read-only:cursor-default focus-visible:ring-0 sm:px-8"
    />
  );
}

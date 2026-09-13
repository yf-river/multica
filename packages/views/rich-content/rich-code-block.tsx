"use client";

/**
 * 富文本模块：RichCodeBlock——产品里唯一的围栏代码分发器（MUL-4922）。
 *
 * 每个产品界面（Chat、issue 描述、评论）都通过这个文件渲染围栏代码。在
 * 别处加语言分支——比如消息列表或评论卡片里写 `if (lang === "…")`——正是
 * 这次清理要删除的漂移，所以新语言只加在这里、别处不加。
 *
 * 分发基于完整的语言 token，从不做子串匹配：`language-htmlbars` 和
 * `language-mermaidx` 是普通代码，不是 HTML 预览 / 图表。
 *
 * 升级成富块还要求围栏是*闭合*的（见 streaming-fence.ts）。流式传输中未
 * 完成的围栏渲染为纯源码，这样 Mermaid 永远不会解析半张图，仍在到达的
 * HTML 也不会创建 iframe。
 *
 * 叶子组件（MermaidDiagram / HtmlBlockPreview / lowlight 静态代码）与界面
 * 无关，并与 Tiptap 编辑器的 NodeViews 共用。它们按直接路径导入——绝不
 * 通过 `editor` 桶导入——这样本模块不会把编辑器的 Tiptap 图拉进 Chat。
 */

import { memo, useEffect, useMemo, useState, type ReactNode } from "react";
import { toHtml } from "hast-util-to-html";
import { Check, Copy } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../i18n";
import {
  MermaidDiagram,
  MERMAID_SKELETON_HEIGHT_PX,
  reservedMermaidHeightPx,
} from "../editor/mermaid-diagram";
import {
  HtmlBlockPreview,
  HTML_BLOCK_PREVIEW_HEIGHT_PX,
} from "../editor/html-block-preview";
import { highlightCode } from "../editor/syntax-highlight";
import { LazyRichBlock } from "./lazy-rich-block";

/**
 * 可能成为富块的代码语言。其他语言——包括未知和无语言——都渲染为静态
 * 高亮代码。
 */
export type RichFenceLanguage = "mermaid" | "html";

/**
 * 判断某个语言是否属于富块语言。
 * @param language 语言 token。
 */
export function isRichFenceLanguage(
  language: string | undefined,
): language is RichFenceLanguage {
  return language === "mermaid" || language === "html";
}

/**
 * 判断一个围栏块是否应渲染为富块而不是源码。
 * 两个条件缺一不可：语言支持富块，且围栏已闭合。
 * @param language 语言 token。
 * @param isFenceClosed 围栏是否已闭合。
 */
export function shouldUpgradeFence(
  language: string | undefined,
  isFenceClosed: boolean,
): boolean {
  return isRichFenceLanguage(language) && isFenceClosed;
}

// 按源码 memo，这样在流式消息的别处追加文字时，不会重跑 Mermaid 的异步
// 渲染，也不会重载已经挂载的 iframe。React 协调会保持实例挂载（同样的
// 元素类型、同样的位置）；memo 进一步阻止它重渲染。
const MemoMermaidDiagram = memo(MermaidDiagram);
const MemoHtmlBlockPreview = memo(HtmlBlockPreview);

/**
 * 静态的 lowlight 高亮 `<code>`，与可编辑的 Tiptap 代码块使用同一引擎和
 * `.hljs-*` CSS，让围栏在每个界面上看起来一模一样。
 * @param language 语言 token。
 * @param body 代码文本。
 * @param className 附加样式类名。
 */
export function StaticCodeBody({
  language,
  body,
  className,
}: {
  language: string | undefined;
  body: string;
  className?: string;
}) {
  const html = useMemo(() => {
    const code = body.replace(/\n$/, "");
    try {
      const tree = highlightCode(code, language);
      return toHtml(tree);
    } catch {
      return null;
    }
  }, [body, language]);

  if (html == null) {
    // 高亮失败不能把代码清空——退回到无高亮渲染。
    return (
      <code className={cn("hljs", className)}>{body.replace(/\n$/, "")}</code>
    );
  }

  return (
    <code
      className={cn("hljs", language && `language-${language}`, className)}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

/**
 * 带悬停复制 chrome 的 `<pre>` 外壳，与 code-block-view.tsx 里可编辑代码块
 * 的头部（语言标签 + 复制）保持一致。
 * @param language 语言标签。
 * @param code 用于复制的代码文本。
 * @param children 代码内容。
 */
export function CodeBlockShell({
  language,
  code,
  children,
}: {
  language?: string;
  code: string;
  children: ReactNode;
}) {
  const { t } = useT("editor");
  const [copied, setCopied] = useState(false);
  const copyLabel = t(($) => $.code_block.copy_code) || "Copy code";

  const handleCopy = async () => {
    if (!code) return;
    if (await copyText(code)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  return (
    <div className="code-block-wrapper group/code relative my-3">
      <div className="absolute top-0 right-0 z-10 flex items-center gap-1.5 px-2 py-1.5 opacity-0 transition-opacity group-hover/code:opacity-100 focus-within:opacity-100">
        {language && (
          <span className="text-caption text-muted-foreground select-none">
            {language}
          </span>
        )}
        <button
          type="button"
          onClick={handleCopy}
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
          title={copyLabel}
          aria-label={copyLabel}
        >
          {copied ? (
            <Check className="h-3.5 w-3.5" />
          ) : (
            <Copy className="h-3.5 w-3.5" />
          )}
        </button>
      </div>
      {/* 不加额外右内边距：`.rich-text-editor pre` 的优先级本来就高于工具类
          内边距，而可编辑 NodeView 也用同样的 1rem——保持一致才能让换行一致。 */}
      <pre className="!m-0">{children}</pre>
    </div>
  );
}

/**
 * 升级围栏的富叶子。只在 shouldUpgradeFence() 返回 true 时才会走到这里，
 * 所以此处围栏已知是闭合的。
 *
 * 两种叶子实例化都很昂贵（异步 Mermaid 渲染 / 沙箱 iframe），所以各自
 * 都包在近视口懒加载外壳里。预留高度来自叶子自身，因此外壳预留的正是
 * 挂载后块将占据的空间。
 * @param language 富围栏语言。
 * @param body 图表或 HTML 源码。
 */
export function RichFenceBlock({
  language,
  body,
}: {
  language: RichFenceLanguage;
  body: string;
}) {
  // 拆成两个组件，让「仅 Mermaid 使用」的高度 hook 永远不会被条件调用。
  if (language === "mermaid") return <MermaidFenceBlock chart={body} />;
  return <HtmlFenceBlock html={body} />;
}

/**
 * 图表的预留高度：首帧用骨架默认值，挂载后用会话缓存的真实高度。
 *
 * 缓存在 sessionStorage 里，服务端没有它。渲染期间读取它，服务端会得到
 * 280px，而带热缓存的浏览器得到缓存高度——在 React hydration 的那一帧上
 * `style="min-height:…"` 不一致，React 会报属性不匹配且不会修复。把读取
 * 推迟到 effect，能让首帧处处一致，紧接着仍能拿到零位移的好处。
 */
function useReservedMermaidHeightPx(chart: string): number {
  const [height, setHeight] = useState(MERMAID_SKELETON_HEIGHT_PX);
  useEffect(() => {
    const cached = reservedMermaidHeightPx(chart);
    setHeight((current) => (current === cached ? current : cached));
  }, [chart]);
  return height;
}

function MermaidFenceBlock({ chart }: { chart: string }) {
  return (
    <LazyRichBlock reservedHeightPx={useReservedMermaidHeightPx(chart)} sourceKey={chart}>
      <MemoMermaidDiagram chart={chart} />
    </LazyRichBlock>
  );
}

function HtmlFenceBlock({ html }: { html: string }) {
  // 预览 iframe 是固定高度，所以这里无需缓存，服务端和客户端天然一致。
  return (
    <LazyRichBlock reservedHeightPx={HTML_BLOCK_PREVIEW_HEIGHT_PX} sourceKey={html}>
      <MemoHtmlBlockPreview html={html} />
    </LazyRichBlock>
  );
}

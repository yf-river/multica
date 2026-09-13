"use client";

/**
 * 富文本模块：RichContent——产品级唯一的只读内容渲染器（MUL-4922）。
 *
 * Chat（用户消息、live assistant、persisted assistant、时间线文本）、issue
 * 描述和评论都通过这个组件渲染。产品里没有第二个 Markdown 渲染器：
 * `packages/ui/markdown` 仍然是一个面向终端/原始输出的通用原语，绝不能
 * 再长回产品关注点（Mention、Attachment、Mermaid、HTML 预览）。
 *
 * 界面之间的差异只在于它们包在这个组件外面的外壳：
 *   - 评论 / issue：编辑、回复、菜单、串线 chrome——都在外面。
 *   - Chat：时间线、思考、工具、失败、复制 chrome——都在外面。
 *
 * 公共 API 刻意收窄。没有 `surface` prop、没有 `renderMention` 覆盖、没有
 * 自定义代码渲染器的逃生门：每一个都是一扇让「按界面分叉」走回来的门。
 * 链接、提及和附件行为只在这里决定一次。
 *
 *   density——仅 CSS。从不切换解析器、插件、组件映射或语义 DOM；`compact`
 *             和 `document` 产出相同的块。
 *   phase——仅生命周期。不决定围栏是否升级；那取决于围栏真实的闭合状态
 *           （见 streaming-fence.ts），所以一个已了结但畸形的围栏仍渲染为源码。
 */

import { createContext, isValidElement, memo, useContext, useMemo, useRef } from "react";
import ReactMarkdown, {
  type Components,
  type ExtraProps,
  type Options as ReactMarkdownOptions,
} from "react-markdown";
import type { ComponentPropsWithoutRef, ReactNode } from "react";
import rehypeKatex from "rehype-katex";
import remarkBreaks from "remark-breaks";
import remarkCjkFriendly from "remark-cjk-friendly/parseOnly";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import rehypeRaw from "rehype-raw";
import rehypeSanitize from "rehype-sanitize";
import { cn } from "@multica/ui/lib/utils";
import { useWorkspaceSlug } from "@multica/core/paths";
import { useConfigStore } from "@multica/core/config";
import type { Attachment } from "@multica/core/types";
import {
  isAllowedFileCardHref,
  isIssueIdentifier,
  markdownSanitizeSchema,
  markdownUrlTransform,
} from "@multica/ui/markdown";
import {
  resolveClickIntent,
  useAppOrigin,
} from "../navigation";
import { IssueMentionCard } from "../issues/components/issue-mention-card";
import { useResolveIssueIdentifier } from "../issues/hooks";
import { ProjectMentionCard } from "../projects/components/project-mention-card";
import { useLinkHover, LinkHoverCard } from "../editor/link-hover-card";
import {
  openLink,
  isMentionHref,
  parseWorkspaceEntityLink,
  type WorkspaceEntityRef,
} from "../editor/utils/link-handler";
import { preprocessMarkdown } from "../editor/utils/preprocess";
import { highlightToHtml } from "../editor/utils/highlight-markdown";
import { AttachmentDownloadProvider } from "../editor/attachment-download-context";
import { Attachment as AttachmentRenderer } from "../editor/attachment";
import { computeClosedFenceOffsets } from "./streaming-fence";
import { remarkRepairCjkStrongTrailingWhitespace } from "./cjk-emphasis";
import {
  CodeBlockShell,
  RichFenceBlock,
  StaticCodeBody,
  isRichFenceLanguage,
  shouldUpgradeFence,
} from "./rich-code-block";
import "katex/dist/katex.min.css";
import "../editor/styles/index.css";
import "./rich-content.css";

/** 内容渲染密度：紧凑 / 文档。 */
export type RichContentDensity = "compact" | "document";
/** 内容生命周期阶段：流式传输中 / 已稳定。 */
export type RichContentPhase = "streaming" | "settled";

// ---------------------------------------------------------------------------
// 围栏闸门 context
// ---------------------------------------------------------------------------
//
// 已闭合围栏的偏移量通过 context 传递，而不是重建 components 映射。映射必须
// 保持引用稳定：`components` 身份变化时 react-markdown 会重跑整个子树，
// 那会重写每个高亮 `<code>` 的 innerHTML，并折叠代码块里激活的文本选区
// （MUL-3621）。

const ClosedFenceContext = createContext<ReadonlySet<number>>(new Set<number>());

/** 判断某个源偏移量对应的围栏是否已闭合。 */
function useIsFenceClosed(offset: number | undefined): boolean {
  const closed = useContext(ClosedFenceContext);
  // 没有偏移量的节点无法匹配到源码围栏；拒绝升级是安全方向（展示源码
  // 而不是可能残缺的块）。
  return offset != null && closed.has(offset);
}

// ---------------------------------------------------------------------------
// Mention / 链接渲染器
// ---------------------------------------------------------------------------

/**
 * Issue 提及小卡片。导航——普通点击、修饰键点击、以及「在新标签页打开
 * issue 链接」偏好——由 IssueMentionCard 内部的 AppLink 负责；这个包装层
 * 只是把周边点击处理器（比如折叠评论的展开器）与提及点击隔离开。
 */
function IssueMentionLink({ issueId, label }: { issueId: string; label?: string }) {
  return (
    <span className="inline align-middle" onClick={(e) => e.stopPropagation()}>
      <IssueMentionCard issueId={issueId} fallbackLabel={label} />
    </span>
  );
}

/**
 * 用标识符（如 `MUL-123`）而非 UUID 寻址的 issue。对它相对当前工作区解析，
 * 命中时渲染一个可导航的提及。
 *
 * 有两条入口到达这里，它们需要*不同*的未命中行为，所以兜底做成 prop 而不是
 * 写死：
 *   - 自动链接预处理器，把裸文本路由成 `mention://issue/<identifier>`——
 *     未命中必须退回作者输入的纯文本；
 *   - 裸的应用内 issue URL，未命中必须保留原始锚点。作者写的是链接；把它
 *     降级成文本，会从一个本工作区看不见的 issue 上剥掉一个能用的链接。
 *
 * 未命中统一覆盖「未找到、仍在加载、跨工作区」三种情况。
 */
function IdentifierIssueMentionLink({
  identifier,
  fallback,
}: {
  identifier: string;
  fallback: ReactNode;
}) {
  const issue = useResolveIssueIdentifier(identifier);
  if (!issue) return <>{fallback}</>;
  return <IssueMentionLink issueId={issue.id} label={identifier} />;
}

/**
 * 项目提及小卡片。导航和无障碍由 ProjectMentionCard 内部的 AppLink 负责；
 * 这个包装层只是把周边点击处理器（比如折叠评论的展开器）与提及点击隔离开
 * ——形状和上面的 IssueMentionLink 一样。
 */
function ProjectMentionLink({ projectId, label }: { projectId: string; label?: string }) {
  return (
    <span className="inline align-middle" onClick={(e) => e.stopPropagation()}>
      <ProjectMentionCard projectId={projectId} fallbackLabel={label} />
    </span>
  );
}

function childrenToLabel(children: ReactNode): string | undefined {
  if (typeof children === "string") return children;
  if (Array.isArray(children)) return children.join("");
  return undefined;
}

/**
 * 判断一个链接是否应渲染成实体小卡片而不是裸 URL。
 *
 * 粘贴从应用里复制出来的链接，是人们引用一个项目的方式：项目只带 UUID 和
 * 自由文本标题，所以不像 issue 那样有 `MUL-123` 缩写供自动链接预处理器
 * 检测——URL 本身就是引用。把它渲染成与 `mention://project/<uuid>` 形式
 * 相同的小卡片，就能弥合这个缺口而不必发明新的文本形式。issue URL 也走
 * 同一路径以保持对称。
 *
 * 三个条件，每个都承重：
 *   - BARE（裸）：可见文本就是 URL 本身，这正是 linkify 预处理器对粘贴 URL
 *     的产物。`[路线图](…/projects/<id>)` 带的是作者选的标签，用小卡片替换
 *     它会丢掉它。
 *   - SAME WORKSPACE（同工作区）：小卡片相对*当前*工作区解析标题，所以把
 *     链接展开到另一个工作区，会把能用的链接变成永久空的小卡片。无 slug
 *     的路径本身就已经是当前工作区。
 *   - `parseWorkspaceEntityLink` 强制的约束：精确的实体路由、只寻址一个
 *     实体的 id（UUID 或 issue 标识符）、且没有查询串或片段。
 */
function unfurlableEntityLink(
  href: string,
  children: ReactNode,
  currentSlug: string | null,
  appOrigin: string | null,
): WorkspaceEntityRef | null {
  if (childrenToLabel(children) !== href) return null;
  const entity = parseWorkspaceEntityLink(href, appOrigin);
  if (!entity) return null;
  if (entity.slug !== null && entity.slug !== currentSlug) return null;
  return entity;
}

function RichLink({ href, children }: { href?: string; children?: ReactNode }) {
  const slug = useWorkspaceSlug();
  const appOrigin = useAppOrigin();
  if (href?.startsWith("slash://skill/")) {
    return <span className="slash-command">{children}</span>;
  }

  if (isMentionHref(href)) {
    const match = href.match(/^mention:\/\/(member|agent|issue|project|all)\/(.+)$/);
    if (match?.[1] === "issue" && match[2]) {
      // 裸标识符（来自自动链接预处理器）以 id 段的形式携带；真正的提及
      // 携带 UUID。根据 id 形状分发。
      if (isIssueIdentifier(match[2])) {
        return (
          <IdentifierIssueMentionLink
            identifier={match[2]}
            fallback={match[2]}
          />
        );
      }
      return <IssueMentionLink issueId={match[2]} label={childrenToLabel(children)} />;
    }
    if (match?.[1] === "project" && match[2]) {
      return <ProjectMentionLink projectId={match[2]} label={childrenToLabel(children)} />;
    }
    // Member / agent / all 提及
    return <span className="mention">{children}</span>;
  }

  // 普通链接——点击直接打开。指向本部署的 URL 在应用内路由，而不是跳到
  // 浏览器。先构建它，是因为当 URL 背后的实体解析不出时，展开也回退到它。
  const plainLink = (
    <a
      href={href}
      onClick={(e) => {
        if (!href) {
          e.preventDefault();
          return;
        }
        if (e.metaKey || e.ctrlKey || e.shiftKey) return;
        e.preventDefault();
        openLink(href, slug, appOrigin, resolveClickIntent(e));
      }}
    >
      {children}
    </a>
  );

  // 裸的应用内实体 URL 渲染成它的提及形式会有的小卡片。
  const entity = href
    ? unfurlableEntityLink(href, children, slug, appOrigin)
    : null;
  if (entity?.kind === "issue") {
    // `MUL-123` 是「复制链接」产出的形状，所以大多数粘贴的 issue URL 都带
    // 它；它需要一次查找，未命中时保留链接。
    return isIssueIdentifier(entity.id) ? (
      <IdentifierIssueMentionLink identifier={entity.id} fallback={plainLink} />
    ) : (
      <IssueMentionLink issueId={entity.id} />
    );
  }
  if (entity?.kind === "project") {
    return <ProjectMentionLink projectId={entity.id} />;
  }

  return plainLink;
}

// ---------------------------------------------------------------------------
// Fenced code
// ---------------------------------------------------------------------------

function getTextContent(node: ReactNode): string {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(getTextContent).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) {
    return getTextContent(node.props.children);
  }
  return "";
}

// react-markdown 实际传给组件覆盖的 props：内置元素自身的 props 加上源码
// hast 节点（`ExtraProps`）。从它们推导能让渲染器对照 react-markdown 的
// 真实契约检查，而不是手写一个近似、再用强转去掩盖。
type RichCodeProps = ComponentPropsWithoutRef<"code"> & ExtraProps;
type RichPreProps = ComponentPropsWithoutRef<"pre"> & ExtraProps;

/**
 * 取一个 hast 节点的源码偏移量，集中在这里收窄。偏移量是把渲染出的块匹配
 * 回处理后 Markdown 里对应围栏的方式（见 streaming-fence.ts）；没有 position
 * 的节点无法匹配。
 */
function nodeStartOffset(node: ExtraProps["node"]): number | undefined {
  return node?.position?.start.offset;
}

/**
 * 把 hast 元素属性读成字符串。hast 属性值是联合类型（string | number |
 * boolean | array | null），所以这里在运行时收窄而不是断言——非字符串的
 * `data-*` 属性返回 ""，而不是一个谎报类型的值。
 */
function stringProperty(node: ExtraProps["node"], key: string): string {
  const value = node?.properties?.[key];
  return typeof value === "string" ? value : "";
}

/** 判断节点是否跨多行。 */
function isMultiLineNode(node: ExtraProps["node"]): boolean {
  const position = node?.position;
  return position != null && position.start.line !== position.end.line;
}

/**
 * `code` 渲染器。对升级围栏直接返回富叶子，并依赖下面的 `pre` 渲染器把它
 * 解包——否则 react-markdown 会把 `code` 子内容包进一个 `<pre>`，其等宽/
 * 溢出样式会夹住图表或预览 iframe。
 */
function RichCode({ className, children, node, ...props }: RichCodeProps) {
  const language = /language-(\w+)/.exec(className || "")?.[1];
  const isBlock = isMultiLineNode(node);
  const isFenceClosed = useIsFenceClosed(nodeStartOffset(node));

  if (isBlock && shouldUpgradeFence(language, isFenceClosed)) {
    // 再次检查 isRichFenceLanguage 是为了类型收窄；shouldUpgradeFence 已经
    // 要求过它了。
    if (isRichFenceLanguage(language)) {
      return <RichFenceBlock language={language} body={String(children).replace(/\n$/, "")} />;
    }
  }

  if (!isBlock && !language) {
    // 行内代码——样式交给 .rich-text-editor code 处理。
    return <code {...props}>{children}</code>;
  }

  return <StaticCodeBody language={language} body={String(children)} />;
}

/**
 * 从 react-markdown 交给 `pre` 覆盖的 `<code>` 元素里读出语言 token 和源码
 * 偏移量。`isValidElement` 的类型参数收窄了子元素的 props，所以无需强转
 * 就能拿到 `node`。
 */
function readFencedCodeChild(children: ReactNode): {
  language?: string;
  offset?: number;
} {
  const child = Array.isArray(children) ? children[0] : children;
  if (!isValidElement<{ className?: string } & ExtraProps>(child)) return {};
  return {
    // 只匹配完整 class token：`language-htmlbars` 不能被读成 `html`。
    language: /(?:^|\s)language-(\w+)(?:\s|$)/.exec(child.props.className ?? "")?.[1],
    offset: nodeStartOffset(child.props.node),
  };
}

/**
 * `pre` 渲染器。react-markdown 在调用 `code` 渲染器*之前*调用它，所以
 * `children` 是 AST 里尚未渲染的 `<code>` 元素：解包决策由那个子元素自己的
 * hast 节点（class token + 源码偏移量）做出，而不是检查
 * `children.type === MermaidDiagram`——那个永远不匹配。
 */
function RichPre({ children }: RichPreProps) {
  const { language, offset } = readFencedCodeChild(children);
  const isFenceClosed = useIsFenceClosed(offset);

  // 升级围栏完全逃出 <pre><code> 外壳。未闭合的 mermaid/html 围栏刻意
  // 落到普通代码外壳，这样流式传输到一半的图显示为带复制 chrome 的普通源码。
  if (shouldUpgradeFence(language, isFenceClosed)) {
    return <>{children}</>;
  }

  return (
    <CodeBlockShell
      language={language}
      code={getTextContent(children).replace(/\n$/, "")}
    >
      {children}
    </CodeBlockShell>
  );
}

// components 映射是模块级且静态的：它从不依赖 density、phase 或围栏闸门，
// 所以它的身份在应用整个生命周期里稳定，react-markdown 能跳过未变化的子树。
const COMPONENTS: Partial<Components> = {
  a: RichLink,

  // 图片——统一通过 <Attachment>。AttachmentDownloadProvider 提供的解析
  // context 会尽可能把 CDN URL 转成完整记录；外部 URL 渲染为普通图片，带
  // 预览弹窗的 lightbox。forceKind 是必须的：markdown `![]()` 不带内容类型，
  // alt 通常也是空的，没有它图片会落到文件卡片的 chrome 上。
  img: ({ src, alt }) => (
    <AttachmentRenderer
      attachment={{
        kind: "url",
        url: typeof src === "string" ? src : "",
        filename: alt ?? "",
        forceKind: "image",
      }}
    />
  ),

  // FileCard——拦截 preprocessMarkdown 产出的 <div data-type="fileCard">。
  div: ({ node, children, ...props }) => {
    if (stringProperty(node, "dataType") === "fileCard") {
      const rawHref = stringProperty(node, "dataHref");
      const href = isAllowedFileCardHref(rawHref) ? rawHref : "";
      return (
        <AttachmentRenderer
          attachment={{
            kind: "url",
            url: href,
            filename: stringProperty(node, "dataFilename"),
          }}
        />
      );
    }
    return <div {...props}>{children}</div>;
  },

  // 表格——包进 tableWrapper div 以获得边框/圆角/滚动（与 Tiptap 一致）。
  table: ({ children }) => (
    <div className="tableWrapper">
      <table>{children}</table>
    </div>
  ),

  code: RichCode,
  pre: RichPre,
};

// 用 `satisfies` 而不是强转：插件列表仍对照 react-markdown 自身的选项类型
// 检查，所以错误的插件元组会在这里失败，而不是被掩盖掉。
const REMARK_PLUGINS = [
  [remarkMath, { singleDollarTextMath: false }],
  remarkBreaks,
  [remarkGfm, { singleTilde: false }],
  remarkCjkFriendly,
  remarkRepairCjkStrongTrailingWhitespace,
] satisfies NonNullable<ReactMarkdownOptions["remarkPlugins"]>;

const REHYPE_PLUGINS = [
  rehypeRaw,
  [rehypeSanitize, markdownSanitizeSchema],
  rehypeKatex,
] satisfies NonNullable<ReactMarkdownOptions["rehypePlugins"]>;

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/** RichContent 组件的属性。 */
export interface RichContentProps {
  content: string;
  /**
   * 与周边实体（评论 / issue 正文 / 聊天消息）关联的附件。与其中某个匹配的
   * 行内 `<img>` 和文件卡片 URL，会在点击时重新签名下载 URL，而不是用可能
   * 已过期的、内嵌在 markdown 里的链接。
   *
   * 调用方应传稳定引用；每次父级渲染都新建数组会破坏 memo。
   */
  attachments?: Attachment[];
  /** 仅 CSS 密度——从不改变语义 DOM。 */
  density?: RichContentDensity;
  /**
   * 流式生命周期。不门控围栏升级（由闭合状态负责）；它存在是为了让界面能
   * 表达「内容仍在到达」。
   */
  phase?: RichContentPhase;
  className?: string;
}

/** 产品级只读内容渲染器组件，用 memo 包裹避免无关重渲染。 */
export const RichContent = memo(function RichContent({
  content,
  attachments,
  density = "document",
  phase = "settled",
  className,
}: RichContentProps) {
  // 订阅而不是读一次。CDN 配置在认证后异步拉取，所以先渲染的内容否则会
  // 把它的旧 CDN 链接永久保留为普通锚点——把它们转成文件卡片的 preprocess
  // 步骤已经对着空域名跑过了，再没有东西会重跑它。订阅把值放进下面 memo
  // 的依赖里，这样配置迟到时正好重处理一次。
  const cdnDomain = useConfigStore((s) => s.cdnDomain);

  const processed = useMemo(
    () =>
      highlightToHtml(
        preprocessMarkdown(content, { cdnDomain, autolinkIssueIdentifiers: true }),
      ),
    [content, cdnDomain],
  );

  // 从交给 ReactMarkdown 的*同一个*字符串推导，这样偏移量与 `code`/`pre`
  // 渲染器观察到的 hast 节点位置对齐。若从 pre-preprocess 原始文本计算，
  // 会与每个被改写的节点错位。
  const closedFences = useMemo(() => computeClosedFenceOffsets(processed), [processed]);

  const wrapperRef = useRef<HTMLDivElement>(null);
  const hover = useLinkHover(wrapperRef);

  // 只在真正输入变化时 memo 化 react-markdown 子树。无关的父级重渲染（兄弟
  // agent 任务通过 WebSocket 流式传输，约每 100ms 触发一次）否则会重跑
  // react-markdown，每次都交给 `<code>` 一个全新的 `dangerouslySetInnerHTML`
  // 对象；React 于是重写高亮 innerHTML，哪怕字符串逐字节相同，从而拆掉每个
  // hljs <span> 并折叠代码块里激活的文本选区（MUL-3621）。稳定的元素引用
  // 让 React 能跳过。
  const markdown = useMemo(
    () => (
      <ClosedFenceContext.Provider value={closedFences}>
        <ReactMarkdown
          remarkPlugins={REMARK_PLUGINS}
          rehypePlugins={REHYPE_PLUGINS}
          urlTransform={markdownUrlTransform}
          components={COMPONENTS}
        >
          {processed}
        </ReactMarkdown>
      </ClosedFenceContext.Provider>
    ),
    [processed, closedFences],
  );

  return (
    <AttachmentDownloadProvider attachments={attachments}>
      <div
        ref={wrapperRef}
        data-rich-content=""
        data-density={density}
        data-phase={phase}
        className={cn(
          "rich-text-editor readonly text-body",
          density === "compact" && "rich-content-compact",
          className,
        )}
      >
        {markdown}
        <LinkHoverCard {...hover} />
      </div>
    </AttachmentDownloadProvider>
  );
});

"use client";

/**
 * ContentEditor——用户**输入**内容处所用的富文本编辑器。
 *
 * 架构决策（2026 年 4 月重构）：
 *
 * 1. 只做编辑。只读展示交给 `ReadonlyContent`（react-markdown 渲染器），
 *    本组件不管。这里过去有个 `editable` prop 在两种模式间切换，但所有只读
 *    调用点都迁去 ReadonlyContent 后，那个 prop 只会引来误用——Tiptap 的
 *    `useEditor` 在挂载时读 `editable`，稍后切换 prop 会静默失败（以只读挂载
 *    的编辑器永远无法聚焦）。要表达「当前禁用」，请在外层布局用
 *    `pointer-events-none` / `aria-disabled`——别伸手进编辑器。
 *
 * 2. 单一 Markdown 管线：经 @tiptap/markdown。内容以 `contentType:
 *    'markdown'` 载入、以 `editor.getMarkdown()` 保存。过去我们用自定义
 *    `markdownToHtml()` 管线（Marked 库）载入 + 正则后处理保存——两条不对称
 *    的路径造成了往返不一致。@tiptap/markdown 扩展（v3.21.0+）原生处理表格
 *    单元格 <p> 包裹与自定义 mention tokenizer，不再需要 HTML 绕行。
 *
 * 3. 预处理尽量少：只有旧式 mention 短码迁移与 URL 链接化
 *    （preprocessMarkdown）。不做 HTML 转换。
 *
 * 技术栈：Tiptap v3（ProseMirror 封装）、@tiptap/markdown 做双向的
 * Markdown ↔ ProseMirror JSON 转换。
 */

import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { useEditor, EditorContent, type Editor } from "@tiptap/react";
import { cn } from "@multica/ui/lib/utils";
import type { UploadResult } from "@multica/core/hooks/use-file-upload";
import { useWorkspaceSlug } from "@multica/core/paths";
import { useQueryClient } from "@tanstack/react-query";
import { issueIdentifierOptions } from "@multica/core/issues/queries";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { isIssueIdentifier } from "@multica/ui/markdown";
import type { Attachment } from "@multica/core/types";
import {
  parseMarkdownChunked,
  MARKDOWN_CHUNK_THRESHOLD,
  type MarkdownManagerLike,
} from "./utils/parse-markdown-chunked";
import type { MentionItem } from "./extensions/mention-suggestion";
import type { IssueIdentifierResolver } from "./extensions/issue-identifier-autolink";
import type { BuiltinCommandSuggestionOptions } from "./extensions/slash-command-suggestion";
import { createEditorExtensions } from "./extensions";
import {
  uploadAndInsertFile,
  insertUploadPlaceholder,
  settleUploadNode,
} from "./extensions/file-upload";
import { configStore } from "@multica/core/config";
import { preprocessMarkdown } from "./utils/preprocess";
import { repairEmptyListItems } from "./utils/repair-list-items";
import { resolveClickIntent, useAppOrigin } from "../navigation";
import { openLink, isMentionHref } from "./utils/link-handler";
import { EditorBubbleMenu } from "./bubble-menu";
import { posFromAnchor, type TextAnchor } from "./text-anchor";
import { useLinkHover, LinkHoverCard } from "./link-hover-card";
import { AttachmentDownloadProvider } from "./attachment-download-context";
import "katex/dist/katex.min.css";
import "./styles/index.css";

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

/** markdown 字符串的规范化比较形态：去掉块在两端留下的空行，让脏检查两侧
 *  能对等比较。
 *
 *  过去这里还用正则剥 `blob:` 图片行，因为在途图片会把进程级 blob URL
 *  序列化进正文。如今这已在源头不可能：image 与 fileCard 的 `renderMarkdown`
 *  在 `attrs.uploading` 置位时什么都不输出，占位符绝不会变成需要事后擦掉的
 *  文本。
 *
 *  开头空行同样要 trim，不只是结尾：第一个块里的占位符会把它那行空行留在
 *  文档头，而开头带空行的草稿与不带的是同一内容。（旧正则也会为此留下一个
 *  换行。） */
function normalizeMarkdown(md: string): string {
  return md.trim();
}

/** 对实时编辑器序列化内容应用 `normalizeMarkdown`。 */
function normalizeEditorMarkdown(editor: Editor): string {
  return normalizeMarkdown(editor.getMarkdown());
}

/** 文档里是否有节点处于上传中（`attrs.uploading`）。`return !found` 的提前
 *  返回与原来的行内扫描逐字一致：在 ProseMirror 里它只停止进入命中节点的
 *  子树（而非整个遍历），但 `found` 一旦为 true，布尔结果就已定。 */
function hasUploadingNode(editor: Editor): boolean {
  let found = false;
  editor.state.doc.descendants((node) => {
    if (node.attrs.uploading) found = true;
    return !found;
  });
  return found;
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ContentEditorBaseProps {
  /**
   * `baseMarkdown` 是本编辑器在产出 `markdown` 之前真正采纳的最后一个权威
   * 受控值。脏编辑器的实时守卫可能有意跳过更新的服务端内容，因此调用方
   * 绝不能用最新 prop 值替代这个 base。
   */
  onUpdate?: (markdown: string, baseMarkdown: string) => void;
  placeholder?: string;
  className?: string;
  debounceMs?: number;
  onSubmit?: () => void;
  onBlur?: () => void;
  /**
   * 上传通道。`uploadId` 由编辑器在插入占位节点时铸造；后端有草稿 store 的
   * 宿主必须把它采纳为上传的 `clientUploadId`，让两份记录共享同一身份——
   * 这正是 settle 能在后来挂载重建的文档里找到自己占位符的原因。没有持久化
   * 的宿主可忽略它。
   */
  onUploadFile?: (file: File, uploadId: string) => Promise<UploadResult | null>;
  /**
   * 纯文本粘贴超过该字符数时，改为作为 `pasted-text.txt` 附件上传而非正文。
   * 需要 `onUploadFile`；没有上传器则粘贴保持文本。
   *
   * 刻意选开，今天只有 chat 传它：那里的一大段粘贴文本是交给 agent 的一个
   * 回合的上下文，作为附件比作为没人会滚动的正文可读得多。其余编辑器都保持
   * 粘贴行内——issue 与 project 描述里长粘贴**就是**内容，issue 评论里则是
   * 期望人类读者在线程里看到的散文。
   */
  pasteAsFileThreshold?: number;
  /**
   * 本编辑器「是否仍有附件在上传」的答案翻转时触发。文档本身就是上传队列——
   * 每条路径（粘贴、拖放、上传按钮、命令式 `uploadFile`）都会在 await 前插入
   * 一个 `attrs.uploading` 的节点，落定时清除或移除它——所以宿主可以基于单一
   * 事实来源驱动提交门禁，而不是自己维护一个会被手动删节点打乱的计数器。
   *
   * 与提交时刻的 `hasActiveUploads()` 二次检查搭配使用：本回调驱动渲染出来的
   * 按钮状态，而 ref 读取是键盘提交与最后一次上传落定竞争时必须咨询的。
   *
   * 不做门禁的宿主（自动保存的描述编辑器）省略它并零成本——它在缺席时整段
   * 扫描都被跳过。
   */
  onUploadingChange?: (uploading: boolean) => void;
  /** 文本选中时显示浮动格式化工具条。默认 true。 */
  showBubbleMenu?: boolean;
  /**
   * 本编辑器所属的 issue id。设置时，气泡菜单提供「从选区创建子 issue」动作，
   * 把新 issue 挂在它下面并用 mention 链接替换选区。
   */
  currentIssueId?: string;
  /**
   * 为 true 时禁用 `@` 建议弹层，但 mention 节点类型仍留在 schema 里，让从
   * 其它 Multica 编辑器粘贴进来的既有 mention 仍渲染成普通胶囊。用于「新建
   * mention 没有业务意义、但保留既有 mention 仍重要」的编辑器（如 agent
   * 系统提示词）。
   */
  disableMentions?: boolean;
  /** chat 可呈现当前/最近 issue/project 建议。其它编辑器用默认 mention 行为。 */
  mentionMode?: "default" | "context";
  mentionContextItems?: MentionItem[];
  /** 启用 `/` 命令选择器。默认 false。 */
  enableSlashCommands?: boolean;
  /**
   * enableSlashCommands 为 true 时显示哪种 `/` 菜单："skill"（默认）列出
   * 当前 agent 的技能（chat）；"command" 显示固定的内建命令菜单（issue
   * 评论），如 /note。
   */
  slashCommandMode?: "skill" | "command";
  /**
   * "command" `/` 菜单要提供的快捷动作，外加把选择解析成它将发布的文本的
   * resolver（MUL-5465）。经函数读取，新建的动作无需重挂编辑器即可出现。
   */
  quickActionMenu?: BuiltinCommandSuggestionOptions;
  /**
   * 本内容引用的附件。编辑器内文件卡片与图片上的下载按钮会按 `url` 查附件、
   * 在点击时刻取新 CloudFront 签名，因此持久化在 markdown 里的过期 URL 从不
   * 打开。传 `issue.attachments` / `comment.attachments` 等；没有附件上下文
   * 时省略（NodeView 按钮退回打开原始 URL）。
   */
  attachments?: Attachment[];
  /**
   * 编辑器卸载时冲刷挂起的防抖 `onUpdate` 而不是丢弃。默认 false 是刻意的：
   * 大多数 composer 先清草稿再卸载（评论编辑取消、create-issue
   * 提交），那里冲刷会把刚丢弃的内容立刻交回 `onUpdate`，复活被清掉的草稿。
   * 只在「关闭=保留用户最后所见」处选开——如 issue 详情描述编辑器，否则
   * 1500ms 防抖会丢掉恰好赶在弹窗关闭前的粘贴。
   */
  flushPendingOnUnmount?: boolean;
  /**
   * Tiptap 实例存在且初始内容已设置时调用一次（`immediatelyRender: false`
   * 把创建推迟到首帧之后）。评论/回复 composer 等只读优先宿主以此为信号，
   * 把静态外壳换成实时编辑器。
   */
  onReady?: () => void;
}

type ContentEditorValueProps =
  | {
      /** 初始 markdown，编辑器挂载时读一次。 */
      defaultValue?: string;
      value?: never;
    }
  | {
      /**
       * 外部同步的 markdown。只在「外部变化必须替换本编辑器文档」时使用
       * （例如实时服务端更新，或切换稳定编辑器实例所持有的文档）。
       */
      value: string;
      defaultValue?: never;
    };

type ContentEditorProps = ContentEditorBaseProps & ContentEditorValueProps;

interface ContentEditorRef {
  getMarkdown: () => string;
  clearContent: () => void;
  focus: () => void;
  /**
   * 聚焦并把光标放到给定视口坐标下的文档位置。供只读优先宿主使用，让那次
   * 召唤编辑器的点击把光标落在用户点击处，与常驻编辑器行为一致。坐标解不出
   * 位置时（点击最后一行下方）退回聚焦到末尾。必须在编辑器元素已布局
   * （非 display:none）时调用。
   */
  focusAtCoords: (coords: { x: number; y: number }) => void;
  /**
   * 聚焦并把光标放到文本锚点解析出的文档位置。只读优先宿主优先用
   * `focusAtCoords` 的它：锚点是逻辑位置（「第 N 块、第 M 个字符」），对
   * 只读渲染与编辑器渲染之间的布局差异免疫——那正是长文档上像素坐标漂移
   * 的地方。
   */
  focusAtAnchor: (anchor: TextAnchor) => void;
  /** 让编辑器失焦。供 `useComposerSubmit` 的 `afterAccepted: "blur"` 在
   *  「发送结束回合」的界面使用，让 composer 不再像「还在写」。 */
  blur: () => void;
  uploadFile: (file: File) => void;
  /** 文件上传是否仍在进行中。 */
  hasActiveUploads: () => boolean;
  /**
   * 把一个 markdown 片段追加到文档末尾（解析、非原始文本），触发正常的
   * `onUpdate` 管线。供上传回写路径使用（MUL-5181）：一次活过发起挂载的
   * 上传落定时，一个新的编辑器实例正在展示同一份草稿——那个编辑器从未拥有
   * 该上传的 promise，所以这是让完成附件的链接落进可见文档（而不仅是持久化
   * 草稿）的方式。
   *
   * 返回插入是否真正落地。命令式句柄在组件首次 commit 就存在，但 Tiptap
   * 实例在被动 effect 里创建——在那段窗口内（以及销毁后）这是 no-op 并
   * 返回 false，调用方可以回退或重试，而不是静默丢片段。
   */
  insertMarkdownAtEnd: (markdown: string) => boolean;
  /**
   * 为「本文档还没展示的上传」画占位符，并报告是否落地。
   *
   * 在前一挂载启动的上传之上重开的 composer 持有草稿里的记录但没有节点——
   * 占位符从不序列化，随画它的文档一起消亡。没有它，用户面对一个看起来空闲
   * 的 composer，而发送门禁却在悄悄等上传。Tiptap 实例还没就绪时返回 false
   *（句柄首次 commit 即有、实例晚一个被动 effect 才到），调用方可以重试
   * 而非想当然。
   */
  insertUploadPlaceholder: (upload: {
    uploadId: string;
    filename: string;
    size?: number;
  }) => boolean;
  /**
   * 把占位符原位替换成已完成的附件。本文档没有该 id 的节点时返回 false——
   * 调用方随后退回追加链接。
   */
  settleUploadPlaceholder: (uploadId: string, result: UploadResult) => boolean;
  /**
   * 取消挂起的防抖 `onUpdate`，并把它的 markdown 交还给调用方而不是触发。
   * 没有挂起内容时返回 null。
   *
   * 供把**同一个**编辑器实例重新指向另一目标的宿主使用（chat 在会话间切换
   * `draftKey`）。旧目标下武装的防抖会在切换后触发，且因 `onUpdate` 总是
   * resolve 到最新一次渲染的闭包，把旧文档写进**新**目标。把 markdown 收回，
   * 宿主就能把它提交到真正输入它的地方。冲刷也把编辑器标为干净，脏守卫不再
   * 压制进来的同步 `value`。
   *
   * 与 `flushPendingOnUnmount` 不同：这是为**活着**的编辑器换目标准备的，
   * 所以读的是当前文档而不是缓存副本。
   */
  flushPendingUpdate: () => string | null;
  /**
   * 强制把 `markdown` 塞进文档，绕过同步 `value` 守卫。
   *
   * 那些守卫是永久跳过而非推迟：`lastSyncedValueRef` 在它们运行前就前进了，
   * 被拒绝的 `value` 不会再被应用。这对它们的常规场景正确（缓存会再发一个
   * value），但不适用于「把**一个**实例重新指向另一份文档、必须恰好落地一次」
   * 的宿主——chat 的草稿切换，其中在途上传会让守卫 0 拒绝本次切换。
   *
   * 调用方自己负责守卫通常提供的安全：只在阻挡理由消失（上传落定）、挂起
   * 编辑已被冲刷后才调用，否则会毁掉它们。
   */
  adoptContent: (markdown: string) => void;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

const ContentEditor = forwardRef<ContentEditorRef, ContentEditorProps>(
  function ContentEditor(
    {
      defaultValue,
      value,
      onUpdate,
      placeholder: placeholderText = "",
      className,
      debounceMs = 300,
      onSubmit,
      onBlur,
      onUploadFile,
      pasteAsFileThreshold,
      onUploadingChange,
      showBubbleMenu = true,
      currentIssueId,
      disableMentions = false,
      mentionMode = "default",
      mentionContextItems,
      enableSlashCommands = false,
      slashCommandMode = "skill",
      quickActionMenu,
      attachments,
      flushPendingOnUnmount = false,
      onReady,
    },
    ref,
  ) {
    const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);
    const flushPendingOnUnmountRef = useRef(flushPendingOnUnmount);
    // 在 `onUpdate` 时刻序列化、等待防抖触发的 markdown。卸载冲刷输出这份
    // 缓存副本——它跑在卸载拆除中途，不能假设编辑器实例仍可读。
    const pendingFlushRef = useRef<string | null>(null);
    const pendingBaseRef = useRef<string | null>(null);
    const onUpdateRef = useRef(onUpdate);
    const onSubmitRef = useRef(onSubmit);
    const onBlurRef = useRef(onBlur);
    const onReadyRef = useRef(onReady);
    const onUploadingChangeRef = useRef(onUploadingChange);
    const onUploadFileRef = useRef<
      ((file: File, uploadId: string) => Promise<UploadResult | null>) | undefined
    >(undefined);
    // 与下面 placeholderRef 同理：扩展数组在挂载时只建一次，粘贴成文件阈值
    // 经 ref 读取才能不重挂编辑器地保持最新。
    const pasteAsFileThresholdRef = useRef<number | undefined>(pasteAsFileThreshold);
    const mentionContextItemsRef = useRef<MentionItem[]>(mentionContextItems ?? []);
    // 放进 ref 与 mentionContextItems 同理：扩展集挂载时只建一次，直接捕获
    // options 对象会冻结当时存在的闭包、看不到新加的快捷动作。
    const quickActionMenuRef = useRef<BuiltinCommandSuggestionOptions | undefined>(quickActionMenu);
    const lastEmittedRef = useRef<string | null>(null);
    // `content` 在 Tiptap 挂载时已经消费了初始同步值。之后的变化单独跟踪，
    // 让同步 effect 不会在 Markdown 序列化把它规范化时把初始文档解析两遍。
    const lastSyncedValueRef = useRef(value);
    // 用户正在编辑的文档背后的权威 markdown。保留原始受控值而非编辑器序列化
    // 结果：Tiptap 可能省略不可见的 channel-media 来源注释、只保留可见图片，
    // 而服务端在合并基准里需要那些注释。
    const documentBaseRef = useRef(normalizeMarkdown(value ?? defaultValue ?? ""));
    // 实时占位文本。以 getter（而非静态字符串）传给 Placeholder 扩展，插件在
    // 每次装饰扫描时重新读取——下面的同步 effect 更新该 ref 并触发一次重绘。
    // Tiptap 会把*字符串*占位符在挂载时快照，getter 才能让它不重挂就变化。
    const placeholderRef = useRef(placeholderText);

    // 本会话内经本编辑器新上传的附件记录。没有服务端 `attachments` prop 的
    // 界面（如快速创建弹窗）仍需 AttachmentDownloadProvider 知道用户刚
    // 粘贴/拖放的图片——作用域里没有记录的话，Attachment.normalize() 无法把
    // 持久化的 /api/attachments/<id>/download URL 换成可重新加载的版本，在
    // 文档源不把 /api 代理到 API 宿主的任何环境里 <img> 都会渲染坏
    //（MUL-3192）。
    const [sessionUploads, setSessionUploads] = useState<Attachment[]>([]);
    // 包一层调用方提供的上传器，把每次成功结果存进 `sessionUploads`。包装器
    // 只在底层 `onUploadFile` 身份变化时重建，让交给 Tiptap 的内层 ref 与
    // 原来直通时一样跨渲染稳定。
    const wrappedOnUploadFile = useMemo(() => {
      if (!onUploadFile) return undefined;
      return async (file: File, uploadId: string): Promise<UploadResult | null> => {
        const result = await onUploadFile(file, uploadId);
        // 只跟踪带持久化 id 的附件——无工作区头像分支返回的 id-less 记录，
        // resolver 无法以其为键，跟踪它只会白白占内存。原因见 useFileUpload
        // 的 `markdownLink` 文档。
        if (result?.id) {
          setSessionUploads((prev) =>
            // 按 id 去重，重复上传（或同 blob 先粘后拖）不会产生并行记录。
            prev.some((a) => a.id === result.id) ? prev : [...prev, result],
          );
        }
        return result;
      };
    }, [onUploadFile]);

    // 喂给 AttachmentDownloadProvider 的合并列表。调用方提供的附件（从服务端
    // 预载完整 attachments[] 的 issue/comment 编辑器）优先——我们只追加调用方
    // 没有的会话上传，父组件重渲染带上同一记录也不会出现两份。
    //
    // id 冲突的一个例外：调用方副本的 `download_url` 为空时（create-issue
    // 草稿持久化前会剥掉短时效签名 URL），用会话上传补上。会话副本持有本次
    // 响应的签名 URL，刚粘贴的图片首帧就能从中绘制，而不是经 `markdown_url`
    // 多一次重定向跳转。
    const providerAttachments = useMemo(() => {
      if (sessionUploads.length === 0) return attachments;
      const sessionById = new Map(sessionUploads.map((a) => [a.id, a]));
      const merged: Attachment[] = [];
      for (const a of attachments ?? []) {
        const session = a.id ? sessionById.get(a.id) : undefined;
        if (session) sessionById.delete(a.id);
        merged.push(
          session && !a.download_url
            ? { ...a, download_url: session.download_url }
            : a,
        );
      }
      merged.push(...sessionById.values());
      return merged;
    }, [attachments, sessionUploads]);

    // 当前工作区 slug 放在 ref 里，点击处理器总能看到最新值而无需重建编辑器。
    // openLink 用它给缺工作区 slug 的旧式 /issues/... 路径补前缀。
    const workspaceSlug = useWorkspaceSlug();
    const workspaceSlugRef = useRef(workspaceSlug);
    workspaceSlugRef.current = workspaceSlug;

    // 本部署应用源同理：它告诉 openLink 哪些绝对 URL 指向本应用、必须站内
    // 路由而不是打开系统浏览器。
    const appOrigin = useAppOrigin();
    const appOriginRef = useRef(appOrigin);
    appOriginRef.current = appOrigin;

    // 不重建编辑器地保持 ref 同步
    onUpdateRef.current = onUpdate;
    onSubmitRef.current = onSubmit;
    onBlurRef.current = onBlur;
    onReadyRef.current = onReady;
    onUploadingChangeRef.current = onUploadingChange;
    onUploadFileRef.current = wrappedOnUploadFile;
    pasteAsFileThresholdRef.current = pasteAsFileThreshold;
    mentionContextItemsRef.current = mentionContextItems ?? [];
    quickActionMenuRef.current = quickActionMenu;
    flushPendingOnUnmountRef.current = flushPendingOnUnmount;

    const queryClient = useQueryClient();

    // Linear 风格裸标识符自动链接 resolver。完全惰性——只在用户输入时跑，
    // 绝不在渲染时跑，因此不给这个广泛使用的组件加任何 query hook。它从查询
    // 缓存（经 slug ref）读当前工作区；工作区外、非标识符 token、或前缀匹配
    // 不上本工作区时返回 null，所以那些情况不发网络请求；精确匹配过滤保证
    // 正确性。
    const resolveIssueIdentifierRef = useRef<IssueIdentifierResolver | undefined>(
      undefined,
    );
    resolveIssueIdentifierRef.current = async (identifier) => {
      if (!isIssueIdentifier(identifier)) return null;
      const slug = workspaceSlugRef.current;
      if (!slug) return null;
      const workspaces = await queryClient.fetchQuery(workspaceListOptions());
      const ws = workspaces.find((w) => w.slug === slug);
      if (!ws) return null;
      const prefix = ws.issue_prefix;
      if (
        prefix &&
        !identifier.toUpperCase().startsWith(`${prefix.toUpperCase()}-`)
      ) {
        return null;
      }
      const issue = await queryClient.fetchQuery(
        issueIdentifierOptions(ws.id, identifier),
      );
      return issue ? { id: issue.id, identifier: issue.identifier } : null;
    };

    const initialMarkdown = value ?? defaultValue ?? "";
    const initialContent = initialMarkdown
      ? // 一次性读取：编辑器在载入时对初始文档预处理一次。与只读渲染器不同，
        // CDN 配置后来落地时不需要重跑——从此文档由用户自己的编辑驱动。
        preprocessMarkdown(initialMarkdown, {
          cdnDomain: configStore.getState().cdnDomain,
        })
      : "";
    // `immediatelyRender: false` 下 Tiptap 实例在挂载后创建，同一 tick 触发的
    // 命令式 `focus()`（如 chat 给全新会话自动聚焦）会撞上 null 编辑器而
    // no-op。把意图锁在这里，待 `onCreate` 里编辑器存在后再兑现。
    const focusOnReadyRef = useRef(false);
    // 大 markdown 分块解析以避开 marked 的 O(n²) tokenizer（见
    // parseMarkdownChunked）。小文档走单次解析快路径。
    const mountChunked = initialContent.length > MARKDOWN_CHUNK_THRESHOLD;

    const editor = useEditor({
      immediatelyRender: false,
      // 显式说明——真正的性能收益在 BubbleMenu 的 useEditorState。
      shouldRerenderOnTransaction: false,
      onCreate: ({ editor: ed }) => {
        // 大文档下面先空挂载、再分块解析，让 O(n²) 的 marked tokenizer 永远
        // 不会一次看到整份文档。
        if (mountChunked) {
          const manager = (
            ed.storage as { markdown?: { manager?: MarkdownManagerLike } }
          ).markdown?.manager;
          if (manager) {
            ed.commands.setContent(
              parseMarkdownChunked(manager, initialContent),
              { emitUpdate: false },
            );
          } else {
            ed.commands.setContent(initialContent, {
              emitUpdate: false,
              contentType: "markdown",
            });
          }
        }
        // 以空列表项结尾的 markdown 草稿（如敲完 `1.` 留下的 `"1. \n\n"`）
        // 会解析出无光标、不合 schema 的项；修复它，让挂载好的编辑器在列表
        // 里有一个真实光标。
        repairEmptyListItems(ed);
        lastEmittedRef.current = normalizeEditorMarkdown(ed);
        if (focusOnReadyRef.current) {
          focusOnReadyRef.current = false;
          ed.commands.focus("end");
        }
      },
      content: mountChunked ? "" : initialContent,
      contentType: mountChunked
        ? undefined
        : initialMarkdown
          ? "markdown"
          : undefined,
      extensions: createEditorExtensions({
        placeholder: () => placeholderRef.current,
        queryClient,
        onSubmitRef,
        onUploadFileRef,
        pasteAsFileThresholdRef,
        disableMentions,
        mentionMode,
        getMentionContextItems: () => mentionContextItemsRef.current,
        enableSlashCommands,
        slashCommandMode,
        quickActionMenu: {
          getQuickActions: () => quickActionMenuRef.current?.getQuickActions?.() ?? [],
          renderQuickAction: (id: string) =>
            quickActionMenuRef.current?.renderQuickAction?.(id) ?? Promise.resolve(""),
          onRenderError: (error: unknown) =>
            quickActionMenuRef.current?.onRenderError?.(error),
        },
        resolveIssueIdentifierRef,
      }),
      onUpdate: ({ editor: ed }) => {
        if (!onUpdateRef.current) return;
        pendingBaseRef.current = documentBaseRef.current;
        if (flushPendingOnUnmountRef.current) {
          pendingFlushRef.current = normalizeEditorMarkdown(ed);
        }
        if (debounceRef.current) clearTimeout(debounceRef.current);
        debounceRef.current = setTimeout(() => {
          debounceRef.current = undefined;
          pendingFlushRef.current = null;
          const base = pendingBaseRef.current ?? documentBaseRef.current;
          pendingBaseRef.current = null;
          const md = normalizeEditorMarkdown(ed);
          if (md === lastEmittedRef.current) return;
          lastEmittedRef.current = md;
          onUpdateRef.current?.(md, base);
        }, debounceMs);
      },
      onBlur: () => {
        onBlurRef.current?.();
      },
      editorProps: {
        handleDOMEvents: {
          click(_view, event) {
            const target = event.target as HTMLElement;
            // 跳过 NodeView 包裹层里的链接——它们自己处理点击
            if (target.closest("[data-node-view-wrapper]")) return false;

            const link = target.closest("a");
            const href = link?.getAttribute("href");
            if (!href || isMentionHref(href)) return false;

            event.preventDefault();
            openLink(
              href,
              workspaceSlugRef.current,
              appOriginRef.current,
              resolveClickIntent(event),
            );
            return true;
          },
          // 中键永不产生 `click` 事件。让它走与修饰键点击相同的路径，从而在
          // 后台页签打开。
          auxclick(_view, event) {
            if (event.button !== 1) return false;
            const target = event.target as HTMLElement;
            if (target.closest("[data-node-view-wrapper]")) return false;
            const href = target.closest("a")?.getAttribute("href");
            if (!href || isMentionHref(href)) return false;
            event.preventDefault();
            openLink(
              href,
              workspaceSlugRef.current,
              appOriginRef.current,
              "background-tab",
            );
            return true;
          },
        },
        attributes: {
          class: cn("flex-1 rich-text-editor text-body outline-none", className),
        },
      },
    });

    // 告诉宿主：延迟创建的编辑器实例现已存在。从被动 effect（而非 `onCreate`）
    // 触发，跑在 <EditorContent> 挂接编辑器 DOM 的 commit 之后——调用方可对它
    // 测量/聚焦。
    const readyFiredRef = useRef(false);
    useEffect(() => {
      if (!editor || readyFiredRef.current) return;
      readyFiredRef.current = true;
      onReadyRef.current?.();
    }, [editor]);

    // 把上传队列的变化发布给宿主，让它能门禁提交。
    //
    // 刻意不从 `onUpdate` 推导：那条路径有防抖、还会丢弃与上次相同 markdown
    // 的发射——而失败的**上传会移除它的占位符、留下逐字节相同的 markdown**
    //（blob URL 从一开始就被剥掉了），于是解除门禁永远不会触发。事务无论序列
    // 化出什么都会携带 attr 翻转。
    useEffect(() => {
      if (!editor || !onUploadingChange) return;
      // 订阅时**无条件**发布当前答案，之后只在翻转时发布。宿主状态活得过任何
      // 单个编辑器实例——评论编辑取消时卸载编辑器、重进时挂新的，chat 在
      // agent 变化时按 `key` 换编辑器。中途被拆掉的编辑器会带走它的挂起节点，
      // 于是仍持有 `uploading: true` 的宿主没有任何东西能解除门禁：因为新实例
      // 也读「没在上传」就跳过这首次发射，正是提交被永久卡死的成因。
      //
      // `last` 按订阅存储而非放 ref，理由相同：翻转跟踪绝不能活过它所描述
      // 的实例。
      let last = hasUploadingNode(editor);
      onUploadingChangeRef.current?.(last);
      const check = () => {
        if (editor.isDestroyed) return;
        const uploading = hasUploadingNode(editor);
        if (uploading === last) return;
        last = uploading;
        onUploadingChangeRef.current?.(uploading);
      };
      editor.on("transaction", check);
      return () => {
        editor.off("transaction", check);
      };
      // `onUploadingChange` 只按存在性读取；实时回调由 ref 携带，宿主传行内
      // 箭头也不会重复绑定。
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [editor, !!onUploadingChange]);

    // 卸载清理。挂起的防抖更新默认被丢弃而不是冲刷——原因见
    // `flushPendingOnUnmount` prop 文档。属主选开时，输出 `onUpdate` 时刻缓存
    // 的 markdown，长防抖不能在外层弹窗关闭时吞掉最后一次编辑。
    useEffect(() => {
      return () => {
        if (!debounceRef.current) return;
        clearTimeout(debounceRef.current);
        debounceRef.current = undefined;
        if (!flushPendingOnUnmountRef.current) return;
        const pending = pendingFlushRef.current;
        const base = pendingBaseRef.current ?? documentBaseRef.current;
        pendingFlushRef.current = null;
        pendingBaseRef.current = null;
        if (pending === null || pending === lastEmittedRef.current) return;
        lastEmittedRef.current = pending;
        onUpdateRef.current?.(pending, base);
      };
    }, []);

    // 用外部 markdown 替换实时文档。被下面同步 `value` effect 与命令式
    // `adoptContent` 共用，两条路径落地内容的方式一致——大文档分块解析、
    // 不回显 onUpdate、保留光标。是否应用由调用方决定；守卫住在 effect 里、
    // 不在这里。
    const applyExternalContent = useCallback(
      (markdown: string) => {
        if (!editor || editor.isDestroyed) return;
        documentBaseRef.current = normalizeMarkdown(markdown);
        const before = normalizeEditorMarkdown(editor);

        // 受控宿主通常会回显本编辑器刚发出的那串 markdown。在预处理前认出
        // 这种确认：预处理半个未打完的 token 会改变它的含义（例如防抖暂停
        // 期间 `dev.de` 曾变成链接）。
        if (normalizeMarkdown(markdown) === before) return;

        const incoming = markdown
          ? preprocessMarkdown(markdown, {
              cdnDomain: configStore.getState().cdnDomain,
            })
          : "";
        const incomingNormalized = normalizeMarkdown(incoming);
        // 规范化后相等的短路。避免预处理输入序列化出来就是屏上文档时产生
        // 一次 no-op 事务。
        if (incomingNormalized === before) return;

        // `emitUpdate: false`。Tiptap v3 的 setContent 默认 `emitUpdate:
        // true`；不设会重新触发 onUpdate → 服务端保存 → 自写循环。
        const { from, to } = editor.state.selection;
        // WS 驱动的大描述重解析走同一分块路径。
        const manager =
          incoming.length > MARKDOWN_CHUNK_THRESHOLD
            ? (editor.storage as { markdown?: { manager?: MarkdownManagerLike } })
                .markdown?.manager
            : undefined;
        if (manager) {
          editor.commands.setContent(parseMarkdownChunked(manager, incoming), {
            emitUpdate: false,
          });
        } else {
          editor.commands.setContent(incoming, {
            emitUpdate: false,
            contentType: "markdown",
          });
        }

        // 外来 markdown 里的空列表项会解析成无光标、不合 schema 的节点；
        // 修复它并让它拥有光标。否则把旧选区钳到新文档尺寸，让 ProseMirror
        // 替换文档后光标不会弹回位置 0。
        if (!repairEmptyListItems(editor, { from, to })) {
          const docSize = editor.state.doc.content.size;
          editor.commands.setTextSelection({
            from: Math.min(from, docSize),
            to: Math.min(to, docSize),
          });
        }

        lastEmittedRef.current = normalizeEditorMarkdown(editor);
      },
      [editor],
    );

    // 把显式外部 `value` 变化同步进编辑器。`defaultValue` 刻意只挂载时读，
    // 符合 React 普通非受控输入契约；草稿因此不会把自己的防抖回写喂回实时
    // 编辑器。
    // Tiptap v3 `useEditor` 只在挂载时读 `content`（ueberdosis/tiptap#5831）；
    // 没有本 effect，WS 驱动的描述更新会让编辑器一直显示过期内容，直到 issue
    // 关闭再重开。
    useEffect(() => {
      if (!editor || editor.isDestroyed || value === undefined) return;

      const previousValue = lastSyncedValueRef.current;
      lastSyncedValueRef.current = value;

      // 初始值已经经 useEditor 的 `content` 选项（或 chunked 路径的 onCreate）
      // 解析过。把那份源 markdown 与 Tiptap 的规范序列化相比，即使表示同一
      // 文档也可能不同，过去会造成立刻的第二次全量解析。只有后来的 prop
      // 变化才属于本同步 effect。
      if (value === previousValue) return;

      // 守卫 0：绝不覆盖在途上传。外部 `value` 变化可能在上传中途到达——例如
      // chat 在首次上传文件时惰性创建会话，翻转 `activeSessionId` → 草稿 key
      // → `value`。若对仍持有 `uploading` image/fileCard 节点的文档做
      // `setContent`，该节点被抹掉、上传收尾再也找不到它（文件消失，留下空的
      // `!file[name]()`）。与下面脏守卫一样，上传中节点是外部同步不得覆盖的
      // 本地状态。
      //
      // 注意：它（与这里的每个守卫一样）对本次 value **永久跳过**同步——
      // `lastSyncedValueRef` 已前进，effect 不会为它重跑。仍必须在此内容上
      // 落地的宿主得显式说出来——经 `adoptContent`。
      if (hasUploadingNode(editor)) return;

      const current = normalizeEditorMarkdown(editor);
      // 「脏」= 用户有尚未经防抖 `onUpdate` 冲刷出去的本地编辑。
      // `lastEmittedRef` 只在防抖触发后前进，所以一旦分歧说明编辑器持有未保存
      // 字节。
      const isDirty =
        lastEmittedRef.current !== null && current !== lastEmittedRef.current;

      // 守卫 1：聚焦且脏——保护用户正在敲的字节。聚焦但干净则继续往下走：
      // 应用 setContent 是安全的（没有用户输入可丢）且必要，因为 onBlur 没有
      // 重放机制，聚焦且干净的编辑器否则会永久丢掉这次同步。
      if (editor.isFocused && isDirty) return;

      // 守卫 2：失焦但脏——blur 发生了但防抖窗口（debounceMs，描述是 1500ms）
      // 还没冲刷。挂起的 onUpdate 会到达服务端、缓存会调和；这里跳过可以避免
      // 覆盖未保存的本地编辑。
      if (isDirty) return;

      applyExternalContent(value);
    }, [value, editor, applyExternalContent]);

    // 把外部 `placeholder` 变化同步进已挂载编辑器。
    // Placeholder 扩展配置为对 `placeholderRef` 的 getter（见上面
    // createEditorExtensions），插件每次重算装饰都会重调它。更新 ref 后派发
    // 一个空事务强制重算——占位文本无需重挂即刷新。没有它，占位文本会冻结在
    // 挂载值：同一 agent 下在归档与活跃会话间切换（不重挂编辑器）会让输入框
    // 一直停在「此会话已归档」。
    useEffect(() => {
      if (placeholderRef.current === placeholderText) return;
      placeholderRef.current = placeholderText;
      if (!editor || editor.isDestroyed) return;
      // 空事务 `docChanged` 为 false，onUpdate 不会触发，不会造成自写循环。
      editor.view.dispatch(editor.state.tr);
    }, [editor, placeholderText]);

    useImperativeHandle(ref, () => ({
      // 刻意不经过 `normalizeMarkdown`——见 content-editor.test.tsx 里
      // 「保持不 trim」的安全网。过去它也过 `stripBlobUrls`；那个包装已移除，
      // 因为在途占位符如今根本不再序列化，比事后擦掉它更强。
      getMarkdown: () => editor?.getMarkdown() ?? "",
      clearContent: () => {
        editor?.commands.clearContent();
      },
      focus: () => {
        if (editor) editor.commands.focus();
        // 编辑器还没挂载——把聚焦推迟到 `onCreate`。
        else focusOnReadyRef.current = true;
      },
      focusAtCoords: (coords: { x: number; y: number }) => {
        if (!editor) {
          // 编辑器还没挂载——降级为锁定的普通聚焦。
          focusOnReadyRef.current = true;
          return;
        }
        const pos = editor.view.posAtCoords({ left: coords.x, top: coords.y });
        if (pos) editor.commands.focus(pos.pos);
        else editor.commands.focus("end");
      },
      focusAtAnchor: (anchor: TextAnchor) => {
        if (!editor) {
          // 编辑器还没挂载——降级为锁定的普通聚焦。
          focusOnReadyRef.current = true;
          return;
        }
        editor.commands.focus(posFromAnchor(editor.state.doc, anchor));
      },
      blur: () => {
        editor?.commands.blur();
      },
      uploadFile: (file: File) => {
        if (!editor || !onUploadFileRef.current) return;
        const endPos = editor.state.doc.content.size;
        uploadAndInsertFile(editor, file, onUploadFileRef.current, endPos);
      },
      hasActiveUploads: () => (editor ? hasUploadingNode(editor) : false),
      insertUploadPlaceholder: (upload) => {
        if (!editor || editor.isDestroyed) return false;
        return insertUploadPlaceholder(editor, upload);
      },
      settleUploadPlaceholder: (uploadId, result) => {
        if (!editor || editor.isDestroyed) return false;
        return settleUploadNode(editor, uploadId, result);
      },
      insertMarkdownAtEnd: (markdown: string) => {
        if (!editor || editor.isDestroyed) return false;
        editor.commands.insertContentAt(editor.state.doc.content.size, markdown, {
          contentType: "markdown",
        });
        return true;
      },
      flushPendingUpdate: () => {
        // 没有武装定时器 = 上次发射后没有输入。编辑器已干净，宿主没有可重路由
        // 的内容。
        if (!debounceRef.current) return null;
        clearTimeout(debounceRef.current);
        debounceRef.current = undefined;
        pendingFlushRef.current = null;
        pendingBaseRef.current = null;
        if (!editor || editor.isDestroyed) return null;
        // 读实时文档：与卸载冲刷不同，实例在这里还活着，所以这是最新鲜的
        // 副本。
        const md = normalizeEditorMarkdown(editor);
        if (md === lastEmittedRef.current) return null;
        // 推进发射水位让编辑器读起来干净——宿主正为这些字节负责，脏守卫现在
        // 必须放行进来的同步 `value`。
        lastEmittedRef.current = md;
        return md;
      },
      adoptContent: (markdown: string) => applyExternalContent(markdown),
    }));

    // 链接悬停卡片——BubbleMenu 活跃（有选区）时禁用
    const wrapperRef = useRef<HTMLDivElement>(null);
    const hoverDisabled = !editor?.state.selection.empty;
    const hover = useLinkHover(wrapperRef, hoverDisabled);

    const handleContainerMouseDown = (event: ReactMouseEvent<HTMLDivElement>) => {
      if (!editor) return;

      const target = event.target as HTMLElement;
      if (target.closest(".ProseMirror")) return;
      if (target.closest("a, button, input, textarea, [role='button'], [data-node-view-wrapper]")) return;

      event.preventDefault();
      editor.commands.focus("end");
    };

    if (!editor) return null;

    return (
      <AttachmentDownloadProvider attachments={providerAttachments}>
        <div
          ref={wrapperRef}
          className="relative flex flex-1 min-h-full flex-col"
          onMouseDown={handleContainerMouseDown}
        >
          <EditorContent className="flex flex-1 flex-col" editor={editor} />
          {showBubbleMenu && (
            <EditorBubbleMenu editor={editor} currentIssueId={currentIssueId} />
          )}
          <LinkHoverCard {...hover} />
        </div>
      </AttachmentDownloadProvider>
    );
  },
);

export { ContentEditor, type ContentEditorProps, type ContentEditorRef };

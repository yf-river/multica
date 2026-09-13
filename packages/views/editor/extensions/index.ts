/**
 * ContentEditor 的共享扩展工厂。
 *
 * 一个函数同时装配「可编辑」与「只读」两种模式的扩展数组。这保证了视觉
 * 一致性——同一套扩展在两种模式下对内容做相同的解析与渲染。
 *
 * 拆分：
 * - 两种模式都有：StarterKit、CodeBlock、Link、Image、Table、Markdown、Mention
 * - 仅编辑模式：Typography、Placeholder、markdownPaste、submitShortcut、
 *   fileUpload、Mention suggestion 弹层
 *
 * Link 配置不同：编辑模式开 autolink（打字时识别 URL），只读模式不开
 * （避免显示时误报）。
 *
 * Mention suggestion 只挂在编辑模式——只读不需要自动补全弹层。
 *
 * 所有链接样式都由 styles/prose.css 控制（var(--brand) 颜色），而不是
 * Tailwind HTMLAttributes，以保持单一事实来源。
 */
import type { RefObject } from "react";
import StarterKit from "@tiptap/starter-kit";
import CodeBlockLowlight from "@tiptap/extension-code-block-lowlight";
import Placeholder from "@tiptap/extension-placeholder";
import Link from "@tiptap/extension-link";
import Typography from "@tiptap/extension-typography";
import Image from "@tiptap/extension-image";
import TableRow from "@tiptap/extension-table-row";
import TableHeader from "@tiptap/extension-table-header";
import TableCell from "@tiptap/extension-table-cell";
import { Table } from "@tiptap/extension-table";
import { TaskList } from "@tiptap/extension-list";
import { Markdown } from "@tiptap/markdown";
import { ReactNodeViewRenderer } from "@tiptap/react";
import type { AnyExtension } from "@tiptap/core";
import type { UploadResult } from "@multica/core/hooks/use-file-upload";
import { shouldAutoLink } from "@multica/ui/markdown";
import { escapeMarkdownLabel } from "../utils/escape-markdown-label";
import { BaseMentionExtension } from "./mention-extension";
import { createMentionSuggestion, type MentionItem } from "./mention-suggestion";
import {
  createIssueIdentifierAutolinkExtension,
  type IssueIdentifierResolver,
} from "./issue-identifier-autolink";
import { SlashCommandExtension } from "./slash-command-extension";
import { createSlashCommandSuggestion, createBuiltinCommandSuggestion } from "./slash-command-suggestion";
import type { BuiltinCommandSuggestionOptions } from "./slash-command-suggestion";
import { SuggestionTriggerArmingExtension } from "./suggestion-trigger-arming";
import { CodeBlockView } from "./code-block-view";
import { PatchedListItem, PatchedTaskItem } from "./list-item";
import { createMarkdownPasteExtension } from "./markdown-paste";
import { createMarkdownCopyExtension } from "./markdown-copy";
import { createBlurShortcutExtension } from "./blur-shortcut";
import { createSubmitShortcutExtension } from "./submit-shortcut";
import { createFileUploadExtension } from "./file-upload";
import { FileCardExtension } from "./file-card";
import { ImageView } from "./image-view";
import { BlockMathExtension, InlineMathExtension } from "./math";
import { HighlightExtension } from "./highlight";
import { codeLowlight } from "../syntax-highlight";

/**
 * LinkExtension——编辑器链接扩展（打字自动识别 URL，点击不自动打开）。
 */
const LinkExtension = Link.extend({ inclusive: false }).configure({
  openOnClick: false,
  autolink: true,
  linkOnPaste: true,
  defaultProtocol: "https",
  shouldAutoLink,
});

/**
 * ImageExtension——编辑器图片节点。
 *
 * 在 @tiptap/extension-image 之上加「上传中占位」「按上传 id 找回节点」与
 * 「固有尺寸」三个 attrs，并用 ImageView 做节点视图。
 */
export const ImageExtension = Image.extend({
  addAttributes() {
    return {
      ...this.parent?.(),
      uploading: {
        default: false,
        renderHTML: (attrs: Record<string, unknown>) =>
          attrs.uploading ? { "data-uploading": "" } : {},
        parseHTML: (el: HTMLElement) => el.hasAttribute("data-uploading"),
      },
      // 该占位符所属的上传——与草稿 store 里的 `clientUploadId` 是同一个值。
      // 让抵达于「并非它发起的挂载」上的 settle 能找到要替换的节点。仅渲染
      // 用，节点持有真实 URL 后即被清空。
      uploadId: {
        default: null,
        rendered: false,
      },
      // 上传时捕获的固有像素尺寸（见 file-upload.ts）。浏览器用 <img> 上的
      // width/height 计算宽高比并在解码前预留占位框，因此插入图片不会引起
      // 布局跳动（插入后的 scrollIntoView 也因此保持正确）。不序列化进
      // markdown——`renderMarkdown` 只输出 src/alt/title——往返保持干净。
      width: {
        default: null,
        renderHTML: (attrs: Record<string, unknown>) =>
          attrs.width ? { width: attrs.width as number } : {},
        parseHTML: (el: HTMLElement) => {
          const w = parseInt(el.getAttribute("width") || "", 10);
          return Number.isFinite(w) ? w : null;
        },
      },
      height: {
        default: null,
        renderHTML: (attrs: Record<string, unknown>) =>
          attrs.height ? { height: attrs.height as number } : {},
        parseHTML: (el: HTMLElement) => {
          const h = parseInt(el.getAttribute("height") || "", 10);
          return Number.isFinite(h) ? h : null;
        },
      },
    };
  },
  addNodeView() {
    return ReactNodeViewRenderer(ImageView);
  },
  renderMarkdown: (node: any) => {
    const src = node.attrs?.src || "";
    // 与 fileCard 同一条规则：进行中的占位符不是内容。它的 src 是进程级
    // `blob:` URL，刷新即失效，写出来会让持久化草稿里躺着死链接。这段逻辑
    // 取代了 ContentEditor 过去每次序列化后跑的正则清洗。
    if (node.attrs?.uploading === true || !src) return "";
    const alt = escapeMarkdownLabel(node.attrs?.alt || "");
    const title = node.attrs?.title;
    if (title) {
      return `![${alt}](${src} "${title}")`;
    }
    return `![${alt}](${src})`;
  },
}).configure({
  inline: false,
  allowBase64: false,
});

/**
 * createEditorExtensions 的装配选项（各字段含义见下）。
 */
export interface EditorExtensionsOptions {
  /**
   * 占位文本，或它的 getter。值在编辑器生命周期内可能变化时优先用 getter：
   * Tiptap 的 Placeholder 会在挂载时快照字符串选项，但对函数选项会在每次
   * 重算装饰时重新调用——所以 getter（配一次空事务触发）可以让占位文本
   * 不重挂编辑器就实时更新。见 ContentEditor。
   */
  placeholder?: string | (() => string);
  queryClient?: import("@tanstack/react-query").QueryClient;
  onSubmitRef?: RefObject<(() => void) | undefined>;
  onUploadFileRef?: RefObject<
    ((file: File, uploadId: string) => Promise<UploadResult | null>) | undefined
  >;
  /**
   * 纯文本粘贴超过该字符数时，改为作为 .txt 附件上传而非文档文本。
   * 用 ref 以便值变化时不用重建编辑器。缺省（默认）保持所有粘贴为文本。
   */
  pasteAsFileThresholdRef?: RefObject<number | undefined>;
  /**
   * 为 true 时不挂 `@` 建议弹层。mention 节点类型仍注册在 schema 里，这样
   * 从另一个 Multica 编辑器粘贴进来的 mention 会渲染成普通 mention 胶囊，
   * 而不是被 ProseMirror 的 schema 检查静默丢弃。用于「新建 mention 没有
   * 业务意义、但保留既有 mention 仍然重要」的编辑器（如 agent 系统提示词）。
   */
  disableMentions?: boolean;
  /** 覆盖 @ 行为：聊天上下文的建议模式。 */
  mentionMode?: "default" | "context";
  getMentionContextItems?: () => MentionItem[];
  /** 为 true 时挂 `/` 弹层。默认 false。 */
  enableSlashCommands?: boolean;
  /**
   * enableSlashCommands 为 true 时挂哪种 `/` 菜单：
   * - "skill"（默认）——聊天选择器，列出当前 agent 的技能。
   * - "command"——固定内建命令菜单（issue 评论），如 /note。
   */
  slashCommandMode?: "skill" | "command";
  /**
   * "command" 型 `/` 菜单里提供的快捷动作，外加把选择解析成它将发布的文本
   * 的 resolver（MUL-5465）。两者都是函数，编辑器只创建一次仍能读到实时
   * 数据；React Query 访问权在装配层。没有 issue 上下文的 composer 可省略。
   */
  quickActionMenu?: BuiltinCommandSuggestionOptions;
  /**
   * Linear 风格裸 issue 标识符自动链接的 resolver。提供它（且 mention 启用）
   * 时，在 `MUL-123` 后敲下边界字符、或粘贴含标识符的文本会解析并换成真
   * issue mention。用 ref 使编辑器只创建一次、resolver 读取实时工作区
   * 上下文；React Query + 工作区访问权在装配层。
   */
  resolveIssueIdentifierRef?: RefObject<IssueIdentifierResolver | undefined>;
}

/**
 * 装配 ContentEditor 的扩展数组（编辑与只读共用同一工厂）。
 * @param options 见 EditorExtensionsOptions。
 */
export function createEditorExtensions(
  options: EditorExtensionsOptions,
): AnyExtension[] {
  const { placeholder: placeholderText } = options;

  return [
    StarterKit.configure({
      // Markdown 能表达的所有标题级。Markdown 解析器保留 `#`…`######` 的
      // 源深度，但 Heading.renderHTML 对没配过的级数会回退到 `levels[0]`——
      // 于是 `levels: [1, 2, 3]` 会让编辑器把每个 H4–H6 画成 H1（MUL-6060）。
      // 同一列表也驱动 parseHTML，因此它也决定粘贴进来的 `<h4>` 是否作为
      // 标题存活。这里说的是把内容已有的标题正确渲染出来；气泡菜单仍只把
      // H1–H3 提供给创作者选择。
      heading: { levels: [1, 2, 3, 4, 5, 6] },
      link: false,
      codeBlock: false,
      // Underline 没有 Markdown 表达。Tiptap 扩展把它序列化成 `++text++`，
      // 不是 CommonMark/GFM，所以 ReadonlyContent（react-markdown +
      // remark-gfm）会把定界符渲染成字面量。禁用该扩展让下划线 mark 在解析
      // 时被丢弃：粘贴来的 `<u>` / `text-decoration: underline` 保留文字，
      // Cmd+U 变成 no-op，而不是产生展示层渲染不出的内容。
      underline: false,
      // 禁用 StarterKit 自带的 ListItem——它的 Enter 键位只绑 `splitListItem`，
      // 会把用户困在空的顶层列表项里（见 list-item.ts）。下面 PatchedListItem
      // 恢复标准的 split → lift 兜底链。
      listItem: false,
    }),
    PatchedListItem,
    // 勾选式任务列表：`- [ ]` / `- [x]`。TaskList + TaskItem 自带 markdown
    // tokenizer / renderMarkdown、输入规则（敲 `[] ` / `[x] `）与复选框
    // NodeView。taskList tokenizer 先于 marked 内建列表 tokenizer 被咨询，
    // 因此 `- [ ]` 变成任务项，而普通 `- ` 仍落进 PatchedListItem 的圆点列表。
    TaskList,
    PatchedTaskItem,
    CodeBlockLowlight.extend({
      addNodeView() {
        return ReactNodeViewRenderer(CodeBlockView);
      },
    }).configure({ lowlight: codeLowlight }),
    // ⚠️ Link 必须排在本数组里 markdownPaste 之前。
    // linkOnPaste 依赖 Link 的 handlePaste 插件先触发；
    // markdownPaste 的 handlePaste 是总会返回 true 的兜底。
    LinkExtension,
    ImageExtension,
    // renderWrapper 会把表格包进 `<div class="tableWrapper">`（即可缩放
    // NodeView 输出的同一个包裹层），prose.css 用 `overflow-x: auto` 修饰它。
    // 没有它，宽表格就是一棵无法收缩到 min-content 以下的裸 <table>，
    // 横向滚动条会落到整页滚动容器上而不是表格自身。
    Table.configure({ resizable: false, renderWrapper: true }),
    TableRow,
    TableHeader,
    TableCell,
    BlockMathExtension,
    InlineMathExtension,
    HighlightExtension,
    // 3 空格缩进，让嵌套有序列表在 ReadonlyContent 的 CommonMark 下存活。
    Markdown.configure({ indentation: { style: "space", size: 3 } }),
    // 让 Cmd+C / Cmd+X / 拖拽把 Markdown 源码写进剪贴板 text/plain，
    // 用户能把富文本内容作为原始 Markdown 复制出去。
    createMarkdownCopyExtension(),
    FileCardExtension,
    // 必须排在 mention 与斜杠弹层前面：它提供「用户敲下了这个触发词」信号，
    // 弹层的 `shouldShow` 读它（MUL-5429）。
    SuggestionTriggerArmingExtension,
    BaseMentionExtension.configure({
      HTMLAttributes: { class: "mention" },
      ...(options.disableMentions
        ? { suggestion: { allow: () => false } }
        : options.queryClient
          ? { suggestion: createMentionSuggestion(options.queryClient, { mode: options.mentionMode, getContextItems: options.getMentionContextItems }) }
          : {}),
    }),
    // Linear 风格裸标识符 → issue mention。只在「给了 resolver 且允许创建
    // mention」时挂载（一个抑制新建 mention 的编辑器不应从标识符合成它们）。
    ...(!options.disableMentions && options.resolveIssueIdentifierRef
      ? [
          createIssueIdentifierAutolinkExtension({
            resolveRef: options.resolveIssueIdentifierRef,
          }),
        ]
      : []),
    SlashCommandExtension.configure({
      HTMLAttributes: { class: "slash-command" },
      suggestion: !options.enableSlashCommands
        ? { char: "/", allow: () => false }
        : options.slashCommandMode === "command"
          ? createBuiltinCommandSuggestion(options.quickActionMenu)
          : options.queryClient
            ? createSlashCommandSuggestion(options.queryClient)
            : { char: "/", allow: () => false },
    }),
    Typography,
    Placeholder.configure({ placeholder: placeholderText }),
    createMarkdownPasteExtension(),
    createSubmitShortcutExtension(() => {
      const fn = options.onSubmitRef?.current;
      if (!fn) return false;
      fn();
      return true;
    }),
    createBlurShortcutExtension(),
    createFileUploadExtension(options.onUploadFileRef!, options.pasteAsFileThresholdRef),
  ];
}

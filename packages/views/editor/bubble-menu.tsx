"use client";

/**
 * EditorBubbleMenu——选中文本时浮出的格式化工具条。
 *
 * 用 @floating-ui/dom（computePosition + autoUpdate）定位，并借
 * createPortal 挂到 document.body。这能脱离祖先链里所有 overflow 容器
 * （Card 的 overflow:hidden、可滚动容器等），同时 autoUpdate 监听每个祖先
 * 滚动容器，让菜单一直锚定选中内容。
 *
 * 关键设计决策：
 * - 虚拟引用上的 contextElement 告诉 Floating UI 到哪里找滚动祖先，
 *   让 hide middleware 能检测嵌套滚动容器的裁剪。
 * - 用 visibility:hidden（而非 display:none）保持元素可测量，computePosition
 *   首次显示时才能正确定尺寸。
 * - portal 根上的 onMouseDown preventDefault 防止菜单内所有点击把焦点
 *   从编辑器抢走。
 */

import { useState, useEffect, useCallback, useRef, useMemo } from "react";
import {
  computePosition,
  offset,
  flip,
  shift,
  hide,
  autoUpdate,
} from "@floating-ui/dom";
import { useEditorState } from "@tiptap/react";
import type { Editor } from "@tiptap/core";
import { posToDOMRect } from "@tiptap/core";
import { NodeSelection } from "@tiptap/pm/state";
import { toast } from "sonner";
import { useCreateIssue } from "@multica/core/issues/mutations";
import { useT } from "../i18n";
import { createShortcutChord, type ShortcutChord } from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { Toggle } from "@multica/ui/components/ui/toggle";
import { Separator } from "@multica/ui/components/ui/separator";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
  TooltipProvider,
} from "@multica/ui/components/ui/tooltip";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import {
  Bold,
  Italic,
  Strikethrough,
  Code,
  Highlighter,
  Link2,
  List,
  ListOrdered,
  ListTodo,
  Quote,
  ChevronDown,
  Check,
  X,
  Unlink,
  Type,
  Heading1,
  Heading2,
  Heading3,
  FilePlus,
  Loader2,
} from "lucide-react";

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

/** 判断当前选区是否应该显示气泡菜单。 */
function shouldShowBubbleMenu(editor: Editor): boolean {
  if (!editor.isEditable) return false;
  const { selection } = editor.state;
  if (selection.empty) return false;
  const { from, to } = selection;
  if (!editor.state.doc.textBetween(from, to).trim().length) return false;
  if (selection instanceof NodeSelection) return false;
  const $from = editor.state.doc.resolve(from);
  if ($from.parent.type.name === "codeBlock") return false;
  return true;
}

// ---------------------------------------------------------------------------
// Mark 开关按钮
// ---------------------------------------------------------------------------

type InlineMark = "bold" | "italic" | "strike" | "code" | "highlight";

const toggleMarkActions: Record<InlineMark, (editor: Editor) => void> = {
  bold: (e) => e.chain().focus().toggleBold().run(),
  italic: (e) => e.chain().focus().toggleItalic().run(),
  strike: (e) => e.chain().focus().toggleStrike().run(),
  code: (e) => e.chain().focus().toggleCode().run(),
  highlight: (e) => e.chain().focus().toggleHighlight().run(),
};

/** 单个行内 mark 的开关按钮（带快捷键 tooltip）。 */
function MarkButton({
  editor,
  mark,
  icon: Icon,
  label,
  shortcut,
  isActive,
}: {
  editor: Editor;
  mark: InlineMark;
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  shortcut: ShortcutChord;
  isActive: boolean;
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Toggle
            size="sm"
            aria-label={label}
            pressed={isActive}
            onPressedChange={() => toggleMarkActions[mark](editor)}
            onMouseDown={(e) => e.preventDefault()}
          />
        }
      >
        <Icon className="size-3.5" />
      </TooltipTrigger>
      <TooltipContent side="top" sideOffset={8}>
        {label}
        <ShortcutKeycaps shortcut={shortcut} className="ml-1.5" />
      </TooltipContent>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// URL 规范化
// ---------------------------------------------------------------------------

/** 能在浏览器里执行代码的协议——也是唯一要拦截的。 */
const DANGEROUS_PROTOCOL_RE = /^(javascript|data|vbscript):/i;
const HAS_PROTOCOL_RE = /^[a-z][a-z0-9+.-]*:\/?\/?/i;
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/**
 * 规范化用户输入的 URL：补协议、识别 mailto、拦截 XSS。
 *
 * 协议用黑名单（而非白名单）——只拦 `javascript:`、`data:`、`vbscript:`。
 * 其它协议全部放行，因为它们不能在浏览器里执行代码，在团队工具里是正当
 * 的深链目标（slack://、vscode://、figma://）。
 * Tiptap `setLink` 命令里的 `isAllowedUri` 提供了第二层安全网。
 */
function normalizeUrl(input: string): string {
  const trimmed = input.trim();
  if (!trimmed) return "";
  if (trimmed.startsWith("/")) return trimmed;
  if (DANGEROUS_PROTOCOL_RE.test(trimmed)) return "";
  if (HAS_PROTOCOL_RE.test(trimmed)) return trimmed;
  if (EMAIL_RE.test(trimmed)) return `mailto:${trimmed}`;
  if (trimmed.startsWith("//")) return `https:${trimmed}`;
  return `https://${trimmed}`;
}

// ---------------------------------------------------------------------------
// 链接编辑条
// ---------------------------------------------------------------------------

/** 链接编辑条：输入/修改当前选区链接的 URL，回车应用、Esc 关闭。 */
function LinkEditBar({
  editor,
  onClose,
}: {
  editor: Editor;
  onClose: () => void;
}) {
  const { t } = useT("editor");
  const existingHref = editor.getAttributes("link").href as string | undefined;
  const [url, setUrl] = useState(existingHref ?? "");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const t = setTimeout(() => inputRef.current?.focus(), 0);
    return () => clearTimeout(t);
  }, []);

  const apply = useCallback(() => {
    const href = normalizeUrl(url);
    if (!href) {
      editor.chain().focus().extendMarkRange("link").unsetLink().run();
    } else {
      editor.chain().focus().extendMarkRange("link").setLink({ href }).run();
    }
    onClose();
  }, [editor, url, onClose]);

  const remove = useCallback(() => {
    editor.chain().focus().extendMarkRange("link").unsetLink().run();
    onClose();
  }, [editor, onClose]);

  return (
    <div className="bubble-menu-link-edit" onMouseDown={(e) => e.preventDefault()}>
      <Input
        ref={inputRef}
        value={url}
        onChange={(e) => setUrl(e.target.value)}
        placeholder="https://..."
        aria-label={t(($) => $.bubble_menu.url_aria_label)}
        className="h-7 flex-1 text-caption"
        onKeyDown={(e) => {
          if (e.key === "Enter") { e.preventDefault(); apply(); }
          if (e.key === "Escape") { e.preventDefault(); onClose(); editor.commands.focus(); }
        }}
      />
      <Button
        size="icon-xs"
        variant="ghost"
        aria-label={t(($) => $.bubble_menu.link_edit.apply)}
        onClick={apply}
        onMouseDown={(e) => e.preventDefault()}
      >
        <Check className="size-3.5" />
      </Button>
      {existingHref && (
        <Button
          size="icon-xs"
          variant="ghost"
          aria-label={t(($) => $.bubble_menu.link_edit.remove)}
          onClick={remove}
          onMouseDown={(e) => e.preventDefault()}
        >
          <Unlink className="size-3.5" />
        </Button>
      )}
      <Button
        size="icon-xs"
        variant="ghost"
        aria-label={t(($) => $.bubble_menu.link_edit.close)}
        onClick={() => { onClose(); editor.commands.focus(); }}
        onMouseDown={(e) => e.preventDefault()}
      >
        <X className="size-3.5" />
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 标题下拉
// ---------------------------------------------------------------------------

/** 标题层级下拉（正文/H1-H3 切换）。 */
function HeadingDropdown({ editor, onOpenChange, activeLevel }: { editor: Editor; onOpenChange: (open: boolean) => void; activeLevel: number | undefined }) {
  const { t } = useT("editor");
  const [open, setOpen] = useState(false);
  const label = activeLevel ? `H${activeLevel}` : t(($) => $.bubble_menu.heading_dropdown.text);
  const items = [
    { label: t(($) => $.bubble_menu.heading_dropdown.normal_text), icon: Type, active: !activeLevel, action: () => editor.chain().focus().setParagraph().run() },
    { label: t(($) => $.bubble_menu.heading_dropdown.heading_1), icon: Heading1, active: activeLevel === 1, action: () => editor.chain().focus().toggleHeading({ level: 1 }).run() },
    { label: t(($) => $.bubble_menu.heading_dropdown.heading_2), icon: Heading2, active: activeLevel === 2, action: () => editor.chain().focus().toggleHeading({ level: 2 }).run() },
    { label: t(($) => $.bubble_menu.heading_dropdown.heading_3), icon: Heading3, active: activeLevel === 3, action: () => editor.chain().focus().toggleHeading({ level: 3 }).run() },
  ];

  const handleOpenChange = useCallback((next: boolean) => {
    setOpen(next);
    onOpenChange(next);
  }, [onOpenChange]);

  return (
    <Popover modal={false} open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger
        className="inline-flex h-7 items-center gap-0.5 rounded-md px-1.5 text-caption font-medium hover:bg-muted"
        onMouseDown={(e) => e.preventDefault()}
      >
        {label}
        <ChevronDown className="size-3" />
      </PopoverTrigger>
      <PopoverContent
        side="bottom"
        sideOffset={8}
        align="start"
        className="w-auto min-w-32 p-1"
        initialFocus={false}
        finalFocus={false}
      >
        {items.map((item) => (
          <button
            type="button"
            key={item.label}
            className="flex w-full cursor-default items-center gap-2 rounded-md px-1.5 py-1 text-caption outline-hidden select-none hover:bg-accent hover:text-accent-foreground"
            onMouseDown={(e) => {
              e.preventDefault();
              item.action();
              handleOpenChange(false);
            }}
          >
            <item.icon className="size-3.5" />
            {item.label}
            {item.active && <Check className="ml-auto size-3.5" />}
          </button>
        ))}
      </PopoverContent>
    </Popover>
  );
}

// ---------------------------------------------------------------------------
// 列表下拉
// ---------------------------------------------------------------------------

/** 列表下拉（圆点/有序/任务）。 */
function ListDropdown({ editor, onOpenChange, isBullet, isOrdered, isTask }: { editor: Editor; onOpenChange: (open: boolean) => void; isBullet: boolean; isOrdered: boolean; isTask: boolean }) {
  const { t } = useT("editor");
  const [open, setOpen] = useState(false);

  const handleOpenChange = useCallback((next: boolean) => {
    setOpen(next);
    onOpenChange(next);
  }, [onOpenChange]);

  return (
    <Popover modal={false} open={open} onOpenChange={handleOpenChange}>
      <Tooltip>
        <TooltipTrigger render={
          <PopoverTrigger
            className="inline-flex h-7 items-center gap-0.5 rounded-md px-1.5 text-caption font-medium hover:bg-muted aria-pressed:bg-muted"
            aria-label={t(($) => $.bubble_menu.list)}
            aria-pressed={isBullet || isOrdered || isTask}
            onMouseDown={(e) => e.preventDefault()}
          />
        }>
          <List className="size-3.5" />
          <ChevronDown className="size-3" />
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={8}>{t(($) => $.bubble_menu.list)}</TooltipContent>
      </Tooltip>
      <PopoverContent
        side="bottom"
        sideOffset={8}
        align="start"
        className="w-auto min-w-32 p-1"
        initialFocus={false}
        finalFocus={false}
      >
        <button
          type="button"
          className="flex w-full cursor-default items-center gap-2 rounded-md px-1.5 py-1 text-caption outline-hidden select-none hover:bg-accent hover:text-accent-foreground"
          onMouseDown={(e) => {
            e.preventDefault();
            editor.chain().focus().toggleBulletList().run();
            handleOpenChange(false);
          }}
        >
          <List className="size-3.5" /> {t(($) => $.bubble_menu.list_dropdown.bullet_list)}
          {isBullet && <Check className="ml-auto size-3.5" />}
        </button>
        <button
          type="button"
          className="flex w-full cursor-default items-center gap-2 rounded-md px-1.5 py-1 text-caption outline-hidden select-none hover:bg-accent hover:text-accent-foreground"
          onMouseDown={(e) => {
            e.preventDefault();
            editor.chain().focus().toggleOrderedList().run();
            handleOpenChange(false);
          }}
        >
          <ListOrdered className="size-3.5" /> {t(($) => $.bubble_menu.list_dropdown.ordered_list)}
          {isOrdered && <Check className="ml-auto size-3.5" />}
        </button>
        <button
          type="button"
          className="flex w-full cursor-default items-center gap-2 rounded-md px-1.5 py-1 text-caption outline-hidden select-none hover:bg-accent hover:text-accent-foreground"
          onMouseDown={(e) => {
            e.preventDefault();
            editor.chain().focus().toggleTaskList().run();
            handleOpenChange(false);
          }}
        >
          <ListTodo className="size-3.5" /> {t(($) => $.bubble_menu.list_dropdown.task_list)}
          {isTask && <Check className="ml-auto size-3.5" />}
        </button>
      </PopoverContent>
    </Popover>
  );
}

// ---------------------------------------------------------------------------
// 创建子 issue 按钮
// ---------------------------------------------------------------------------

/**
 * 把当前选区变成一个 `parentIssueId` 的子 issue，并用指向新 issue 的
 * mention 链接替换选区。标题取选中文本（trim、折叠空白、限长）。只在父
 * issue 在场时渲染；否则没有有意义的「某 issue 的子 issue」目标。
 */
function CreateSubIssueButton({
  editor,
  parentIssueId,
}: {
  editor: Editor;
  parentIssueId: string;
}) {
  const { t } = useT("editor");
  const createIssue = useCreateIssue();
  const [pending, setPending] = useState(false);

  const handleClick = useCallback(async () => {
    if (pending) return;
    const { from, to } = editor.state.selection;
    if (from === to) return;

    // 标题取自选区：折叠空白、限长。完整选区仍作为链接文本——只有 issue
    // 标题被限长。
    const rawTitle = editor.state.doc.textBetween(from, to, " ", " ").trim();
    const title = rawTitle.replace(/\s+/g, " ").slice(0, 200);
    if (!title) return;

    setPending(true);
    try {
      const newIssue = await createIssue.mutateAsync({
        title,
        parent_issue_id: parentIssueId,
      });
      editor
        .chain()
        .focus()
        .insertContentAt(
          { from, to },
          [
            {
              type: "mention",
              attrs: {
                id: newIssue.id,
                label: newIssue.identifier,
                type: "issue",
              },
            },
            { type: "text", text: " " },
          ],
        )
        .run();
      toast.success(t(($) => $.bubble_menu.sub_issue.created, { identifier: newIssue.identifier }));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.bubble_menu.sub_issue.create_failed),
      );
    } finally {
      setPending(false);
    }
  }, [editor, parentIssueId, createIssue, pending, t]);

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Toggle
            size="sm"
            aria-label={t(($) => $.bubble_menu.sub_issue.tooltip)}
            pressed={false}
            disabled={pending}
            onPressedChange={handleClick}
            onMouseDown={(e) => e.preventDefault()}
          />
        }
      >
        {pending ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <FilePlus className="size-3.5" />
        )}
      </TooltipTrigger>
      <TooltipContent side="top" sideOffset={8}>
        {t(($) => $.bubble_menu.sub_issue.tooltip)}
      </TooltipContent>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// 主气泡菜单——@floating-ui/dom + portal 到 body
// ---------------------------------------------------------------------------

/**
 * EditorBubbleMenu——选中文本的悬浮格式化工具条。
 */
function EditorBubbleMenu({
  editor,
  currentIssueId,
}: {
  editor: Editor;
  currentIssueId?: string;
}) {
  const { t } = useT("editor");
  const [visible, setVisible] = useState(false);
  const [mode, setMode] = useState<"toolbar" | "link-edit">("toolbar");
  const floatingRef = useRef<HTMLDivElement>(null);

  // 对格式化状态的精确订阅——只有这些值真的变化才重渲染，而不是每个事务
  // 都渲染。
  const fmt = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      bold: e.isActive("bold"),
      italic: e.isActive("italic"),
      strike: e.isActive("strike"),
      code: e.isActive("code"),
      highlight: e.isActive("highlight"),
      link: e.isActive("link"),
      blockquote: e.isActive("blockquote"),
      bulletList: e.isActive("bulletList"),
      orderedList: e.isActive("orderedList"),
      taskList: e.isActive("taskList"),
      // 存的是层级本身，而不是每个可选项一个布尔：schema 接受 h1-h6，
      // 光标可能坐在 Markdown 带进来的 H4-H6 里，下拉必须如实报告而不能
      // 落回「正文」。它仍然只把 H1-H3 作为选项（MUL-6060）。
      headingLevel: e.isActive("heading")
        ? (e.getAttributes("heading").level as number | undefined)
        : undefined,
    }),
  });

  // 追踪文本选区的虚拟引用。
  // contextElement 告诉 autoUpdate/hide 到哪里找滚动祖先。
  const virtualRef = useMemo(
    () => ({
      getBoundingClientRect: () => {
        if (editor.isDestroyed) return new DOMRect();
        const { from, to } = editor.state.selection;
        return posToDOMRect(editor.view, from, to);
      },
      contextElement: editor.view.dom,
    }),
    [editor],
  );

  // 按选区状态显示/隐藏
  useEffect(() => {
    const onTransaction = () => {
      if (!editor.isInitialized) return;
      setVisible(shouldShowBubbleMenu(editor));
    };
    editor.on("transaction", onTransaction);
    return () => { editor.off("transaction", onTransaction); };
  }, [editor]);

  // 失焦隐藏——防抖让焦点先安定下来（例如点击菜单时）
  useEffect(() => {
    const onBlur = () => {
      setTimeout(() => {
        if (editor.isDestroyed) return;
        const el = floatingRef.current;
        if (el && el.contains(document.activeElement)) return;
        if (editor.view.hasFocus()) return;
        setVisible(false);
      }, 0);
    };
    editor.on("blur", onBlur);
    return () => { editor.off("blur", onBlur); };
  }, [editor]);

  // 可见时用 autoUpdate 定位浮动元素
  useEffect(() => {
    const el = floatingRef.current;
    if (!visible || !el || !editor.isInitialized) return;

    const updatePosition = () => {
      computePosition(virtualRef, el, {
        strategy: "fixed",
        placement: "top",
        middleware: [offset(8), flip(), shift({ padding: 8 }), hide()],
      }).then(({ x, y, middlewareData }) => {
        if (!el.isConnected) return;
        const hidden = middlewareData.hide?.referenceHidden;
        el.style.visibility = hidden ? "hidden" : "visible";
        el.style.left = `${x}px`;
        el.style.top = `${y}px`;
      });
    };

    // autoUpdate 监听所有滚动祖先（经 contextElement）、resize 与动画帧——
    // 不需要手动滚动监听。
    const cleanup = autoUpdate(virtualRef, el, updatePosition);
    return cleanup;
  }, [visible, editor, virtualRef]);

  // 外部点击关闭
  useEffect(() => {
    if (!visible) return;
    const handle = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (editor.view.dom.contains(target)) return;
      if (floatingRef.current?.contains(target)) return;
      setVisible(false);
    };
    document.addEventListener("mousedown", handle);
    return () => document.removeEventListener("mousedown", handle);
  }, [visible, editor]);

  // 选区变化时重置模式
  useEffect(() => {
    const handler = () => setMode("toolbar");
    editor.on("selectionUpdate", handler);
    return () => { editor.off("selectionUpdate", handler); };
  }, [editor]);

  // Popover 关闭时把焦点还给编辑器
  const handleMenuOpenChange = useCallback(
    (open: boolean) => { if (!open) editor.commands.focus(); },
    [editor],
  );

  return (
    <div
      ref={floatingRef}
      style={{
        position: "fixed",
        zIndex: 50,
        width: "max-content",
        visibility: visible ? "visible" : "hidden",
      }}
      onMouseDown={(e) => e.preventDefault()}
    >
      {mode === "link-edit" ? (
        <LinkEditBar editor={editor} onClose={() => { setMode("toolbar"); editor.commands.focus(); }} />
      ) : (
        <TooltipProvider delay={300}>
          <div className="bubble-menu">
            <MarkButton editor={editor} mark="bold" icon={Bold} label={t(($) => $.bubble_menu.bold)} shortcut={createShortcutChord("B", { primary: true })} isActive={fmt.bold} />
            <MarkButton editor={editor} mark="italic" icon={Italic} label={t(($) => $.bubble_menu.italic)} shortcut={createShortcutChord("I", { primary: true })} isActive={fmt.italic} />
            <MarkButton editor={editor} mark="strike" icon={Strikethrough} label={t(($) => $.bubble_menu.strikethrough)} shortcut={createShortcutChord("S", { primary: true, shift: true })} isActive={fmt.strike} />
            <MarkButton editor={editor} mark="code" icon={Code} label={t(($) => $.bubble_menu.code)} shortcut={createShortcutChord("E", { primary: true })} isActive={fmt.code} />
            <MarkButton editor={editor} mark="highlight" icon={Highlighter} label={t(($) => $.bubble_menu.highlight)} shortcut={createShortcutChord("H", { primary: true, shift: true })} isActive={fmt.highlight} />
            <Separator orientation="vertical" className="mx-0.5 h-5" />
            <Tooltip>
              <TooltipTrigger render={
                <Toggle
                  size="sm"
                  aria-label={t(($) => $.bubble_menu.link)}
                  pressed={fmt.link}
                  onPressedChange={() => setMode("link-edit")}
                  onMouseDown={(e) => e.preventDefault()}
                />
              }>
                <Link2 className="size-3.5" />
              </TooltipTrigger>
              <TooltipContent side="top" sideOffset={8}>{t(($) => $.bubble_menu.link)}</TooltipContent>
            </Tooltip>
            <Separator orientation="vertical" className="mx-0.5 h-5" />
            <HeadingDropdown editor={editor} onOpenChange={handleMenuOpenChange} activeLevel={fmt.headingLevel} />
            <ListDropdown editor={editor} onOpenChange={handleMenuOpenChange} isBullet={fmt.bulletList} isOrdered={fmt.orderedList} isTask={fmt.taskList} />
            {/* 复选框任务列表的专用一键开关——把当前行变成 `- [ ]` 任务项
                或退回段落。同样的开关也存在于列表下拉里，但独立按钮把常见
                的「把这个做成清单」动作从两步缩短到一步。 */}
            <Tooltip>
              <TooltipTrigger render={
                <Toggle
                  size="sm"
                  aria-label={t(($) => $.bubble_menu.task_list)}
                  pressed={fmt.taskList}
                  onPressedChange={() => editor.chain().focus().toggleTaskList().run()}
                  onMouseDown={(e) => e.preventDefault()}
                />
              }>
                <ListTodo className="size-3.5" />
              </TooltipTrigger>
              <TooltipContent side="top" sideOffset={8}>{t(($) => $.bubble_menu.task_list)}</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger render={
                <Toggle
                  size="sm"
                  aria-label={t(($) => $.bubble_menu.quote)}
                  pressed={fmt.blockquote}
                  onPressedChange={() => editor.chain().focus().toggleBlockquote().run()}
                  onMouseDown={(e) => e.preventDefault()}
                />
              }>
                <Quote className="size-3.5" />
              </TooltipTrigger>
              <TooltipContent side="top" sideOffset={8}>{t(($) => $.bubble_menu.quote)}</TooltipContent>
            </Tooltip>
            {currentIssueId && (
              <>
                <Separator orientation="vertical" className="mx-0.5 h-5" />
                <CreateSubIssueButton editor={editor} parentIssueId={currentIssueId} />
              </>
            )}
          </div>
        </TooltipProvider>
      )}
    </div>
  );
}

/** 导出 EditorBubbleMenu 气泡菜单。 */
export { EditorBubbleMenu };

"use client";

import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { CheckCircle2, MessageSquare, MessagesSquare, Search } from "lucide-react";
import type { TimelineEntry } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { createShortcutChord, useShortcut } from "@multica/core/shortcuts";
import { preprocessMentionShortcodes } from "@multica/core/markdown";
import { isImeComposing } from "@multica/core/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "../../common/actor-avatar";
import { pickerNavigationDirection } from "../../common/picker-keys";
import { ShortcutKeycaps } from "../../common/shortcut-keycaps";
import { useLocale, useT } from "../../i18n";
import { commentPreview } from "./thread-minimap";

// ---------------------------------------------------------------------------
// 本文件：ThreadNavPanel —— 评论线程间跳转的头部入口面板
// ---------------------------------------------------------------------------
//
// 右缘轨道（ThreadMinimap）擅长指示位置、不擅长"找东西"：刻度没有文字，
// 想定位某个具体线程就得一颗颗悬停试，且线程数超过轨道高度后刻度会压到
// 5px 下限、彻底数不过来。列表修正了成本模型——一眼能读 8–10 个标题，
// 而不是一次一个——搜索/过滤让 24 个线程和 240 个线程花同样的功夫
// （MUL-5755）。
//
// 面板刻意不标记"哪些线程当前在屏幕上"。"在屏幕上"是一个集合，不是单点
// ——高视口同时容纳多条线程，标记会同时落在两三行上，读起来像坏掉的多选
// 而不是一个位置。绝对位置是轨道的差事，那里一列刻度能诚实展示区段；
// 面板的差事是"找"。悬停某行仍会点亮轨道上对应刻度，把两者连起来，
// 而不用复读轨道的答案。
//
// 打开模型 —— 悬停预览、按下钉住：
//
//   hover 200ms  -> 打开、未钉住。指针移出即关。不移动焦点，
//                   半打字的评论绝不丢光标。
//   按下          -> 钉住。搜索框拿焦点、方向键驱动列表，
//                   只有 Escape / 外部按下 / 再次按下才关。
//   面板内聚焦或滚动 -> 钉住。
//
// 纯 hover 对"必须能滚动、能打字、能方向键操作"的表面来说是错误触发器：
// 三者都要求指针能离开按钮。按*意图*（按下、聚焦、滚动）钉住而非仅仅
// 指针进入，让一次穿堂而过的滑行不至于把面板卡在打开状态。

/** 低于这个数头部按钮就是噪音——没有讨论的问题不需要它。 */
const MIN_THREADS = 1;

/** 预览打开的悬停意图延迟。 */
const HOVER_OPEN_DELAY_MS = 200;
/** 离开的宽限期——够从按钮走到面板里。 */
const HOVER_CLOSE_DELAY_MS = 200;

const DAY_MS = 24 * 60 * 60 * 1000;

// 底部提示快捷键。按真实和弦构建，让 ShortcutKeycaps 用平台自带的
// 字形与键帽外观渲染每一个。
const KEY_UP = createShortcutChord("Up");
const KEY_DOWN = createShortcutChord("Down");
const KEY_ENTER = createShortcutChord("Enter");
const KEY_ESCAPE = createShortcutChord("Escape");

export type ThreadNavFilter = "all" | "unresolved" | "resolved" | "mine";

export interface ThreadNavThread {
  /** 根评论 id —— 也是渲染行的 `comment-${id}` DOM 锚点。 */
  id: string;
  /** 线程的根评论条目（预览文字、作者、时间戳）。 */
  entry: TimelineEntry;
  /** 由调用方用 `deriveThreadResolution` 派生——覆盖回复里的解决。 */
  resolved: boolean;
  /** 根评论下的回复数，不含根自身。 */
  replyCount: number;
  /** 当前用户在本线程里写过、回过或被 @ 过。 */
  involvesMe: boolean;
}

export type ThreadDayGroup = "today" | "yesterday" | "earlier";

/**
 * 按根评论所在日历日给线程分桶，时区用读者本地时区。只有三个桶：
 * 过了 "yesterday" 再按天分页，在长期问题上会产生比行还多的页头——
 * 那是噪音不是结构；读者进入 "earlier" 后，每行自带的时间戳已经给出
 * 确切日期。
 */
export function threadDayGroup(createdAt: string, nowMs: number): ThreadDayGroup {
  const ts = Date.parse(createdAt);
  if (Number.isNaN(ts)) return "earlier";
  const startOfToday = new Date(nowMs).setHours(0, 0, 0, 0);
  if (ts >= startOfToday) return "today";
  if (ts >= startOfToday - DAY_MS) return "yesterday";
  return "earlier";
}

/** 一条线程 + 行与过滤器需要的一切，一次性算好。 */
interface PreparedThread {
  thread: ThreadNavThread;
  title: string;
  excerpt: string;
  authorName: string;
  group: ThreadDayGroup;
  /** 小写的 `title + excerpt + author`——查询要跑的稻草堆。 */
  haystack: string;
}

/**
 * 线程是否匹配给定过滤器。
 */
export function matchesFilter(thread: ThreadNavThread, filter: ThreadNavFilter): boolean {
  switch (filter) {
    case "unresolved":
      return !thread.resolved;
    case "resolved":
      return thread.resolved;
    case "mine":
      return thread.involvesMe;
    case "all":
      return true;
    default:
      // 服务端驱动的值到不了这个联合类型，但穷尽式 default 能防止未来
      // 新加的过滤器悄悄把每条线程都藏起来。
      return true;
  }
}

/**
 * `content` 是否 @ 了 `userId`。
 *
 * 现行 @ 会序列化成 `mention://member/<uuid>` 的 markdown 链接，但旧的
 * `[@ id="..." label="..."]` 短代码形式仍躺在数据库里——`preprocessMarkdown`
 * 在*读取时*迁移它，而不是存储前；时间线交给我们的又是原始内容。只匹配
 * 链接形式会静默丢掉每条"对读者的唯一提及是用老格式写的"线程。
 * `preprocessMentionShortcodes` 在没有 `[@ ` 时原样返回输入，
 * 所以正常路径只多花一次子串扫描。
 */
export function mentionsUser(content: string | undefined, userId: string): boolean {
  if (!content || !userId) return false;
  return preprocessMentionShortcodes(content).includes(`mention://member/${userId}`);
}

/**
 * 把 `text` 按 `query` 的每一次大小写不敏感出现切分，并给命中处染色。
 * 没有它，过滤后的列表会让读者重新去找刚打的词——靠摘要命中的行和靠标题
 * 命中的行长一个样。复用页内查找条的同一款 `--find-match` 染色，
 * 让"这就是你的搜索词"在任何地方读起来都一致。
 */
export function highlightMatches(text: string, query: string): ReactNode {
  const needle = query.trim();
  if (needle === "") return text;
  const lowerText = text.toLowerCase();
  const lowerNeedle = needle.toLowerCase();
  const out: ReactNode[] = [];
  let from = 0;
  let at = lowerText.indexOf(lowerNeedle);
  while (at !== -1) {
    if (at > from) out.push(text.slice(from, at));
    out.push(
      <mark
        key={at}
        className="rounded-[3px] bg-[var(--find-match)] px-px text-[var(--find-match-foreground)]"
      >
        {text.slice(at, at + needle.length)}
      </mark>,
    );
    from = at + needle.length;
    at = lowerText.indexOf(lowerNeedle, from);
  }
  if (from < text.length) out.push(text.slice(from));
  return out;
}

/**
 * 单行线程。可跳转按钮 + 高亮标题/摘要 + 作者/回复数/已解决标记。
 */
function ThreadRow({
  prepared,
  isActive,
  optionId,
  query,
  onJump,
  onHover,
}: {
  prepared: PreparedThread;
  /** 键盘光标在此行上。 */
  isActive: boolean;
  /** 稳定的 DOM id，好让搜索框能指向这一行。 */
  optionId: string;
  /** 当前搜索词，在标题与摘要内染色。 */
  query: string;
  onJump: () => void;
  onHover: (threadId: string | null) => void;
}) {
  const { t } = useT("issues");
  const locale = useLocale();
  const { thread, title, excerpt, authorName } = prepared;
  return (
    <button
      type="button"
      // 不是 Tab 停靠点，role=option：搜索框保持 DOM 焦点，通过
      // aria-activedescendant 点名本行。让行自身可聚焦会产生两个光标——
      // Tab 到第 3 行、ArrowDown 高亮第 2 行、Enter 点第 3 行——因为 DOM
      // 焦点与高亮是两块互不同步的状态。
      tabIndex={-1}
      role="option"
      id={optionId}
      aria-selected={isActive}
      data-thread-id={thread.id}
      data-active={isActive || undefined}
      onClick={onJump}
      onPointerEnter={() => onHover(thread.id)}
      onPointerLeave={() => onHover(null)}
      className={cn(
        "relative flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left outline-none transition-colors",
        "hover:bg-surface-hover data-active:bg-surface-hover",
      )}
    >
      <ActorAvatar
        actorType={thread.entry.actor_type}
        actorId={thread.entry.actor_id}
        name={thread.entry.actor_name}
        avatarUrl={thread.entry.actor_avatar_url}
        size="sm"
        profileLink={false}
        className="mt-0.5 shrink-0"
      />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="flex items-baseline gap-1.5">
          <span
            className={cn(
              "min-w-0 flex-1 truncate text-body",
              thread.resolved ? "text-muted-foreground" : "font-medium text-foreground",
            )}
          >
            {highlightMatches(title, query)}
          </span>
          <span className="shrink-0 text-micro tabular-nums text-faint-foreground">
            {formatStamp(prepared.thread.entry.created_at, prepared.group, locale)}
          </span>
        </span>
        <span className="mt-1 flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
          <span className="shrink-0">{authorName}</span>
          {thread.replyCount > 0 && (
            <span className="flex shrink-0 items-center gap-0.5 tabular-nums text-faint-foreground">
              <MessageSquare className="h-3 w-3" />
              {thread.replyCount}
            </span>
          )}
          {thread.resolved && (
            <span className="flex shrink-0 items-center gap-0.5 text-success">
              <CheckCircle2 className="h-3 w-3" />
              {t(($) => $.comment.resolve.thread_resolved_badge)}
            </span>
          )}
          {excerpt && (
            <span className="min-w-0 truncate text-faint-foreground">
              {highlightMatches(excerpt, query)}
            </span>
          )}
        </span>
      </span>
    </button>
  );
}

/**
 * "今天/昨天"页头下用墙上时钟，"earlier"下用日历日期——前两种情况下
 * 页头已点名那天，每行再重复是已知处的冗余；到了未知处（更早）反而缺失。
 *
 * 由行自己的分组驱动，而不是再测一次"是否 48 小时以内"。这两条边界在一天
 * 的大多数时间里都不一致：前天的 23:41 距今不足 48 小时，但被分到
 * "earlier"下，于是它渲染成光秃秃的 `11:41 PM`、没有任何信息说明是哪天。
 * 一条边界、判定一次，就不会跟自己打架。
 */
export function formatStamp(createdAt: string, group: ThreadDayGroup, locale: string): string {
  const ts = Date.parse(createdAt);
  if (Number.isNaN(ts)) return "";
  const d = new Date(ts);
  return group === "earlier"
    ? d.toLocaleDateString(locale, { month: "short", day: "numeric" })
    : d.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });
}

interface ThreadNavPanelProps {
  threads: ThreadNavThread[];
  onJump: (threadId: string) => void;
  /** 点亮右缘轨道上对应的刻度。 */
  onHoverThread: (threadId: string | null) => void;
  /** 受控的打开状态——好让全局快捷键能钉住面板。 */
  open: boolean;
  onOpenChange: (open: boolean, pinned: boolean) => void;
  /** `open` 是经由按下/快捷键到达的（而非悬停）时为 true。 */
  pinned: boolean;
}

/**
 * 评论线程导航面板（职责见文件头）。
 */
export function ThreadNavPanel({
  threads,
  onJump,
  onHoverThread,
  open,
  onOpenChange,
  pinned,
}: ThreadNavPanelProps) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  // 订阅而非快照：`getShortcut` 只读一次 store、不订阅，所以在设置里重绑后
  // 这个 tooltip 会一直显示旧键，而真正的 keydown 处理器早已用上新键。
  const openShortcutChord = useShortcut("openThreadNav");
  const listId = useId();
  const optionId = useCallback((threadId: string) => `${listId}-${threadId}`, [listId]);

  const [tooltipOpen, setTooltipOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<ThreadNavFilter>("all");
  const [activeIndex, setActiveIndex] = useState(0);
  const searchRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  // 预览按内容缓存，让一条无关的时间线更新（一个表情、另一线程的一条回复）
  // 不必重新把每条根评论扁平化一遍。
  const previewCacheRef = useRef(
    new Map<string, { content: string | undefined; preview: { title: string; body: string } }>(),
  );
  const prepared = useMemo<PreparedThread[]>(() => {
    const nowMs = Date.now();
    const nextCache = new Map<
      string,
      { content: string | undefined; preview: { title: string; body: string } }
    >();
    const rows = threads.map((thread) => {
      const cached = previewCacheRef.current.get(thread.id);
      const preview =
        cached && cached.content === thread.entry.content
          ? cached.preview
          : commentPreview(thread.entry.content ?? "");
      nextCache.set(thread.id, { content: thread.entry.content, preview });
      const authorName =
        thread.entry.actor_name ||
        getActorName(thread.entry.actor_type, thread.entry.actor_id);
      const title = preview.title || authorName;
      return {
        thread,
        title,
        excerpt: preview.body,
        authorName,
        group: threadDayGroup(thread.entry.created_at, nowMs),
        haystack: `${title}\n${preview.body}\n${authorName}`.toLowerCase(),
      };
    });
    previewCacheRef.current = nextCache;
    return rows;
  }, [threads, getActorName]);

  const rows = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return prepared.filter(
      (row) =>
        matchesFilter(row.thread, filter) &&
        (needle === "" || row.haystack.includes(needle)),
    );
  }, [prepared, filter, query]);

  const counts = useMemo(
    () => ({
      all: threads.length,
      unresolved: threads.filter((th) => !th.resolved).length,
      resolved: threads.filter((th) => th.resolved).length,
      mine: threads.filter((th) => th.involvesMe).length,
    }),
    [threads],
  );

  // 每次打开都从干净状态起步：不带入上次的查询词、过滤器或光标。
  const openRef = useRef(open);
  useEffect(() => {
    if (open && !openRef.current) {
      setQuery("");
      setFilter("all");
      setActiveIndex(0);
    }
    openRef.current = open;
  }, [open]);

  // 过滤会让列表缩到光标之下；改成钳制而不是让它指到末端之后——
  // 那样 ↵ 会毫无反应。
  useEffect(() => {
    setActiveIndex((prev) => (prev >= rows.length ? Math.max(0, rows.length - 1) : prev));
  }, [rows.length]);

  // 让键盘光标保持在可视范围内——长按方向键可能把它推到可视切片
  // 很远之外。
  useEffect(() => {
    if (!open) return;
    const row = rows[activeIndex];
    if (!row) return;
    listRef.current
      ?.querySelector<HTMLElement>(`[data-thread-id="${CSS.escape(row.thread.id)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, open, rows]);

  const pin = useCallback(() => onOpenChange(true, true), [onOpenChange]);

  // 焦点跟随 `pinned` 而不是按下处理器，好让全局快捷键——它直接设置
  // pinned 状态——也能把光标落进搜索框。延后一帧是因为 Base UI 在打开时
  // 会跑自己的焦点流程，否则会把光标抢回去。
  useEffect(() => {
    if (!open || !pinned) return;
    const raf = requestAnimationFrame(() => searchRef.current?.focus());
    return () => cancelAnimationFrame(raf);
  }, [open, pinned]);

  const close = useCallback(() => {
    onHoverThread(null);
    onOpenChange(false, false);
  }, [onHoverThread, onOpenChange]);

  const jump = useCallback(
    (threadId: string) => {
      onHoverThread(null);
      onJump(threadId);
      onOpenChange(false, false);
    },
    [onHoverThread, onJump, onOpenChange],
  );

  const handleOpenChange = useCallback(
    (next: boolean, details: { reason?: string }) => {
      const reason = details?.reason;
      if (next) {
        if (reason === "trigger-press") pin();
        else onOpenChange(true, false);
        return;
      }
      // 按下一个"悬停预览"的触发器 = "留着它，我要进去干活"——
      // 不是"关闭"。只有按下一个已经钉住的面板才关闭它。
      if (reason === "trigger-press" && !pinned) {
        pin();
        return;
      }
      // 钉住的面板活得比指针久：用户必须够得到滚动条和行，
      // 而不必担心面板在身后蒸发。
      if (pinned && reason === "trigger-hover") return;
      close();
    },
    [close, pin, pinned, onOpenChange],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      // IME 组词时会以 Enter（候选确认）和别名普通字母的形式到达事件。
      // 对它们动手会在用户（尤其 CJK）选词的当口跳走并关掉面板——
      // 搜索框正是本面板里人们会打整句正文的唯一位置。
      if (isImeComposing(e)) return;
      // 列表键盘归搜索框独占，方向键也算。让方向键在弹层任意位置都能发、
      // 而 Enter 只在搜索框里发，是两者最差组合：焦点停在过滤器药丸上时
      // ArrowDown 移高亮、Enter 却激活了药丸——两个光标各说各话。
      if (e.target !== searchRef.current) return;
      // 方向键外加 Ctrl+N/J 与 Ctrl+P/K 别名，沿用编辑器 pickers 同一策略
      // ——它又镜像 cmdk 的 `vimBindings` 免费给命令条的东西。
      // 一个有高亮的列表不该跟另一个导航得不一样。
      const direction = pickerNavigationDirection(e.nativeEvent);
      if (direction) {
        // preventDefault 对字母别名尤其要紧：焦点在搜索框里，
        // Ctrl+K/N/P 本是 readline 编辑命令——会一边移光标一边弄乱查询词。
        e.preventDefault();
        if (rows.length === 0) return;
        const step = direction === "next" ? 1 : -1;
        setActiveIndex((prev) => (prev + step + rows.length) % rows.length);
        return;
      }
      // Enter 受上面同样的 target 检查约束——正是它保住过滤器药丸可按：
      // 对按钮 keydown 调 `preventDefault` 会取消浏览器即将合成的 click，
      // 所以若对整个弹层处理 Enter，"Resolved"会跳走而不是过滤。
      if (e.key === "Enter") {
        const row = rows[activeIndex];
        if (!row) return;
        e.preventDefault();
        jump(row.thread.id);
      }
      // Escape 是 Base UI 的活——它已经走 onOpenChange 并把焦点还给触发器。
      //
      // Tab 在这里刻意*不*作为接受键，与编辑器 pickers 不同：这是 popover，
      // Tab 必须保持焦点导航，这样过滤器药丸才能被键盘够到。
    },
    [activeIndex, jump, rows],
  );

  if (threads.length < MIN_THREADS) return null;

  const buttonLabel = t(($) => $.detail.thread_nav.button_label, { count: threads.length });
  const openShortcut = openShortcutChord;

  const filterPills: { id: ThreadNavFilter; label: string; count: number }[] = [
    { id: "all", label: t(($) => $.detail.thread_nav.filter_all), count: counts.all },
    { id: "unresolved", label: t(($) => $.detail.thread_nav.filter_unresolved), count: counts.unresolved },
    { id: "resolved", label: t(($) => $.detail.thread_nav.filter_resolved), count: counts.resolved },
    { id: "mine", label: t(($) => $.detail.thread_nav.filter_mine), count: counts.mine },
  ];

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      {/* Base UI 把悬停配置放在 Trigger 而非 Root 上（popover 可以有多个
          触发器）。触发器保持真实按钮，按下与键盘对触屏和无障碍仍然有效。 */}
      {/* Tooltip 与其他头部控件一样，也是唯一能发现打开快捷键的地方——
          同一套标签 + 键帽，聊天启动器用的形状。全程受控（从不 `undefined`，
          否则中途会翻成非受控）并在面板打开期间强制关闭，
          让提示永不叠在它上面。 */}
      <Tooltip open={tooltipOpen && !open} onOpenChange={setTooltipOpen}>
        <TooltipTrigger
          render={
            <PopoverTrigger
              openOnHover
              delay={HOVER_OPEN_DELAY_MS}
              closeDelay={HOVER_CLOSE_DELAY_MS}
              render={
                <Button
                  variant={open ? "secondary" : "ghost"}
                  size="sm"
                  aria-label={buttonLabel}
                  className={cn("h-7 gap-1.5 px-2", !open && "text-muted-foreground")}
                />
              }
            >
              {/* 显式 `size-4`，让图标与相邻头部动作按钮一致，而不用
                  `sm` 按钮更小的默认图标。 */}
              <MessagesSquare className="size-4" />
              <span className="text-caption font-medium tabular-nums">{threads.length}</span>
            </PopoverTrigger>
          }
        />
        <TooltipContent side="bottom">
          {buttonLabel}
          {openShortcut ? (
            <ShortcutKeycaps shortcut={openShortcut} decorative className="ml-1.5" />
          ) : null}
        </TooltipContent>
      </Tooltip>
      <PopoverContent
        align="end"
        // 让进右缘轨道 32px 细条一步，两个导航器才能并排可读：轨道继续展示
        // 绝对位置，面板负责找。对齐轴上，正偏移把弹层推向与对齐端相反的那
        // 条边，所以配 `align="end"` 时它是左移、远离轨道。
        alignOffset={24}
        // 悬停预览绝不能抢走半条评论里的光标；`pin()` 改为显式聚焦搜索框。
        initialFocus={false}
        onKeyDown={handleKeyDown}
        // 面板内任何刻意的交互都把预览升级为钉住。指针进入本身刻意不触发：
        // 路过面板去别处的滑行不该把它留在打开状态。
        onFocusCapture={pinned ? undefined : pin}
        // 每个横带都让内容内缩到同一个 14px：搜索图标与页脚坐在 px-3.5，
        // 药丸行与列表靠"容器内边距 + 条目内边距"（6 + 8）够到它。
        // 10 / 18 / 12 时三条横带近到像手滑、远到看得出缝——这正是面板
        // 显得漂浮而不是密实的原因。400px 而非 380，让更宽的内缩
        // 不牺牲摘要宽度。
        className="flex w-[400px] flex-col gap-0 p-0"
      >
        {/* 搜索与过滤器是一个头部块，所以它们之间那条分隔线没了：
            三道分隔线在 380px popover 里读起来像堆叠的横带而不是一个表面，
            而过滤器行又是其中最细的横带。 */}
        <div className="flex h-11 shrink-0 items-center gap-2.5 px-3.5">
          <Search className="size-4 shrink-0 text-faint-foreground" />
          <input
            ref={searchRef}
            role="combobox"
            aria-expanded
            aria-controls={listId}
            aria-activedescendant={rows[activeIndex] ? optionId(rows[activeIndex]!.thread.id) : undefined}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActiveIndex(0);
            }}
            placeholder={t(($) => $.detail.thread_nav.search_placeholder)}
            aria-label={t(($) => $.detail.thread_nav.search_placeholder)}
            className="min-w-0 flex-1 bg-transparent text-body text-foreground outline-none placeholder:text-faint-foreground"
          />
          {query.trim() !== "" && (
            <span className="shrink-0 text-caption tabular-nums text-faint-foreground">
              {t(($) => $.detail.thread_nav.match_count, { count: rows.length })}
            </span>
          )}
        </div>

        {/* 这条分隔线留着：它是一个滚动区域的顶边，没有它列表会滑到
            药丸下面、没有任何东西标出边界。 */}
        <div className="flex shrink-0 items-center gap-1 border-b border-border px-1.5 py-2">
          {filterPills.map((pill) => (
            <button
              key={pill.id}
              type="button"
              onClick={() => {
                setFilter(pill.id);
                setActiveIndex(0);
              }}
              data-active={filter === pill.id || undefined}
              className={cn(
                "flex h-7 items-center gap-1 rounded-full px-2 text-caption text-muted-foreground transition-colors",
                "hover:bg-surface-hover",
                "data-active:bg-surface-selected data-active:font-medium data-active:text-foreground data-active:hover:bg-surface-selected",
              )}
            >
              {pill.label}
              <span className="tabular-nums text-faint-foreground">{pill.count}</span>
            </button>
          ))}
        </div>

        <div
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label={t(($) => $.detail.thread_nav.search_placeholder)}
          // 滚动是一种意图信号：读者正在操作列表，
          // 所以之后指针离开时面板也该活着。
          onScroll={pinned ? undefined : pin}
          className="max-h-[26rem] min-h-0 flex-1 overflow-y-auto p-1.5"
        >
          {rows.length === 0 ? (
            <p className="px-2 py-8 text-center text-caption text-muted-foreground">
              {t(($) => $.detail.thread_nav.empty)}
            </p>
          ) : (
            rows.map((row, i) => {
              const prev = rows[i - 1];
              const showHeader = !prev || prev.group !== row.group;
              return (
                <div key={row.thread.id}>
                  {/* 刻意不对称——日期标签属于它下面的那组，所以它离自己组
                      的首行比离上一组末行更近。 */}
                  {showHeader && (
                    <p className="px-2 pb-1 pt-3 text-micro font-medium text-faint-foreground">
                      {t(($) => $.detail.thread_nav.group[row.group])}
                    </p>
                  )}
                  <ThreadRow
                    prepared={row}
                    isActive={i === activeIndex}
                    optionId={optionId(row.thread.id)}
                    query={query}
                    onJump={() => jump(row.thread.id)}
                    onHover={onHoverThread}
                  />
                </div>
              );
            })
          )}
        </div>

        {/* 键帽来自共享的 ShortcutKeycaps，读起来与侧栏、命令面板、确认弹窗
            画的是同一批键——真实的键帽外观与平台自己的字形，而不是打进
            翻译字符串里的箭头。 */}
        <div className="flex h-9 shrink-0 items-center gap-4 border-t border-border px-3.5 text-micro text-muted-foreground">
          <span className="flex items-center gap-1.5">
            <span className="flex items-center gap-0.5">
              <ShortcutKeycaps shortcut={KEY_UP} />
              <ShortcutKeycaps shortcut={KEY_DOWN} />
            </span>
            {t(($) => $.detail.thread_nav.hint_select)}
          </span>
          <span className="flex items-center gap-1.5">
            <ShortcutKeycaps shortcut={KEY_ENTER} />
            {t(($) => $.detail.thread_nav.hint_jump)}
          </span>
          <span className="flex items-center gap-1.5">
            <ShortcutKeycaps shortcut={KEY_ESCAPE} />
            {t(($) => $.detail.thread_nav.hint_close)}
          </span>
        </div>
      </PopoverContent>
    </Popover>
  );
}

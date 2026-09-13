"use client";

/**
 * 斜杠命令（`/`）建议弹层：两种菜单共用一套列表与键盘交互——
 *
 * 1. 技能选择器（聊天场景）：列出当前选中的 agent 启用的技能，按相关性
 *    排序后插入一个 slashCommand 富节点；
 * 2. 内建命令菜单（issue 评论）：固定的一组命令 + 调用方配置的快捷动作，
 *    选中后把命令文本（如 `/note `）以纯文本插入。
 *
 * 弹层定位/生命周期复用 suggestion-popup.tsx。
 */

import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import type { QueryClient } from "@tanstack/react-query";
import type { SuggestionOptions } from "@tiptap/suggestion";
import { PluginKey } from "@tiptap/pm/state";
import { useAuthStore } from "@multica/core/auth";
import { useChatStore } from "@multica/core/chat";
import { getCurrentWsId } from "@multica/core/platform";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import { isImeComposing } from "@multica/core/utils";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type { Agent, MemberWithUser } from "@multica/core/types";
import { useT } from "../../i18n";
import {
  createSuggestionPopupRender,
} from "./suggestion-popup";
import {
  isPickerAcceptKey,
  pickerNavigationDirection,
} from "../../common/picker-keys";
import { isTriggerArmedAt } from "./suggestion-trigger-arming";

const MAX_ITEMS = 20;

/** 已知的内建命令 id——编辑器 `slash_command.commands` 下的 key。 */
export type BuiltinCommandKey = "note";

/** 一条可被 `/` 选中的命令/技能条目。 */
export interface SlashCommandItem {
  id: string;
  label: string;
  /** 原始描述（技能选择器用）。内建命令改走 descriptionKey。 */
  description?: string;
  /**
   * 内建命令专用：editor `slash_command.commands` 下的 i18n key。设置了它
   * 时，菜单渲染翻译文案而不是 `description`，保证可见字符串保持本地化
   * （敲出来的 `/label` 则不是）。
   */
  descriptionKey?: BuiltinCommandKey;
}

interface SlashCommandListProps {
  items: SlashCommandItem[];
  query: string;
  command: (item: SlashCommandItem) => void;
  /**
   * 为 true 时，没有匹配条目就什么都不渲染，而不是画一个空状态框。
   * issue 评论里的内建命令菜单用它：`/` 在散文中很常见（路径、日期），
   * 每个斜杠都弹窗会变成噪音。聊天技能选择器保持 false，以便仍能解释
   * 「没有配置技能」。
   */
  hideOnEmpty?: boolean;
}

/** 列表组件通过 ref 暴露的键盘处理句柄。 */
export interface SlashCommandListRef {
  onKeyDown: (props: { event: KeyboardEvent }) => boolean;
}

/**
 * SlashCommandList——`/` 弹层里的条目列表组件（技能与内建命令共用）。
 */
export const SlashCommandList = forwardRef<
  SlashCommandListRef,
  SlashCommandListProps
>(function SlashCommandList({ items, query, command, hideOnEmpty = false }, ref) {
  const { t } = useT("editor");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);

  useEffect(() => {
    setSelectedIndex(0);
  }, [items]);

  useEffect(() => {
    itemRefs.current[selectedIndex]?.scrollIntoView({ block: "nearest" });
  }, [selectedIndex]);

  const selectItem = useCallback(
    (index: number) => {
      const item = items[index];
      if (!item) return;
      command(item);
    },
    [items, command],
  );

  useImperativeHandle(ref, () => ({
    onKeyDown: ({ event }) => {
      if (isImeComposing(event)) return false;
      // 方向键加 Ctrl+N/J/P/K 别名，即命令栏接受的组合——见
      // pickerNavigationDirection。
      const direction = pickerNavigationDirection(event);
      if (direction !== null) {
        if (items.length === 0) return false;
        const delta = direction === "next" ? 1 : items.length - 1;
        setSelectedIndex((i) => (i + delta) % items.length);
        return true;
      }
      // Enter 是规范确认键；纯 Tab 是附加别名（见 isPickerAcceptKey）。
      // Shift/修饰键 + Tab 落进焦点导航。
      if (isPickerAcceptKey(event)) {
        if (items.length === 0) return false;
        selectItem(selectedIndex);
        return true;
      }
      return false;
    },
  }));

  if (items.length === 0) {
    if (hideOnEmpty) return null;
    return (
      <div className="rounded-md border bg-popover p-2 text-caption text-muted-foreground shadow-md">
        {t(($) =>
          query.trim()
            ? $.slash_command.no_results
            : $.slash_command.no_skills_configured,
        )}
      </div>
    );
  }

  // 内建命令带 i18n key，让可见描述保持本地化；技能携带来自其配置的原始
  // 描述字符串。
  const describe = (item: SlashCommandItem): string | undefined =>
    item.descriptionKey === "note"
      ? t(($) => $.slash_command.commands.note)
      : item.description;

  return (
    // 高度预算钳到 min(设计最大值, suggestion-popup.tsx 的 size middleware
    // 发布的视口感知 `--suggestion-available-height`)，独立渲染时回退到设计
    // 最大值。唯一高度权威——与 MentionList 一致。
    <div className="rounded-md border bg-popover py-1 shadow-md w-72 max-h-[min(300px,var(--suggestion-available-height,300px))] overflow-y-auto">
      {items.map((item, index) => {
        const description = describe(item);
        return (
          <button
            key={item.id}
            ref={(el) => {
              itemRefs.current[index] = el;
            }}
            className={`flex w-full flex-col gap-0.5 px-3 py-1.5 text-left text-caption transition-colors ${
              selectedIndex === index ? "bg-accent" : "hover:bg-accent/50"
            }`}
            onClick={() => selectItem(index)}
          >
            <span className="font-medium">/{item.label}</span>
            {description && (
              <span className="truncate text-muted-foreground">
                {description}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
});

const NO_MATCH = 4;

/** 返回匹配层级：精确名称、前缀、子串，再到描述。 */
function skillMatchRank(
  skill: { name: string; description?: string },
  q: string,
): number {
  const name = skill.name.toLowerCase();
  if (name === q) return 0;
  if (name.startsWith(q)) return 1;
  if (name.includes(q)) return 2;
  if ((skill.description ?? "").toLowerCase().includes(q)) return 3;
  return NO_MATCH;
}

/** 按相关性排序匹配结果，同一层级内保持配置顺序。 */
function rankSkillMatches<T extends { name: string; description?: string }>(
  skills: T[],
  q: string,
): T[] {
  if (!q) return skills;
  return skills
    .map((skill) => ({ skill, rank: skillMatchRank(skill, q) }))
    .filter((entry) => entry.rank !== NO_MATCH)
    .sort((a, b) => a.rank - b.rank)
    .map((entry) => entry.skill);
}

function buildItems(qc: QueryClient, query: string): SlashCommandItem[] {
  const wsId = getCurrentWsId();
  if (!wsId) return [];

  const agents: Agent[] = qc.getQueryData(workspaceKeys.agents(wsId)) ?? [];
  const members: MemberWithUser[] =
    qc.getQueryData(workspaceKeys.members(wsId)) ?? [];
  // Tiptap 会在 React 渲染之外调用 suggestion items，所以这里刻意直接读
  // store。
  const { selectedAgentId } = useChatStore.getState();
  const userId = useAuthStore.getState().user?.id ?? null;
  const memberRole = members.find((m) => m.user_id === userId)?.role ?? null;

  const availableAgents = agents.filter(
    (a) =>
      !a.archived_at &&
      canAssignAgentToIssue(a, { userId, role: memberRole }).allowed,
  );
  const activeAgent =
    availableAgents.find((a) => a.id === selectedAgentId) ??
    availableAgents[0] ??
    null;

  const q = query.toLowerCase();
  return rankSkillMatches(activeAgent?.skills ?? [], q)
    .slice(0, MAX_ITEMS)
    .map((s) => ({ id: s.id, label: s.name, description: s.description ?? "" }));
}

/**
 * 创建聊天场景的 `/` 技能选择器 Suggestion 配置。
 * @param qc React Query 客户端，用于读 agent 及其技能。
 */
export function createSlashCommandSuggestion(qc: QueryClient): Omit<
  SuggestionOptions<SlashCommandItem>,
  "editor"
> {
  const pluginKey = new PluginKey("slashCommandSuggestion");

  return {
    char: "/",
    pluginKey,
    // 只对用户真正敲下的 `/` 打开，粘贴来的路径（`/usr/local/bin`）绝不会
    // 打开技能选择器（MUL-5429）。
    shouldShow: ({ editor, range }) => isTriggerArmedAt(editor, range.from),
    items: ({ query }) => buildItems(qc, query),
    command: ({ editor, range, props }) => {
      const nodeAfter = editor.view.state.selection.$to.nodeAfter;
      const overrideSpace = nodeAfter?.text?.startsWith(" ");
      if (overrideSpace) {
        range.to += 1;
      }

      editor
        .chain()
        .focus()
        .insertContentAt(range, [
          {
            type: "slashCommand",
            attrs: {
              id: props.id,
              label: props.label,
              mentionSuggestionChar: "/",
            },
          },
          { type: "text", text: " " },
        ])
        .run();

      window.getSelection()?.collapseToEnd();
    },
    render: createSuggestionPopupRender<SlashCommandItem, SlashCommandItem, SlashCommandListRef, SlashCommandListProps>({
      pluginKey,
      component: SlashCommandList,
      getProps: (props) => ({
        items: props.items,
        query: props.query,
        command: props.command,
      }),
      onKeyDown: (ref, props) => ref?.onKeyDown(props) ?? false,
    }),
  };
}

// ---------------------------------------------------------------------------
// 内建命令菜单（issue 评论）
// ---------------------------------------------------------------------------

/**
 * issue 评论 composer 里提供的内建斜杠命令。与聊天的 `/` 选择器（列出当前
 * agent 的技能）不同，这是一组固定、人工挑选的命令。目前只有 `/note`：
 * 把评论标记为仅人工的 note、不会触发被指派的 agent——镜像服务端
 * `noteCommentPrefix`（server/internal/handler/comment.go）。
 */
export const BUILTIN_COMMANDS: SlashCommandItem[] = [
  { id: "note", label: "note", descriptionKey: "note" },
];

/** 把某个菜单项标记为「配置的快捷动作」而非内建命令。 */
export const QUICK_ACTION_ITEM_PREFIX = "quick-action:";

/** 该条目是否来自快捷动作。 */
export function isQuickActionItem(item: SlashCommandItem): boolean {
  return item.id.startsWith(QUICK_ACTION_ITEM_PREFIX);
}

/** 从快捷动作条目里取出原始动作 id。 */
export function quickActionIdFromItem(item: SlashCommandItem): string {
  return item.id.slice(QUICK_ACTION_ITEM_PREFIX.length);
}

// 只按命令标签做前缀匹配——描述只用于展示、不参与搜索。单条命令下这让
// 菜单行为可预期（敲 `/no` 会露出 `note`；无关的 `/deploy` 什么都不显示）。
export function buildBuiltinCommandItems(
  query: string,
  quickActions: { id: string; name: string; description?: string }[] = [],
): SlashCommandItem[] {
  const q = query.toLowerCase();
  // 快捷动作排前面：在 issue 上它们才是用户伸手去敲 `/` 的原因，而 `/note`
  // 是很少用到的逃生口。
  const actionItems: SlashCommandItem[] = quickActions.map((a) => ({
    id: `${QUICK_ACTION_ITEM_PREFIX}${a.id}`,
    label: a.name,
    description: a.description || undefined,
  }));
  return [...actionItems, ...BUILTIN_COMMANDS]
    .filter((c) => c.label.toLowerCase().startsWith(q))
    .slice(0, MAX_ITEMS);
}

export interface BuiltinCommandSuggestionOptions {
  /**
   * 与内建命令并排提供的已配置快捷动作。每次击键惰性读取，让新建的动作
   * 无需重挂编辑器即可出现。
   */
  getQuickActions?: () => { id: string; name: string; description?: string }[];
  /**
   * 把快捷动作解析成它将发布的文本。服务端渲染，所以插入的正文与点击侧栏
   * 按钮发送的内容逐字节一致。返回 ""（或抛错）时不得改动 composer，而不是
   * 插入一段半渲染的提示词。
   */
  renderQuickAction?: (quickActionId: string) => Promise<string>;
  /**
   * renderQuickAction 拒绝时调用。扩展自身无法展示 UI——ProseMirror 命令
   * 运行在 React 树之外——所以宿主把它变成 toast。没有它，失败的选取会
   * 完全无声。
   */
  onRenderError?: (error: unknown) => void;
}

/**
 * 创建 issue 评论场景的内建 `/` 命令菜单 Suggestion 配置。
 * @param options 见 BuiltinCommandSuggestionOptions。
 */
export function createBuiltinCommandSuggestion(
  options: BuiltinCommandSuggestionOptions = {},
): Omit<SuggestionOptions<SlashCommandItem>, "editor"> {
  const pluginKey = new PluginKey("builtinCommandSuggestion");

  return {
    char: "/",
    pluginKey,
    // 只对用户真正敲下的 `/` 打开，粘贴来的路径（`/usr/local/bin`）绝不会
    // 打开命令菜单（MUL-5429）。
    shouldShow: ({ editor, range }) => isTriggerArmedAt(editor, range.from),
    items: ({ query }) => buildBuiltinCommandItems(query, options.getQuickActions?.() ?? []),
    command: ({ editor, range, props }) => {
      if (isQuickActionItem(props)) {
        const render = options.renderQuickAction;
        if (!render) return;
        const id = quickActionIdFromItem(props);

        // 请求在途时刻意保留 "/query" 文本。先删除的话，一次失败或缓慢的
        // 渲染会毁掉用户输入的内容却无所得；而且等到解析完成时插入还会
        // 落在光标恰好所在的位置。
        //
        // 快照的是该区间下的**精确**文本而不只是形态。前缀检查（「是否仍
        // 以 / 开头」）在用户于请求中途把 `/review` 改成 `/fix` 时也会通过，
        // 过期的响应就会覆盖新命令。
        const originalText = editor.state.doc.textBetween(range.from, range.to);

        void render(id)
          .then((content) => {
            if (!content) return;
            const withinDoc = range.to <= editor.state.doc.content.size;
            const unchanged =
              withinDoc && editor.state.doc.textBetween(range.from, range.to) === originalText;
            if (!unchanged) {
              // 请求未决期间命令被编辑、移动或删除。现在插到任何地方要么
              // 覆盖用户更新的文本、要么把正文丢进无关位置，所以干脆放弃
              // 这次选取。
              return;
            }
            editor
              .chain()
              .focus()
              // contentType: "markdown" 至关重要。没有它 Tiptap 会把字符串当
              // 字面 TEXT 插入，于是服务端渲染的 `[@Name](mention://agent/…)`
              // 永远不会变成 mention 节点——它会以方括号被转义
              // （`\[@Name\](…)`）的形式序列化回去，在线程里渲染成原始标记。
              .insertContentAt({ from: range.from, to: range.to }, content, {
                contentType: "markdown",
              })
              .run();
            window.getSelection()?.collapseToEnd();
          })
          .catch((error: unknown) => {
            // 命令文本还在，用户可以重试或手改；宿主负责把「为何什么都没
            // 插入」提示出来。
            options.onRenderError?.(error);
          });
        return;
      }

      // 插入纯文本前缀（如 "/note "）而非富节点，让菜单选取与手敲的命令
      // 逐字节一致，后端用简单前缀匹配就能认出标记。末尾空格终结 suggestion
      // 匹配，菜单不会再次打开。
      editor
        .chain()
        .focus()
        .insertContentAt(range, [{ type: "text", text: `/${props.label} ` }])
        .run();

      window.getSelection()?.collapseToEnd();
    },
    render: createSuggestionPopupRender<SlashCommandItem, SlashCommandItem, SlashCommandListRef, SlashCommandListProps>({
      pluginKey,
      component: SlashCommandList,
      getProps: (props) => ({
        items: props.items,
        query: props.query,
        command: props.command,
        hideOnEmpty: true,
      }),
      onKeyDown: (ref, props) => ref?.onKeyDown(props) ?? false,
    }),
  };
}

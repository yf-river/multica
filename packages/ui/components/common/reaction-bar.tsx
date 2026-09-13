"use client";
// 本文件是「消息表情回应栏」：把若干条零散的 reaction 记录按 emoji 分组聚合成
// 一排可点击的计数徽章，点已有的表情是切换自己的回应，点右下角笑脸可添加新表情。

import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { QuickEmojiPicker } from "./quick-emoji-picker";

/** 一条原始的表情回应记录（来自后端） */
interface ReactionItem {
  /** 回应记录的唯一 id */
  id: string;
  /** 回应者类型（member / agent 等） */
  actor_type: string;
  /** 回应者 id */
  actor_id: string;
  /** 回应的表情字符 */
  emoji: string;
}

/** 按表情聚合后的分组结果，可直接用于渲染 */
interface GroupedReaction {
  /** 这个组对应的表情 */
  emoji: string;
  /** 回应总人数 */
  count: number;
  /** 当前用户是否已经回应过这个表情（用于高亮自己点的那个） */
  reacted: boolean;
  /** 参与回应的所有角色（类型 + id），用于悬浮提示里列出谁回应了 */
  actors: { type: string; id: string }[];
}

/**
 * 把原始的 reaction 记录列表按表情聚合成组。
 * 同一表情累计 count、收集参与人；若当前用户（member 类型且 id 匹配）已回应
 * 则标记该组 reacted=true。
 *
 * @param reactions 原始的回应记录
 * @param currentUserId 当前登录用户的 id（用于判断「我点没点过」）
 * @returns 分组后的数组（顺序按首次出现排序）
 */
function groupReactions(reactions: ReactionItem[], currentUserId?: string): GroupedReaction[] {
  const map = new Map<string, GroupedReaction>();
  for (const r of reactions) {
    let group = map.get(r.emoji);
    if (!group) {
      group = { emoji: r.emoji, count: 0, reacted: false, actors: [] };
      map.set(r.emoji, group);
    }
    group.count++;
    group.actors.push({ type: r.actor_type, id: r.actor_id });
    if (r.actor_type === "member" && r.actor_id === currentUserId) {
      group.reacted = true;
    }
  }
  return Array.from(map.values());
}

/**
 * ReactionBar 组件的 props 定义。
 */
interface ReactionBarProps {
  /** 原始的回应记录列表 */
  reactions: ReactionItem[];
  /** 当前登录用户的 id，用于高亮自己已回应的表情 */
  currentUserId?: string;
  /** 点击某个表情时触发切换（回应/取消回应），参数为表情字符 */
  onToggle: (emoji: string) => void;
  /** 根据角色类型 + id 解析显示名，用于悬浮提示里列出回应人 */
  getActorName: (type: string, id: string) => string;
  /** 额外的外层 class 名 */
  className?: string;
}

/**
 * ReactionBar：一排表情回应徽章 + 一个添加入口。
 * 每个徽章展示 emoji 与人数；自己回应过的用品牌色高亮；悬浮可看到谁回应了。
 * 点击徽章 = 切换自己的回应；点右下笑脸 = 从完整面板里挑个新表情回应。
 *
 * @param props 见 ReactionBarProps 定义
 * @returns 回应栏元素
 */
function ReactionBar({
  reactions,
  currentUserId,
  onToggle,
  getActorName,
  className,
}: ReactionBarProps) {
  const grouped = groupReactions(reactions, currentUserId);

  return (
    <div className={`flex flex-wrap items-center gap-1.5 ${className ?? ""}`}>
      {grouped.map((g) => (
        <Tooltip key={g.emoji}>
          <TooltipTrigger
            render={
              <button
                type="button"
                onClick={() => onToggle(g.emoji)}
                className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-caption transition-colors hover:bg-brand/15 ${
                  g.reacted
                    ? "border-brand/30 bg-brand/8 text-brand"
                    : "border-brand/10 bg-brand/4 text-muted-foreground"
                }`}
              >
                <span>{g.emoji}</span>
                <span>{g.count}</span>
              </button>
            }
          />
          <TooltipContent side="top">
            {g.actors.map((a) => getActorName(a.type, a.id)).join(", ")}
          </TooltipContent>
        </Tooltip>
      ))}
      <QuickEmojiPicker onSelect={onToggle} />
    </div>
  );
}

export { ReactionBar, type ReactionBarProps, type ReactionItem };

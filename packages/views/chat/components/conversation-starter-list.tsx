// 开场建议列表：展示聊天空状态下的推荐提问按钮，并提供默认（兜底）建议。
"use client";

import type { AgentConversationStarter } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * 当 agent 的所有者没有配置开场建议时，新聊天展示的通用建议。这是它们的唯一
 * 定义：设置预览也渲染同一个 hook，所以配置 agent 的作者看到的就是真实新聊天
 * 会显示的内容，而不是一份手工维护的仿制品。
 * @returns 默认的开场建议数组。
 */
export function useFallbackConversationStarters(): AgentConversationStarter[] {
  const { t } = useT("chat");
  return [
    {
      label: t(($) => $.conversation_starters.capabilities.label),
      prompt: t(($) => $.conversation_starters.capabilities.prompt),
    },
    {
      label: t(($) => $.conversation_starters.first_task.label),
      prompt: t(($) => $.conversation_starters.first_task.prompt),
    },
    {
      label: t(($) => $.conversation_starters.recommend.label),
      prompt: t(($) => $.conversation_starters.recommend.prompt),
    },
  ];
}

const ROW_CLASS =
  "w-full rounded-lg border border-border bg-card px-3 py-2.5 text-left text-body text-foreground";

/**
 * 聊天空状态下的一叠开场建议按钮。
 *
 * 省略 `onPick` 时，把同样的行渲染成无交互的展示——那就是设置预览，
 * 它必须看起来和真的一样，但又不能提供一个从设置页“发送”提示词的
 * 第二个、且无实际功能的入口。
 * @param starters - 开场建议列表。
 * @param onPick - 点击建议后的回调（可选，省略则为纯展示）。
 * @param className - 额外的样式类。
 */
export function ConversationStarterList({
  starters,
  onPick,
  className,
}: {
  starters: AgentConversationStarter[];
  onPick?: (prompt: string) => void;
  className?: string;
}) {
  if (!onPick) {
    return (
      <div className={cn("w-full space-y-2", className)} aria-hidden="true">
        {starters.map((item, index) => (
          <div key={index} className={cn(ROW_CLASS, "truncate")}>
            {item.label}
          </div>
        ))}
      </div>
    );
  }

  return (
    <div className={cn("w-full space-y-2", className)}>
      {starters.map((item, index) => (
        <button
          key={index}
          type="button"
          onClick={() => onPick(item.prompt)}
          className={cn(
            ROW_CLASS,
            "transition-colors hover:border-brand/40 hover:bg-accent",
          )}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}

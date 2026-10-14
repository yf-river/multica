"use client";

// Agent 系统提示词与对话开场白编辑，含聊天空态“定制”入口的深链高亮逻辑。

import { useEffect, useRef, useState } from "react";
import { Loader2, Save } from "lucide-react";
import { useConfigStore } from "@multica/core/config";
import { AGENT_FOCUS_CONVERSATION_STARTERS } from "@multica/core/paths";
import type { Agent, AgentConversationStarter } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import { useOptionalNavigation } from "../../../navigation";

import { ConversationStartersEditor } from "../conversation-starters-editor";

/** 从深链进入开场白编辑器时，高亮光环保持多久。 */
const FOCUS_FLASH_MS = 1600;

/**
 * 指令 / 开场白编辑标签页。
 * @param agent 当前 Agent；字段变更后需等待父层 save 回写。
 * @param onSave 保存回调，携带 instructions（可能还有 conversation_starters）。
 * @param onDirtyChange 本地是否改动的通知（父层用它拦截切换 Tab）。
 */
export function InstructionsTab({
  agent,
  onSave,
  onDirtyChange,
}: {
  agent: Agent;
  onSave: (updates: {
    instructions: string;
    conversation_starters?: AgentConversationStarter[];
  }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  // 可选读取：本标签是叶子，测试常单独挂载；它唯一的导航相关行为（下方深链聚焦）
  // 在没有适配器时就退化成“不高亮”。
  const navigation = useOptionalNavigation();
  const conversationStartersSupported = useConfigStore(
    (state) => state.agentConversationStartersSupported,
  );
  const [value, setValue] = useState(agent.instructions ?? "");
  const [conversationStarters, setConversationStarters] = useState<AgentConversationStarter[]>(
    agent.conversation_starters ?? [],
  );
  const [saving, setSaving] = useState(false);
  const [conversationStartersFocused, setConversationStartersFocused] = useState(false);
  const conversationStartersRef = useRef<HTMLDivElement | null>(null);
  const focusHandledForAgentRef = useRef<string | null>(null);
  const focusFlashTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const persistedConversationStartersKey = JSON.stringify(
    agent.conversation_starters ?? [],
  );
  const persistedRef = useRef({
    agentId: agent.id,
    instructions: agent.instructions ?? "",
    conversationStartersKey: persistedConversationStartersKey,
  });
  const localRef = useRef({
    instructions: value,
    conversationStartersKey: JSON.stringify(conversationStarters),
  });
  localRef.current = {
    instructions: value,
    conversationStartersKey: JSON.stringify(conversationStarters),
  };
  const isDirty =
    value !== (agent.instructions ?? "") ||
    (conversationStartersSupported &&
      JSON.stringify(conversationStarters) !== persistedConversationStartersKey);
  const conversationStartersValid =
    !conversationStartersSupported ||
    conversationStarters.every(
      (item) => item.label.trim() && item.prompt.trim(),
    );

  // 重新拉取会把嵌套数组整体替换——哪怕内容没变。跟“上一次持久化”的语义快照比较，
  // 让这些重拉不会抹掉本地编辑。真实的服务端变更只在本地表单干净时才采纳；
  // 切换 Agent 则始终加载所选项。
  useEffect(() => {
    const previous = persistedRef.current;
    const switchingAgents = previous.agentId !== agent.id;

    // 保存还在途时，父层会乐观发布这些字段。但那份快照和之后可能的回滚都不是
    // 已确认的服务端状态，所以失败重试期间保留已提交的值，继续可编辑。
    if (!switchingAgents && saving) return;

    const local = localRef.current;
    const wasLocallyDirty =
      local.instructions !== previous.instructions ||
      local.conversationStartersKey !== previous.conversationStartersKey;
    const persistedContentsChanged =
      previous.instructions !== (agent.instructions ?? "") ||
      previous.conversationStartersKey !== persistedConversationStartersKey;

    persistedRef.current = {
      agentId: agent.id,
      instructions: agent.instructions ?? "",
      conversationStartersKey: persistedConversationStartersKey,
    };
    if (switchingAgents || (!wasLocallyDirty && persistedContentsChanged)) {
      setValue(agent.instructions ?? "");
      setConversationStarters(
        JSON.parse(persistedConversationStartersKey) as AgentConversationStarter[],
      );
    }
  }, [agent.id, agent.instructions, persistedConversationStartersKey, saving]);

  // 从聊天空态里的“定制”而来：把开场白编辑区滚进视野并闪一下光环，让深链落在
  // “这个设置”上，而不是落在某条恰好包含它的长页面。参数到达即消费，所以刷新
  // 不会重放没人要的动画——但每次点击必须生效：包括从仍开着的聊天窗口再点一次，
  // 以及指向另一个 Agent 的深链。
  useEffect(() => {
    if (!navigation || !conversationStartersSupported) return;

    if (navigation.searchParams.get("focus") !== AGENT_FOCUS_CONVERSATION_STARTERS) {
      // 参数已不在：要么是普通访问，要么下面的 `replace` 已生效。重新武装，
      // 让下一次点击能再次聚焦。
      focusHandledForAgentRef.current = null;
      return;
    }
    // 防止在“剥掉参数的 URL”落地前重复执行——适配器对象引用不稳定，下面的
    // `replace` 自身引发的渲染会再次触发本 effect。按 Agent 做键，指向“另一个”
    // Agent 的深链绝不会被这个闩锁吞掉。
    if (focusHandledForAgentRef.current === agent.id) return;
    focusHandledForAgentRef.current = agent.id;

    // 刻意用 `block: "nearest"`：原生 scrollIntoView 会滚动所有可滚祖先，
    // 桌面端会把整个外壳都拖走（#3929）。
    conversationStartersRef.current?.scrollIntoView?.({ block: "nearest" });
    setConversationStartersFocused(true);

    const params = new URLSearchParams(navigation.searchParams);
    params.delete("focus");
    const query = params.toString();
    navigation.replace(
      query ? `${navigation.pathname}?${query}` : navigation.pathname,
    );

    // 定时器存在 ref 里，而不是作为本 effect 的 cleanup 返回：适配器对象不稳定，
    // 用 cleanup 会在下一次渲染就被调用，光环还没开始就被取消、永远亮着。
    // 卸载时在下方清除。重复聚焦会重启计时，而不是继承上次剩余的时长。
    if (focusFlashTimerRef.current) clearTimeout(focusFlashTimerRef.current);
    focusFlashTimerRef.current = setTimeout(
      () => setConversationStartersFocused(false),
      FOCUS_FLASH_MS,
    );
  }, [agent.id, navigation, conversationStartersSupported]);

  useEffect(
    () => () => {
      if (focusFlashTimerRef.current) clearTimeout(focusFlashTimerRef.current);
    },
    [],
  );

  // 向上汇报 dirty，让父层能拦截 Tab 切换。
  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave({
        instructions: value,
        ...(conversationStartersSupported
          ? { conversation_starters: conversationStarters }
          : {}),
      });
    } catch {
      // toast handled by parent
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-5">
      <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.instructions.intro)}
      </p>

      <div className="space-y-2">
        <label
          htmlFor={`agent-system-prompt-${agent.id}`}
          className="text-body font-medium"
        >
          {t(($) => $.tab_body.instructions.system_prompt_label)}
        </label>
        <Textarea
          id={`agent-system-prompt-${agent.id}`}
          name="agent-system-prompt"
          autoComplete="off"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder={t(($) => $.tab_body.instructions.placeholder)}
          rows={18}
          className="min-h-96 resize-y leading-6"
        />
      </div>

      {conversationStartersSupported ? (
        <div
          ref={conversationStartersRef}
          className={cn(
            "scroll-mt-4 rounded-lg transition-shadow duration-500",
            conversationStartersFocused && "ring-2 ring-brand/50 ring-offset-2 ring-offset-background",
          )}
        >
          <ConversationStartersEditor
            value={conversationStarters}
            onChange={setConversationStarters}
          />
        </div>
      ) : null}

      <div className="flex items-center justify-end gap-3">
        {isDirty && (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.common.unsaved_changes)}
          </span>
        )}
        <Button
          size="sm"
          onClick={handleSave}
          disabled={!isDirty || !conversationStartersValid || saving}
        >
          {saving ? (
            <Loader2
              className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
              aria-hidden="true"
            />
          ) : (
            <Save className="h-3.5 w-3.5" aria-hidden="true" />
          )}
          {t(($) => $.tab_body.common.save)}
        </Button>
      </div>
    </div>
  );
}

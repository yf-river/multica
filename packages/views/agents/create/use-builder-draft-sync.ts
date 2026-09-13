"use client";

// 本文件是 AI 创建会话“配置与服务端同步”的 hook：到达时恢复已存配置，用户编辑时
// 防抖写回；卸载（切 Tab / 换路由 / 切会话）时把防抖窗口里最后的编辑立即冲掉。
// 顺序即一切：恢复跑完前自动保存绝不武装。

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import {
  fromStoredAgentDraft,
  storedAgentDraftsEqual,
  toStoredAgentDraft,
  type AgentDraft,
  type StoredAgentDraft,
} from "@multica/core/agents";
import { api } from "@multica/core/api";

/** 停止编辑多久后把配置写回服务端。 */
const AUTOSAVE_DELAY_MS = 800;

interface BuilderDraftSync {
  /**
   * 已存配置被应用（或判定不存在）之前为 false。页面在它翻转前绝不能合并助手
   * 的 `<agent_draft>`，否则回复会落进一张空草稿、紧接着又被恢复覆盖。
   */
  restored: boolean;
  /** 草稿已体现在表单里的那条助手消息 id。 */
  appliedMessageId: string | null;
  markApplied: (messageId: string) => void;
}

/**
 * 让一条会话的配置与服务端保持同步：到达时恢复，用户编辑时写回。
 *
 * 顺序是全部要点。自动保存只在恢复跑完之后才武装——表单还拿着空初始态时来一发
 * 保存，会用“什么没有”覆盖已存配置，把“重新打开我的草稿”变成“丢掉我的草稿”。
 * `restored` 出于同一原因也管着助手草稿的合并。
 *
 * `appliedMessageId` 与配置一起持久化，因为正是它阻止恢复把最后一次回复重放到
 * 那之后的编辑上。只放内存里它随页面一起死掉——而下次恢复恰恰需要它。
 */
export function useBuilderDraftSync(options: {
  sessionId: string;
  /** 已存配置；从没存过则为 null。 */
  stored: StoredAgentDraft | null | undefined;
  /** 已存配置仍在拉取时为 false。 */
  storedSettled: boolean;
  /** carrier 的运行环境——归服务端所有，绝不取自本地存的副本。 */
  runtimeId: string;
  draft: AgentDraft;
  setDraft: Dispatch<SetStateAction<AgentDraft>>;
}): BuilderDraftSync {
  const { sessionId, stored, storedSettled, runtimeId, draft, setDraft } =
    options;

  const [restored, setRestored] = useState(false);
  const appliedMessageIdRef = useRef<string | null>(null);
  // 已知在服务端的最新形态——草稿没变就不发请求。恢复时也会喂它，否则“刚读回就
  // 立即保存一遍”就发生了。
  const savedRef = useRef<StoredAgentDraft | null>(null);

  useEffect(() => {
    if (!sessionId || restored || !storedSettled) return;
    if (stored) {
      appliedMessageIdRef.current = stored.applied_message_id;
      savedRef.current = stored;
      setDraft(fromStoredAgentDraft(stored, runtimeId));
    }
    setRestored(true);
  }, [restored, runtimeId, sessionId, setDraft, stored, storedSettled]);

  // 防抖正在持有的内容——卸载 effect 靠它把最后一份发出去。没有它，在防抖窗口内
  // 导航离开就会静默丢掉最后几次编辑——恰恰是用户最可能刚打的那几下。
  const pendingRef = useRef<StoredAgentDraft | null>(null);
  const save = useCallback(
    (next: StoredAgentDraft) => {
      savedRef.current = next;
      pendingRef.current = null;
      void api.saveAgentBuilderDraft(sessionId, next).catch(() => {
        // 尽力而为：下一次编辑会重试，丢一次写入用户也看不见损失。在这里弹错误
        // 横幅，等于为一件会自愈的事打断打字。
        savedRef.current = null;
      });
    },
    [sessionId],
  );
  // 经 ref 读取，供下方卸载 effect 使用——它不能重跑，因此不能依赖任何东西。
  const saveRef = useRef(save);
  saveRef.current = save;

  useEffect(() => {
    if (!sessionId || !restored) return;
    const next = toStoredAgentDraft(draft, appliedMessageIdRef.current);
    if (savedRef.current && storedAgentDraftsEqual(savedRef.current, next)) {
      return;
    }
    pendingRef.current = next;
    const timer = setTimeout(() => save(next), AUTOSAVE_DELAY_MS);
    return () => clearTimeout(timer);
  }, [draft, restored, save, sessionId]);

  // 卸载是截止线：切 Tab、换路由、切会话都会拆掉这一切，防抖计时器随之消亡。
  useEffect(
    () => () => {
      const pending = pendingRef.current;
      if (pending) saveRef.current(pending);
    },
    [],
  );

  return {
    restored,
    appliedMessageId: appliedMessageIdRef.current,
    markApplied: (messageId: string) => {
      appliedMessageIdRef.current = messageId;
    },
  };
}

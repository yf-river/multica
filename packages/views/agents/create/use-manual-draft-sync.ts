"use client";

// 本文件是“手动创建 Agent”表单的持久化同步 hook：把草稿按“流程拥有者”存进
// core 的 store，让跨页面/刷新不丢（#6287）。恢复必须先于保存武装；写入只限
// 本流程的槽位，绝不越权动其它流程的草稿。

import {
  useEffect,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import {
  fromStoredAgentDraft,
  manualDraftOwner,
  setManualDraftEntry,
  toStoredAgentDraft,
  useManualAgentDraftStore,
  type AgentDraft,
} from "@multica/core/agents";

/**
 * 让手动创建表单跨导航存活（#6287）。
 *
 * 被报告的 bug 是切 Tab：桌面外壳只挂载当前 Tab，切回来就是一次重挂载，
 * 之前输入的内容全没了。换路由和刷新同样会丢，所以修复必须是“持久化”，
 * 而不是只守一条出口的守卫。
 *
 * 两个顺序都很要紧，它们在被写成规则之前都是 bug：
 *
 *   - 恢复必须先于保存被武装。表单还拿着空初始态时来一发写入，会用“什么没有”
 *     覆盖已存的草稿——把“回到我的表单”变成“丢掉我的表单”。
 *   - 写入限定在本流程自己的槽位。开一张空白表单绝不能抹掉某人做到一半的
 *     复制草稿，反之亦然；store 精确地按拥有者分键，正是一个流程不能替另一个
 *     流程作答。
 */
export function useManualDraftSync(options: {
  duplicateId: string | null;
  draft: AgentDraft;
  setDraft: Dispatch<SetStateAction<AgentDraft>>;
  /** 复制草稿播完种之前为 false；提前保存会把“待播种替换的空表单”持久化。 */
  ready: boolean;
}): void {
  const { duplicateId, draft, setDraft, ready } = options;
  const owner = manualDraftOwner(duplicateId);

  const setStored = useManualAgentDraftStore((state) => state.setDraft);
  const [restored, setRestored] = useState(false);

  useEffect(() => {
    if (restored || !ready) return;
    // 在应用时刻读取而非订阅：本 effect 只跑一次，订阅反而会在每次键入的
    // 写入中反复触发。
    const persisted = useManualAgentDraftStore.getState().draft.byOwner[owner];
    if (persisted) {
      setDraft(fromStoredAgentDraft(persisted.draft, persisted.runtimeId));
    }
    setRestored(true);
  }, [owner, ready, restored, setDraft]);

  useEffect(() => {
    if (!restored) return;
    const current = useManualAgentDraftStore.getState().draft;
    setStored(
      setManualDraftEntry(current, owner, {
        runtimeId: draft.runtimeId,
        draft: toStoredAgentDraft(draft, null),
      }),
    );
  }, [draft, owner, restored, setStored]);
}

/** Agent 已建成后清掉本流程的槽位；其它流程不受影响。 */
export function clearManualDraftForOwner(duplicateId: string | null): void {
  const store = useManualAgentDraftStore.getState();
  store.setDraft(
    setManualDraftEntry(store.draft, manualDraftOwner(duplicateId), null),
  );
}

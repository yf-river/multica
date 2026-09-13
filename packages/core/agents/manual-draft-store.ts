"use client";

// 手动创建 Agent 表单的草稿 store：把表单内容跨导航保存下来。
import { createDraftStore } from "../drafts/create-draft-store";
import type { StoredAgentDraft } from "../types";
import { EMPTY_AGENT_DRAFT } from "./draft";
import { storedAgentDraftsEqual, toStoredAgentDraft } from "./stored-draft";

/**
 * 手动创建表单的内容，跨导航保持。
 *
 * AI 路线的配置存在服务端，以它所属的对话为 key。手动路线没有对话、服务端也没有
 * 可挂载草稿的地方，所以本地持久化——这足够扛住它需要扛住的：切标签页、换路由、
 * 刷新（#6287）。
 *
 * 草稿按"正在创建的是什么"分 key，而不是每次只存一份。空白 Agent 和复制某 Agent X
 * 是两件不同的活儿，复制 X 也不是复制 Y；单一槽位会在用户打开另一个（还没输入任何
 * 内容）时就毁掉前一个。形状与 chat composer 为每次会话草稿用的相同。
 */
export interface ManualDraftEntry {
  /** 这里包含 runtime（不同于 AI 路线）：没有载体 Agent 能对 runtime 提供权威值。 */
  runtimeId: string;
  draft: StoredAgentDraft;
}

/** 手动创建草稿集合：按"创建对象"分组存放多条草稿。 */
export interface ManualAgentDrafts {
  /** 以 {@link manualDraftOwner} 为 key。 */
  byOwner: Record<string, ManualDraftEntry>;
}

/** 空手动草稿条目（对应一份全新的空白表单）。 */
export const EMPTY_MANUAL_DRAFT_ENTRY: ManualDraftEntry = {
  runtimeId: "",
  draft: toStoredAgentDraft(EMPTY_AGENT_DRAFT, null),
};

/** 空白（非复制）创建流程的草稿 key。 */
export const MANUAL_DRAFT_BLANK_OWNER = "blank";

/** 计算一份草稿的归属 key：复制某 Agent 时按其 id、否则用"blank"。 */
export function manualDraftOwner(duplicateId: string | null): string {
  return duplicateId ? `duplicate:${duplicateId}` : MANUAL_DRAFT_BLANK_OWNER;
}

/**
 * 判断一条草稿条目是否值得保留：与一张全新表单有任何不同就算。
 *
 * 刻意整条比较而不是逐字段列举。枚举式谓词会在草稿新增字段之后静默漏掉它——
 * 而且失败不可见：只要另外*某个*字段也填了，新字段照样能存下来。这个函数的第一版
 * 列了六个字段，把"只改了 model"或"只改了访问权限"的编辑直接丢在地上了。
 *
 * runtime 是唯一绝不能算进去的一项，而它本就不在这次比较之内：表单每次进入都会用
 * 种子填 runtime，把它算进去会在用户没碰过的表单上存草稿，并为每个曾打开复制的 Agent
 * 长出死槽位。它也正因为此放在 entry 上、而非 draft 里。
 * @param entry 手动创建草稿条目。
 * @returns 是否含有与空表单不同的内容。
 */
export function manualDraftEntryHasContent(entry: ManualDraftEntry): boolean {
  return !storedAgentDraftsEqual(entry.draft, EMPTY_MANUAL_DRAFT_ENTRY.draft);
}

/**
 * 只写某一个归属者的条目、不动其它条目；条目无内容时整条删除。
 * 因此清空表单会移除它的槽位，而不是停放一份空白草稿——这也是让 map 不会为每个
 * 曾打开复制的 Agent 长出一把死 key 的原因。
 * @param drafts 现有草稿集合。
 * @param owner 草稿归属 key。
 * @param entry 新条目；传 null 或无内容时删除该归属的草稿。
 * @returns 更新后的草稿集合。
 */
export function setManualDraftEntry(
  drafts: ManualAgentDrafts,
  owner: string,
  entry: ManualDraftEntry | null,
): ManualAgentDrafts {
  const byOwner = { ...drafts.byOwner };
  if (entry && manualDraftEntryHasContent(entry)) {
    byOwner[owner] = entry;
  } else {
    delete byOwner[owner];
  }
  return { byOwner };
}

const EMPTY_MANUAL_AGENT_DRAFTS: ManualAgentDrafts = { byOwner: {} };

/** 手动创建 Agent 草稿的持久化 store（空态时提供空集合，有内容才落盘）。 */
export const useManualAgentDraftStore = createDraftStore<ManualAgentDrafts>({
  storageKey: "multica:agents:manual-draft",
  emptyData: EMPTY_MANUAL_AGENT_DRAFTS,
  hasMeaningful: (drafts) =>
    Object.values(drafts.byOwner).some(manualDraftEntryHasContent),
});

// Agent 创建工作室（Studio）与"隐藏的构建 Agent"之间的通信协议：
// 出站请求的编码、入站 <agent_draft> 块的解析/剥离、以及把块合并回表单草稿。
import type { RuntimeDevice, RuntimeModel } from "../types";
import {
  AGENT_CONVERSATION_STARTER_LABEL_MAX_LENGTH,
  AGENT_CONVERSATION_STARTER_MAX_LENGTH,
  AGENT_CONVERSATION_STARTERS_MAX,
} from "./constants";
import type { AgentDraft } from "./draft";

/**
 * Agent 创建工作室与隐藏的构建 Agent 之间的线上格式。
 *
 * 两个不对称的方向：
 *   - 出站：编辑框的纯文本被包进一个 JSON 信封里，同时还带着当前草稿和构建 Agent
 *     可挑 id 的目录，让每一轮都完整重述全部决策上下文。
 *   - 入站：构建 Agent 在自己的自然语言回复末尾追加一个 `<agent_draft>` JSON 块；
 *     该块更新表单，并在消息渲染前被剥离。
 *
 * 两个方向都做防御性解析：CLI 支撑的模型可能吐略畸形的 JSON；解析失败的草稿必须
 * 退化成"不更新表单"而不是弄断对话。
 */
const BUILDER_INPUT_PREFIX = "MULTICA_AGENT_BUILDER_INPUT\n";

/** 构建 Agent 回复里 `<agent_draft>` 块的载荷形态（字段全 unknown，需逐个校验）。 */
export interface BuilderDraftPayload {
  name?: unknown;
  description?: unknown;
  instructions?: unknown;
  conversation_starters?: unknown;
  model?: unknown;
  skill_ids?: unknown;
  permission_scope?: unknown;
  member_ids?: unknown;
}

/** 从回复里解析 `<agent_draft>...</agent_draft>` 块；没有块或解析失败返回 null。 */
export function parseBuilderDraft(content: string): BuilderDraftPayload | null {
  const match = content.match(/<agent_draft>([\s\S]*?)<\/agent_draft>/);
  if (!match?.[1]) return null;
  try {
    const value = JSON.parse(match[1]);
    return value && typeof value === "object"
      ? (value as BuilderDraftPayload)
      : null;
  } catch {
    // 有些 CLI 支撑的模型即使用紧凑 JSON 也会在 Markdown 指令字符串里放出裸换行。
    // 只修复字符串内部出现的 JSON 控制字符；对象结构及其它语法仍须通过 JSON.parse。
    try {
      const value = JSON.parse(escapeJsonStringControlCharacters(match[1]));
      return value && typeof value === "object"
        ? (value as BuilderDraftPayload)
        : null;
    } catch {
      return null;
    }
  }
}

/** 把 JSON 字符串值里的裸控制字符（换行/回车/制表）转义成合法的 JSON 转义序列。 */
function escapeJsonStringControlCharacters(value: string): string {
  let result = "";
  let inString = false;
  let escaped = false;

  for (const character of value) {
    if (!inString) {
      result += character;
      if (character === '"') inString = true;
      continue;
    }
    if (escaped) {
      result += character;
      escaped = false;
      continue;
    }
    if (character === "\\") {
      result += character;
      escaped = true;
      continue;
    }
    if (character === '"') {
      result += character;
      inString = false;
      continue;
    }
    if (character === "\n") {
      result += "\\n";
    } else if (character === "\r") {
      result += "\\r";
    } else if (character === "\t") {
      result += "\\t";
    } else {
      result += character;
    }
  }

  return result;
}

/**
 * 在人类阅读之前把结构化块从回复里移除。块是机制——它驱动配置表单——而表单里
 * 已经能看到它的效果，所以会话记录只留散文。
 *
 * 两种模式，不是一种。闭合形式是历史保存的样子。未闭合形式是流式输出的样子：
 * 回复按 token 逐字到达，所以在吐出 JSON 的这几秒里只有开标签没有闭标签——
 * 闭合形式匹配不到它——以前每次轮转，裸载荷都会滚过读者的视线。
 *
 * 开头的 `\s*` 收掉模型留在散文与块之间的空白，移除后不会多留一个空行。
 */
export function stripBuilderDraft(content: string): string {
  return content
    .replace(/\s*<agent_draft>[\s\S]*?<\/agent_draft>/g, "")
    .replace(/\s*<agent_draft>[\s\S]*$/, "")
    .trim();
}

/**
 * 把用户请求编码成发给构建 Agent 的出站信封：请求文本 + 当前草稿 + 可选目录
 * （运行时、模型、技能、成员），供其从中挑选。
 */
export function encodeBuilderInput(
  request: string,
  draft: AgentDraft,
  skills: Array<{ id: string; name: string; description: string }>,
  members: Array<{ user_id: string; name: string }>,
  runtime: Pick<RuntimeDevice, "id" | "name" | "provider"> | null,
  models: RuntimeModel[] | null,
): string {
  return (
    BUILDER_INPUT_PREFIX +
    JSON.stringify(
      {
        user_request: request,
        current_draft: {
          name: draft.name,
          description: draft.description,
          instructions: draft.instructions,
          conversation_starters: draft.conversationStarters,
          model: draft.model,
          skill_ids: [...draft.skillIds],
          permission_scope: draft.permissionScope,
          member_ids: [...draft.memberIds],
        },
        selected_runtime: runtime
          ? {
              id: runtime.id,
              name: runtime.name,
              provider: runtime.provider,
            }
          : null,
        available_runtime_models:
          models === null
            ? null
            : models.map((model) => ({
                id: model.id,
                label: model.label,
                provider: model.provider,
              })),
        available_workspace_skills: skills.map((skill) => ({
          id: skill.id,
          name: skill.name,
          description: skill.description,
        })),
        available_workspace_members: members.map((member) => ({
          id: member.user_id,
          name: member.name,
        })),
      },
      null,
      2,
    )
  );
}

/** 从编码过的入站内容里还原出原始用户请求（历史展示用）；不含前缀则原样返回。 */
export function decodeBuilderInput(content: string): string {
  if (!content.startsWith(BUILDER_INPUT_PREFIX)) return content;
  try {
    const parsed = JSON.parse(content.slice(BUILDER_INPUT_PREFIX.length)) as {
      user_request?: unknown;
    };
    return typeof parsed.user_request === "string"
      ? parsed.user_request
      : content;
  } catch {
    return content;
  }
}

/** 一份可被构建器输入框采用的恢复内容：会话 id + 原始文本。 */
export interface BuilderRestore {
  id: string;
  content: string;
}

/**
 * 选择构建器输入框应采用哪份被取消的提示词（#5219）。
 *
 * 两个来源、永不同时出现：取消一个 daemon 从未启动的任务会同步应答
 * （`cancelled_chat_message.restore_to_input`）；而取消"已启动但还没产出"的任务会把
 * 判断推迟，稍后把提示词作为一条持久的 chat_draft_restore 行送达。持久副本是原始
 * chat_message 内容，也就是仍处于构建器编码线格式——所以这里要像同步路径解码它自己的
 * 那份一样来解码。
 *
 * 会话 id 刻意不沿用：构建器输入框用自己的草稿 key `agent-builder:<id>`，
 * 所以 ChatInput 的会话守卫永远匹配不上裸会话 id——也不需要匹配，因为这个输入框
 * 永远只展示构建器会话。
 */
export function pickBuilderRestore(
  synchronous: BuilderRestore | null,
  durable: { id: string; content: string } | null,
): BuilderRestore | null {
  if (synchronous) return synchronous;
  if (!durable) return null;
  return { id: durable.id, content: decodeBuilderInput(durable.content) };
}

/**
 * 把构建 Agent 返回的草稿补丁合并进当前表单草稿。所有字段逐个防御性校验：
 * 类型不对、或引用了无效 id（不在传入的白名单里）就保留当前值——构建 Agent 的
 * 一次"发现失败"绝不能变成"放行任意输入"。
 */
export function mergeBuilderDraft(
  current: AgentDraft,
  payload: BuilderDraftPayload,
  validSkillIds: Set<string>,
  validMemberIds: Set<string>,
  validModelIds: ReadonlySet<string> | null,
): AgentDraft {
  const scope =
    payload.permission_scope === "workspace" ||
    payload.permission_scope === "members" ||
    payload.permission_scope === "private"
      ? payload.permission_scope
      : current.permissionScope;
  const skillIds = Array.isArray(payload.skill_ids)
    ? payload.skill_ids.filter(
        (id): id is string => typeof id === "string" && validSkillIds.has(id),
      )
    : [...current.skillIds];
  const memberIds = Array.isArray(payload.member_ids)
    ? payload.member_ids.filter(
        (id): id is string => typeof id === "string" && validMemberIds.has(id),
      )
    : [...current.memberIds];
  // 当前值可能是 ModelDropdown 里用户刻意输入的自定义条目，保留它永远安全。
  // 只有目录里的 id 才是构建 Agent 能引入的；因此"发现失败"不会变成放行态输入。
  const model =
    typeof payload.model === "string" &&
    (payload.model === current.model ||
      (validModelIds !== null &&
        validModelIds.size > 0 &&
        (payload.model === "" || validModelIds.has(payload.model))))
      ? payload.model
      : current.model;

  const conversationStarters = Array.isArray(payload.conversation_starters)
    ? payload.conversation_starters
        .slice(0, AGENT_CONVERSATION_STARTERS_MAX)
        .filter(
          (item): item is { label: string; prompt: string } =>
            !!item &&
            typeof item === "object" &&
            typeof (item as { label?: unknown }).label === "string" &&
            typeof (item as { prompt?: unknown }).prompt === "string" &&
            (item as { label: string }).label.trim().length > 0 &&
            (item as { prompt: string }).prompt.trim().length > 0,
        )
        .map((item) => ({
          label: [...item.label.trim()]
            .slice(0, AGENT_CONVERSATION_STARTER_LABEL_MAX_LENGTH)
            .join(""),
          prompt: [...item.prompt.trim()]
            .slice(0, AGENT_CONVERSATION_STARTER_MAX_LENGTH)
            .join(""),
        }))
    : current.conversationStarters;

  return {
    ...current,
    name: typeof payload.name === "string" ? payload.name : current.name,
    description:
      typeof payload.description === "string"
        ? payload.description
        : current.description,
    instructions:
      typeof payload.instructions === "string"
        ? payload.instructions
        : current.instructions,
    conversationStarters,
    model,
    // 构建 Agent 能换 model，这会作废用户为上一个 model 挑的思考/速度档位。
    // 它自己不设置这两个字段，所以 model 没变时原样保留。
    thinkingLevel: model === current.model ? current.thinkingLevel : "",
    serviceTier: model === current.model ? current.serviceTier : "",
    skillIds: new Set(skillIds),
    permissionScope: scope,
    memberIds: new Set(scope === "members" ? memberIds : []),
    teamIds: current.teamIds,
  };
}

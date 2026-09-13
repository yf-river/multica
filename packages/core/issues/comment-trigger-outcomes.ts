// 解析评论中的 @提及 结果：判断哪些提及触发了 Agent 运行、哪些被拦截，供评论发送后提示使用。
import { CommentTriggerOutcomeSchema } from "../api/schemas";
import type { CommentTriggerOutcome } from "../types/comment";

// 评论 markdown 里渲染出的“提及”的来源，捕获用户选的标签、目标类型和目标 id：
// `[@Go](mention://agent/UUID)`。用字符串保存，是为了让每次解析都构造自己的
// 全局 RegExp——跨 `matchAll` 调用共用一个全局实例会泄漏 `lastIndex`、丢匹配。
const MENTION_MARKUP_SOURCE =
  "\\[@?(.+?)\\]\\(mention:\\/\\/(member|agent|squad|issue|all)\\/([0-9a-fA-F-]+|all)\\)";

export interface ParsedMention {
  /** 用户看到的标签文本，如 `Go`。 */
  label: string;
  /** 目标类型，如 member / agent / squad / issue / all。 */
  type: string;
  /** 目标 id。 */
  id: string;
}

// 按顺序返回正文里的每一个提及。只关心子集的调用方（例如跳过 `issue` 链接、或去重）
// 可以自己再过滤结果。
export function parseMentions(content: string): ParsedMention[] {
  const re = new RegExp(MENTION_MARKUP_SOURCE, "g");
  const mentions: ParsedMention[] = [];
  for (const match of content.matchAll(re)) {
    const label = match[1];
    const type = match[2];
    const id = match[3];
    if (!label || !type || !id) continue;
    mentions.push({ label, type, id });
  }
  return mentions;
}

// 服务器返回的“被拦截”触发结果有意省略了目标的名字（枚举安全：线路上绝不泄露
// 私有目标）。但客户端已经在自己草稿/评论里渲染过那个名字，所以 composer 和
// 发送后的 toast 能从用户敲的 markdown 里给被拦截的提及打标签——不算新的泄露。
// 返回一个 `${target_type}:${target_id}` → 标签 的映射。
export function mentionLabelsByTarget(content: string): Map<string, string> {
  const labels = new Map<string, string>();
  for (const { label, type, id } of parseMentions(content)) {
    labels.set(`${type}:${id}`, label);
  }
  return labels;
}

// 用户为某个被拦截结果敲下的标签；无法关联时返回 undefined
// （例如那个提及被编辑掉了）。调用方回退到不带名字的原因文案，警告仍能显示。
export function blockedTriggerLabel(
  outcome: { target_type: string; target_id: string },
  labels: Map<string, string>,
): string | undefined {
  return labels.get(`${outcome.target_type}:${outcome.target_id}`);
}

// 校验创建/编辑评论响应里的 `trigger_outcomes`（MUL-4525 §2）。
// 创建/编辑响应没有做完整的 schema 解析，所以 UI 会分支判断的那个字段在这里单独校验：
// 非数组返回 []，格式错误的条目单独丢弃，而不是让整组失败。
export function parseCommentTriggerOutcomes(raw: unknown): CommentTriggerOutcome[] {
  if (!Array.isArray(raw)) return [];
  const out: CommentTriggerOutcome[] = [];
  for (const item of raw) {
    const parsed = CommentTriggerOutcomeSchema.safeParse(item);
    if (parsed.success) {
      out.push(parsed.data as CommentTriggerOutcome);
    }
  }
  return out;
}

// 唯一算“成功”的结果状态：这个提及确实被处理了（一次运行被排队、合并进已有运行、
// 或被有意推迟）。成功是“白名单”，而不是“除了 blocked 之外的都算”，所以未知/未来的
// 状态——或 schema 对格式错误条目默认出的空状态——永远不当作成功（MUL-4525；
// 与 Run 的 now 白名单一致）。
const HANDLED_TRIGGER_STATUSES = new Set(["queued", "coalesced", "deferred"]);

// 那些“没有明确触发”的显式 @agent / @squad 提及，是“已发布、但有 N 个未触发”警告
// 要覆盖的对象：`blocked` 加上任何未知/未来/空状态。绝不要假设未识别的状态是成功。
export function unhandledCommentTriggerOutcomes(raw: unknown): CommentTriggerOutcome[] {
  return parseCommentTriggerOutcomes(raw).filter((o) => !HANDLED_TRIGGER_STATUSES.has(o.status));
}

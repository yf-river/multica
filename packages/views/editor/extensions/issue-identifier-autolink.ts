/**
 * IssueIdentifierAutolink——在可编辑编辑器里对裸 issue 标识符（如 `MUL-123`）
 * 做 Linear 风格的实时自动链接。
 *
 * 用户打完一个裸标识符、后面敲下边界字符（空格/标点），或粘贴的文本里
 * 含有标识符时，已完成的 token 会被拿去工作区解析——精确命中时——替换成
 * 真正的 `issue` mention 节点。保存时该 mention 序列化为规范形态
 * `[MUL-123](mention://issue/<uuid>)`。
 *
 * 解析是异步的，所以这不是同步的 Tiptap InputRule/PasteRule。一个
 * ProseMirror 插件把用户事务引入的**具体候选区间**——输入时是光标前那个
 * token，粘贴时是粘贴片里的 tokens——捕获进插件 state，并在后续每个事务
 * 上向前映射这些区间，让它们在异步间隙里保持最新。插件的 `view` 解析
 * 捕获到的标识符，只替换那些映射后的区间（替换前会复核该区间仍恰好是
 * 这个标识符、边界完好、且没有 code/link mark）。它绝不按标识符全文档
 * 重扫，所以用户没碰过的同一标识符的既有或非粘贴来源出现不会被动到。
 * 解析失败与错误会缓存，保证一个 token 在每个编辑会话中最多解析一次。
 *
 * 只有真正的用户编辑才会种下候选（绝不含程序化的 setContent），所以仅仅
 * 打开既有内容不会重写它。解析器由编辑器装配层注入（一个 ref），装配层
 * 拥有 React Query 与工作区上下文；本扩展绝不碰 React hooks。
 */
import { Extension } from "@tiptap/core";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import type { EditorState, Transaction } from "@tiptap/pm/state";
import type { EditorView } from "@tiptap/pm/view";
import type { Mark, Node as PMNode, NodeType } from "@tiptap/pm/model";
import type { RefObject } from "react";

/** 一次成功解析返回的引用：issue 的稳定身份（UUID）与规范展示标识。 */
export interface ResolvedIssueRef {
  /** issue 的 UUID。 */
  id: string;
  /** 服务端返回的规范标识符，如 "MUL-123"。 */
  identifier: string;
}

/**
 * 把一个裸标识符解析成真实 issue 引用的异步函数；查无此 issue 时
 * resolve 为 null。
 */
export type IssueIdentifierResolver = (
  identifier: string,
) => Promise<ResolvedIssueRef | null>;

/** createIssueIdentifierAutolinkExtension 的配置选项。 */
export interface IssueIdentifierAutolinkOptions {
  /**
   * 解析器的 ref。用 ref（而非裸函数）是为了编辑器只创建一次，
   * 而解析器总能读到最新的工作区上下文。
   */
  resolveRef: RefObject<IssueIdentifierResolver | undefined>;
}

// 带边界限定、区分大小写的标识符——与 @multica/ui/markdown 里的只读
// 探测器同形。
const IDENTIFIER_RE = /(?<![A-Za-z0-9_-])([A-Z][A-Z0-9]*-\d+)(?![A-Za-z0-9_-])/g;
const BOUNDARY_RE = /[A-Za-z0-9_-]/;

// 标在我们自己替换事务上的元数据，值为这批事务消费掉的待处理 key 列表，
// 让 `apply` 能丢掉它们、并且绝不会把这次替换当作用户输入。
const META_REMOVE = "issueIdentifierAutolinkRemove";

/** 候选：文档里一处完整的裸标识符及其区间。 */
interface Candidate {
  identifier: string;
  from: number;
  to: number;
}

/** 带唯一 key 的候选，把「捕获 → 异步解析 → 替换」三个阶段关联起来。 */
interface PendingCandidate extends Candidate {
  /** 每个插件内唯一的 key。 */
  key: number;
}

/** 插件 state：待处理候选列表 + 发号器。 */
interface AutolinkPluginState {
  pending: PendingCandidate[];
  seq: number;
}

const pluginKey = new PluginKey<AutolinkPluginState>("issueIdentifierAutolink");

/** 绝不能自动链接的文本节点（代码与显式链接语境）。 */
function isSkippedTextNode(
  marks: readonly Mark[],
  parent: PMNode | null,
): boolean {
  if (marks.some((m) => m.type.name === "code" || m.type.name === "link")) {
    return true;
  }
  if (parent && parent.type.name === "codeBlock") return true;
  return false;
}

/**
 * 完整的、独立的标识符 token——其区间与 [rangeFrom, rangeTo) 相交。
 * 「完整」意味着同一文本节点内已存在结尾边界字符——还正在输入、没有收尾
 * 的 token 会被跳过。
 */
function collectCandidates(
  state: EditorState,
  rangeFrom = 0,
  rangeTo = Number.POSITIVE_INFINITY,
): Candidate[] {
  const out: Candidate[] = [];
  state.doc.descendants((node, pos, parent) => {
    if (!node.isText || !node.text) return;
    if (isSkippedTextNode(node.marks, parent)) return;
    const text = node.text;
    IDENTIFIER_RE.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = IDENTIFIER_RE.exec(text)) !== null) {
      const identifier = m[1];
      if (!identifier) continue;
      const localEnd = m.index + identifier.length;
      if (localEnd >= text.length) continue; // 节点内没有收尾边界字符
      const from = pos + m.index;
      const to = pos + localEnd;
      if (to <= rangeFrom || from >= rangeTo) continue; // 在窗口之外
      out.push({ identifier, from, to });
    }
  });
  return out;
}

/** 一次事务的改动所覆盖的绝对 [from,to) 区间；没有改动返回 null。 */
function changedRange(tr: Transaction): { from: number; to: number } | null {
  let from = Number.POSITIVE_INFINITY;
  let to = -1;
  tr.mapping.maps.forEach((map) => {
    map.forEach((_oldStart, _oldEnd, newStart, newEnd) => {
      from = Math.min(from, newStart);
      to = Math.max(to, newEnd);
    });
  });
  if (to < 0) return null;
  return { from, to };
}

/**
 * 单个用户事务引入的候选：
 *   - 粘贴：只取粘贴区间**内部**的每个完整标识符
 *   - 输入：光标前紧邻的那个 token，即刚被敲下的边界字符补全的「前一个
 *     token」
 */
function candidatesFromUserTransaction(
  tr: Transaction,
  state: EditorState,
): Candidate[] {
  const isPaste =
    tr.getMeta("paste") === true || tr.getMeta("uiEvent") === "paste";

  if (isPaste) {
    const range = changedRange(tr);
    if (!range) return [];
    return collectCandidates(state, range.from, range.to);
  }

  const caret = state.selection.from;
  const target = collectCandidates(state).find((c) => c.to === caret - 1);
  return target ? [target] : [];
}

/**
 * 校验映射后的候选区间是否仍恰好是 `identifier` 的纯文本、且边界完好——
 * 这是替换前的异步安全网。
 */
function rangeStillPlainIdentifier(
  state: EditorState,
  from: number,
  to: number,
  identifier: string,
): boolean {
  const size = state.doc.content.size;
  if (from < 0 || to > size || from >= to) return false;
  if (state.doc.textBetween(from, to) !== identifier) return false;

  let ok = true;
  state.doc.nodesBetween(from, to, (node, _pos, parent) => {
    if (node.isText && isSkippedTextNode(node.marks, parent)) ok = false;
  });
  if (!ok) return false;

  const before = from > 0 ? state.doc.textBetween(from - 1, from) : "";
  const after = to < size ? state.doc.textBetween(to, to + 1) : "";
  if (before && BOUNDARY_RE.test(before)) return false;
  if (after && BOUNDARY_RE.test(after)) return false;
  return true;
}

/**
 * 创建裸 issue 标识符自动链接扩展。
 *
 * @param options 见 IssueIdentifierAutolinkOptions。
 * @returns 一个 Tiptap 扩展；schema 没有 mention 节点时该扩展不做任何事。
 */
export function createIssueIdentifierAutolinkExtension(
  options: IssueIdentifierAutolinkOptions,
): Extension {
  return Extension.create({
    name: "issueIdentifierAutolink",

    addProseMirrorPlugins() {
      const maybeMentionType = this.editor.schema.nodes.mention;
      if (!maybeMentionType) return [];
      // 赋给一个确定有值的常量，让收窄在嵌套的插件 `view`/`applyReady`
      // 闭包里也能成立。
      const mentionType: NodeType = maybeMentionType;
      const resolveRef = options.resolveRef;

      return [
        new Plugin<AutolinkPluginState>({
          key: pluginKey,
          state: {
            init: () => ({ pending: [], seq: 0 }),
            apply(tr, value, _oldState, newState): AutolinkPluginState {
              let pending = value.pending;
              let seq = value.seq;

              // 1. 让捕获的区间在每次文档变化后保持最新，并丢掉被本次编辑
              //    弄失效的区间（折叠为空）。
              if (tr.docChanged && pending.length > 0) {
                pending = pending
                  .map((c) => ({
                    ...c,
                    from: tr.mapping.map(c.from, 1),
                    to: tr.mapping.map(c.to, -1),
                  }))
                  .filter((c) => c.from < c.to);
              }

              // 2. 丢掉被我们自己替换事务消费掉的候选。
              const remove = tr.getMeta(META_REMOVE) as number[] | undefined;
              if (remove && remove.length > 0) {
                const rm = new Set(remove);
                pending = pending.filter((c) => !rm.has(c.key));
              }

              // 3. 只从真正的用户编辑里捕获新候选（绝不来自程序化 setContent
              //    或我们自己的替换）。
              const isUserEdit =
                tr.docChanged && !tr.getMeta("preventUpdate") && !remove;
              if (isUserEdit) {
                const fresh = candidatesFromUserTransaction(tr, newState);
                if (fresh.length > 0) {
                  pending = pending.concat(
                    fresh.map((c) => ({ key: seq++, ...c })),
                  );
                }
              }

              if (pending === value.pending && seq === value.seq) return value;
              return { pending, seq };
            },
          },
          view(view) {
            // identifier → 解析出的引用（null = 未命中）；每个会话至多解析一次。
            const resultCache = new Map<string, ResolvedIssueRef | null>();
            const inFlight = new Set<string>();
            let destroyed = false;
            let scheduled = false;

            // 在一个微任务里统一应用所有已就绪（解析完成）的候选，保证
            // 我们绝不在 `update` 内部同步 dispatch。
            function scheduleApply(): void {
              if (scheduled || destroyed) return;
              scheduled = true;
              void Promise.resolve().then(() => {
                scheduled = false;
                if (!destroyed) applyReady(view);
              });
            }

            function applyReady(v: EditorView): void {
              const st = pluginKey.getState(v.state);
              if (!st || st.pending.length === 0) return;

              const removeKeys: number[] = [];
              const replacements: {
                from: number;
                to: number;
                ref: ResolvedIssueRef;
              }[] = [];

              for (const c of st.pending) {
                if (!resultCache.has(c.identifier)) continue;
                const ref = resultCache.get(c.identifier);
                removeKeys.push(c.key); // 无论成败都处理掉
                if (
                  ref &&
                  rangeStillPlainIdentifier(v.state, c.from, c.to, c.identifier)
                ) {
                  replacements.push({ from: c.from, to: c.to, ref });
                }
              }
              if (removeKeys.length === 0) return;

              const { tr } = v.state;
              // 从右到左替换，保证靠前的区间在后来的改动里仍然有效。
              replacements.sort((a, b) => b.from - a.from);
              for (const r of replacements) {
                tr.replaceWith(
                  r.from,
                  r.to,
                  mentionType.create({
                    id: r.ref.id,
                    label: r.ref.identifier,
                    type: "issue",
                  }),
                );
              }
              tr.setMeta(META_REMOVE, removeKeys);
              v.dispatch(tr);
            }

            return {
              update() {
                if (destroyed) return;
                const resolve = resolveRef.current;
                if (!resolve) return;
                const st = pluginKey.getState(view.state);
                if (!st || st.pending.length === 0) return;

                let anyReady = false;
                for (const c of st.pending) {
                  if (resultCache.has(c.identifier)) {
                    anyReady = true;
                    continue;
                  }
                  if (inFlight.has(c.identifier)) continue;
                  inFlight.add(c.identifier);
                  Promise.resolve(resolve(c.identifier))
                    .then((ref) => {
                      resultCache.set(c.identifier, ref ?? null);
                      inFlight.delete(c.identifier);
                      scheduleApply();
                    })
                    .catch(() => {
                      // 解析失败按未命中处理，避免无限重试空转。
                      resultCache.set(c.identifier, null);
                      inFlight.delete(c.identifier);
                      scheduleApply();
                    });
                }
                if (anyReady) scheduleApply();
              },
              destroy() {
                destroyed = true;
              },
            };
          },
        }),
      ];
    },
  });
}

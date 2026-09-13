import { Extension, type Editor } from "@tiptap/core";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import type { Node as ProseMirrorNode } from "@tiptap/pm/model";

/**
 * 记录用户确实**敲下**了 suggestion 触发字符，让弹层可以拒绝因为文档里
 * 恰好有个 `@` 或 `/` 就打开。
 *
 * ## 为什么存在
 *
 * Tiptap 的 Suggestion 插件是「从状态推导」而非「事件驱动」：它在每个事务
 * 上都重跑 `findSuggestionMatch`，问的是「光标前的文本看起来像触发词吗？」，
 * 从不问「用户刚才是不是敲了一个？」。粘贴、拖放、撤销和服务端加载的文本
 * 产出的文档与敲出来的毫无二致，插件无法区分。开了 `allowSpaces` 后匹配又
 * 会从 `@` 一直吃到行尾，于是粘贴 `npx @scope/pkg --token=…` 会打开一个空
 * 弹层并吞掉 Enter（MUL-5429）。
 *
 * 这是上游已知且未修的设计：ueberdosis/tiptap#4183（「当前用户没敲触发词
 * Suggestion 也会打开」，2023 年起挂着，标记 `complexity: hard`）与 #7371
 * （「Suggestion 只应在用户显式敲出触发字符时开始」）。上游的答复是暴露
 * `shouldShow` 让应用自己补上缺失的来历信息——但 `shouldShow` 拿到的事务里
 * 没有 `prev.active`，而 `allow` 拿到 `prev.active` 却没有事务，所以任何
 * 检查事务的谓词都无法表达「只在打开它的那个事务上」。「来历」只能来自
 * 事务流之外。
 *
 * ## 怎么工作
 *
 * `handleTextInput` 是唯一一个只对真实键盘与 IME 输入触发的 ProseMirror
 * 钩子——粘贴走 `handlePaste`/`doPaste`，拖放走 `handleDrop`，两者都到不了
 * 它。Tiptap 自己的 InputRules 跑的就是这个信号，所以敲 `- ` 会生成列表、
 * 而粘贴不会。Suggestion 是唯一一个不用它的 Tiptap 功能；本插件把它补上。
 *
 * 敲下触发字符后「武装」其文档位置。弹层的 `shouldShow` 于是只对锚定在
 * 已武装位置上的匹配打开。凡用户没敲过的东西永远不会被武装，也就永远
 * 不会打开弹层。
 */

/** 本编辑器里所有 suggestion 监听的触发字符。 */
const TRIGGER_CHARS = new Set(["@", "/"]);

/**
 * 每个编辑器最新的武装位置。
 *
 * `shouldShow` 在 Suggestion 插件的 `apply` 内运行，那里的 `editor.state`
 * 仍是**上一个**状态——所以被应用事务对应的武装位置还没法通过 PluginKey
 * 读到。这张表作为镜像，由武装插件的 `apply` 同步维护。扩展优先级（见下）
 * 让该 `apply` 排在所有 suggestion 插件之前，因此 `shouldShow` 读它时
 * 镜像永远是最新的。
 */
const armedPositions = new WeakMap<Editor, number | null>();

/** `from` 是否就是用户敲下触发字符的位置。 */
export function isTriggerArmedAt(editor: Editor, from: number): boolean {
  const armed = armedPositions.get(editor);
  return armed !== null && armed !== undefined && armed === from;
}

/** 敲入文本中最后一个触发字符的偏移，没有则返回 -1。 */
function lastTriggerIndex(text: string): number {
  for (let i = text.length - 1; i >= 0; i--) {
    if (TRIGGER_CHARS.has(text[i]!)) return i;
  }
  return -1;
}

/** 武装位置是否仍持有它的触发字符。 */
function stillHoldsTrigger(doc: ProseMirrorNode, pos: number): boolean {
  if (pos < 0 || pos + 1 > doc.content.size) return false;
  return TRIGGER_CHARS.has(doc.textBetween(pos, pos + 1));
}

export const SuggestionTriggerArmingExtension = Extension.create({
  name: "suggestionTriggerArming",
  // 排到所有 suggestion 插件前面，保证 `handleTextInput`（必须在输入规则
  // 消费按键之前看到击键）与 `apply`（必须在 `shouldShow` 读镜像前刷新它）
  // 都最先运行。
  priority: 1000,

  addProseMirrorPlugins() {
    const editor = this.editor;
    // 由 `handleTextInput` 在 ProseMirror 派发插入前一刻设置，被紧接着的
    // 下一次 `apply` 消费。保存的是**新**文档里的位置：在 `from` 敲入文本，
    // 第 i 个字符落在 `from + i`。
    let pendingArm: number | null = null;

    return [
      new Plugin<number | null>({
        key: new PluginKey("suggestionTriggerArming"),

        state: {
          init() {
            armedPositions.set(editor, null);
            return null;
          },

          apply(tr, prev, _oldState, newState) {
            const next = ((): number | null => {
              // 刚敲下一个触发字符——无条件武装它。
              if (pendingArm !== null) return pendingArm;
              if (prev === null) return null;

              // 一次有意的光标移动（点击、方向键）放弃触发词。
              // 打字从不显式设置选区，所以它不会被误判。
              // IME 除外：组合输入中途会派发自己的选区事务。
              if (tr.selectionSet && !tr.docChanged && !editor.view.composing) {
                return null;
              }

              const mapped = tr.docChanged ? tr.mapping.map(prev, -1) : prev;
              if (!stillHoldsTrigger(newState.doc, mapped)) return null;
              // 光标已不再位于同一查询串内触发词之后。
              if (!newState.selection.empty || newState.selection.from <= mapped) {
                return null;
              }
              return mapped;
            })();

            pendingArm = null;
            armedPositions.set(editor, next);
            return next;
          },
        },

        props: {
          handleTextInput(_view, from, _to, text) {
            const index = lastTriggerIndex(text);
            if (index !== -1) pendingArm = from + index;
            // 绝不拦截输入——其它所有插件都必须还能看到它。
            return false;
          },
        },
      }),
    ];
  },
});

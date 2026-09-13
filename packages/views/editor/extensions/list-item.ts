/**
 * 列表项行为补丁——修正 Tiptap/ProseMirror 内置 ListItem/TaskItem 的键盘键位
 * 缺陷：
 *   - 「空顶层列表项上连按两次 Enter 退出列表」：原生 splitListItem 在空项上
 *     返回 false 却无人接管（见 listItemKeymap）；
 *   - 「整列表选中后按 Tab 整体缩进」：原生 sinkListItem 遇第一项在 index 0
 *     就放弃（见 sinkListItemRange）；
 *   - TaskItem 补充 GitHub 风格 `- [ ]` / `- [x]` 输入规则。
 * 导出 PatchedListItem / PatchedTaskItem 两个补丁扩展，供装配层替换默认列表项。
 */
import { type Editor, InputRule } from "@tiptap/core";
import { ListItem, TaskItem } from "@tiptap/extension-list";
import { sinkListItem as pmSinkListItem } from "@tiptap/pm/schema-list";
import { type Command, TextSelection } from "@tiptap/pm/state";
import type { NodeType } from "@tiptap/pm/model";

/**
 * Tab 缩进版本，还支持「多选列表项且第一项恰好是其（子）列表的第一个
 * 子节点」的情况。
 *
 * 原生 `sinkListItem`（prosemirror-schema-list）只要 `range.startIndex === 0`
 * 就会放弃——不 dispatch、直接返回 false，因为第一项前面没有可用来嵌套的
 * 兄弟节点。这对「光标折叠在第一项里」是正确的；但它也杀掉了「从顶部
 * 选中整个列表再按 Tab」这个自然手势：命令看到第一项在 index 0 就什么都不
 * 做（MUL-3697）。
 *
 * 在嵌套列表模型里结构上正确的行为（与 Notion / GitHub 一致）是：把第一
 * 个选中项当作锚，其余项全部下沉到它下面。做法是对收窄到「从第二个选中项
 * 内部开始」的选区重跑*原生*命令（这样它的 `startIndex` 变成 1），同时保留
 * 原来的 `$to`。收窄后的选区是在派生 state 上算出来的、不会单独 dispatch，
 * 所以整个操作是一次 dispatch / 一个撤销步。
 *
 * Shift-Tab / `liftListItem` 没有同类限制（它本来就正确处理区间和第一项），
 * 所以只需要给 Tab 包这一层。
 */
function sinkListItemRange(itemType: NodeType): Command {
  return (state, dispatch) => {
    // 常规路径——光标或选区不在第一项开头。这也覆盖了「光标折叠在第一项
    // 里」的真 no-op：原生返回 false，下面兜底的各守卫同样返回 false。
    if (pmSinkListItem(itemType)(state, dispatch)) return true;

    const { $from, $to } = state.selection;
    const range = $from.blockRange(
      $to,
      (node) => node.childCount > 0 && node.firstChild?.type === itemType,
    );
    // 兜底不适用时干净地返回 false（不 dispatch）：没有列表区间、区间没有
    // 从第一项开始、选中的项不足两个，或项类型不匹配（C2）。
    if (!range) return false;
    if (range.startIndex !== 0) return false;
    if (range.endIndex - range.startIndex < 2) return false;
    if (range.parent.child(range.startIndex).type !== itemType) return false;

    // 把 $from 移进第二个选中项，$to 保持在最后一个选中项（C1——不要折到
    // 第二个项上）。+2 步越过 <listItem> + <paragraph> 两个开始标记进入行内
    // 内容；`between` 在该项不以段落开头时会吸附到合法文本位置。
    const secondItemStart =
      range.start + range.parent.child(range.startIndex).nodeSize + 2;
    const narrowed = state.apply(
      state.tr.setSelection(
        TextSelection.between(state.doc.resolve(secondItemStart), $to),
      ),
    );
    return pmSinkListItem(itemType)(narrowed, dispatch);
  };
}

/**
 * 共用的列表键位，带正确的「连按两次 Enter 退出列表」行为。
 *
 * Tiptap 原生的 `Enter: splitListItem` 不完整。当光标位于空的**顶层**列表项
 * 时，`splitListItem` 自己返回 false（不 dispatch），代码注释写着「放弃并把
 * 提升交给下一条命令处理」——但原生键位里根本没有下一条命令。于是空项上
 * 的 Enter 就落进 ProseMirror 的 baseKeymap（`splitBlock`），只会在列表项
 * 里再插入一个空段落，把用户困在里面。
 *
 * 修复：用 `commands.first` 串联 `splitListItem` → `liftListItem`。提升兜底
 * 只在 `splitListItem` 返回 false（顶层空项）时执行，对应编辑器通用行为：
 * 空圆点项上再按一次 Enter 就以普通段落退出列表。非空项和嵌套空项不受
 * 影响，因为 `splitListItem` 能正确处理它们并返回 true。
 *
 * Tab 缩进列表项——多项且含第一项的情形见 `sinkListItemRange`。
 * Shift-Tab 用原生命令取消缩进。
 *
 * 只要光标在列表项里，Tab 就属于列表的缩进控制，即使结构上无事可做
 * （首个子项没有可嵌套对象，或已到最大深度）也必须吞掉。否则未被处理的
 * Tab 会落到浏览器手里，把焦点移出编辑器跑到下一个控件。因此返回值跟踪
 * 的是「光标是否在这个列表里」（`editor.isActive(name)`），而不是「缩进
 * 有没有移动东西」：尽力缩进，随后在列表内吞掉按键、在列表外放行（焦点
 * 导航）。
 */
function listItemKeymap(editor: Editor, name: string) {
  return {
    Enter: () =>
      editor.commands.first(({ commands }) => [
        () => commands.splitListItem(name),
        () => commands.liftListItem(name),
      ]),
    Tab: () => {
      const itemType = editor.schema.nodes[name];
      if (!itemType) return false;
      sinkListItemRange(itemType)(editor.state, (tr) =>
        editor.view.dispatch(tr),
      );
      return editor.isActive(name);
    },
    "Shift-Tab": () => editor.commands.liftListItem(name),
  };
}

/**
 * PatchedListItem——对原生 ListItem 补上上面的键盘键位
 * （含「连按两次 Enter 退出列表」修复）。
 */
export const PatchedListItem = ListItem.extend({
  addKeyboardShortcuts() {
    return listItemKeymap(this.editor, this.name);
  },
});

/**
 * PatchedTaskItem——与 PatchedListItem 相同的「连按两次 Enter 退出列表」
 * 修复，套用到勾选式任务项，让它们与圆点/数字列表行为一致。
 * `nested: true` 让任务项能容纳嵌套列表（这样 Tab 能缩进出子任务、嵌套
 * markdown 能正常往返），与 GitHub / Notion 一致。
 *
 * 它还补上了 GitHub 风格的 `- [ ] ` 输入流程。TaskItem 自带输入规则只把
 * 「纯段落开头」的 `[ ] ` / `[x] ` 转成任务项；一旦行首的 `- `（或 `1. `）
 * 已把该行变成圆点/数字项，那条规则就不再触发，`[ ]` 会一直以字面文本
 * 存在。下面这条额外规则专门接住「复选框 token 恰好是刚输入列表项的全部
 * 内容」的情形，只把那一项转成任务项——同列表里的兄弟项不动（先提升再
 * 重包）。
 */
export const PatchedTaskItem = TaskItem.extend({
  addKeyboardShortcuts() {
    return listItemKeymap(this.editor, this.name);
  },
  addInputRules() {
    return [
      ...(this.parent?.() ?? []),
      new InputRule({
        find: /^\[([ xX])?\]\s$/,
        handler: ({ state, range, match, chain }) => {
          // 仅当复选框 token 是列表项的全部内容时才转换。纯段落里的输入
          // 由上面继承的规则处理；锚定正则保证了它前面没有别的文本。
          // 守卫不满足时 handler 不动事务，规则即 no-op，ProseMirror 自然
          // 放行。
          if (state.selection.$from.node(-1)?.type.name === "listItem") {
            const checked = (match[1] ?? "").toLowerCase() === "x";
            chain()
              .deleteRange(range)
              .liftListItem("listItem")
              .toggleList("taskList", "taskItem")
              .updateAttributes("taskItem", { checked })
              .run();
          }
        },
      }),
    ];
  },
}).configure({ nested: true });

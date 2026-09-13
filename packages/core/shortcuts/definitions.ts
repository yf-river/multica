// 本文件定义快捷键动作目录、和弦表示与校验：哪些动作有默认快捷键、如何把
// 键盘事件转成结构化和弦、如何判定保留组合（浏览器/操作系统独占的加速键）等。

import {
  getShortcutPlatform,
  type ShortcutPlatform,
} from "./platform";

/** 全部可配置的快捷键动作 id。 */
export type ShortcutActionId =
  | "openSearch"
  | "createIssue"
  | "toggleSidebar"
  | "toggleRightSidebar"
  | "toggleChat"
  | "findInIssue"
  | "openThreadNav"
  | "archiveInboxItem"
  | "send"
  | "goBack"
  | "goForward"
  | "goInbox"
  | "goChat"
  | "goMyIssues"
  | "goIssues"
  | "goProjects"
  | "goAutopilots"
  | "goAgents"
  | "goSquads"
  | "goUsage"
  | "goRuntimes"
  | "goSkills"
  | "goSettings";

/** 快捷键的分类：通用（general）/ 导航（navigation）。 */
export type ShortcutCategory = "general" | "navigation";

/** 一个快捷键需要按住哪些修饰键。 */
export interface ShortcutModifiers {
  /** macOS 上指 Command，Windows/Linux 上指 Control。 */
  primary: boolean;
  /** 字面上的 Control，在 macOS 上区别于 Command。 */
  control: boolean;
  /** 字面上的 Meta/Windows/Super，在 Windows/Linux 上区别于 Control。 */
  meta: boolean;
  alt: boolean;
  shift: boolean;
}

/** 一个按键和弦：逻辑键 + 修饰键。 */
export interface ShortcutChord {
  /** 逻辑键（`KeyboardEvent.key`），不是物理键盘位置。 */
  key: string;
  modifiers: ShortcutModifiers;
}

/** 一个快捷键动作的定义。 */
export interface ShortcutActionDefinition {
  id: ShortcutActionId;
  category: ShortcutCategory;
  defaultShortcut: ShortcutChord | null;
  /** 焦点在编辑器/控件里时，全局处理器是否仍允许触发该动作。 */
  allowInEditable: boolean;
}

/** 构造一个和弦，未指定的修饰键默认不按。 */
export function createShortcutChord(
  key: string,
  modifiers: Partial<ShortcutModifiers> = {},
): ShortcutChord {
  return {
    key,
    modifiers: {
      primary: false,
      control: false,
      meta: false,
      alt: false,
      shift: false,
      ...modifiers,
    },
  };
}

/** 便捷函数：构造一个「只按 primary」的和弦。 */
const primary = (key: string) =>
  createShortcutChord(key, { primary: true });

/** 快捷键动作目录：全部动作及其默认和弦。 */
export const SHORTCUT_ACTIONS: readonly ShortcutActionDefinition[] = [
  { id: "openSearch", category: "general", defaultShortcut: primary("K"), allowInEditable: true },
  { id: "createIssue", category: "general", defaultShortcut: createShortcutChord("C"), allowInEditable: false },
  { id: "toggleSidebar", category: "general", defaultShortcut: primary("B"), allowInEditable: false },
  {
    id: "toggleRightSidebar",
    category: "general",
    defaultShortcut: primary("/"),
    allowInEditable: false,
  },
  // Mod+J 遵循「切换停靠面板」的惯例，是这个模块自己的策略在每类平台和运行时
  // 上留空的少数几个字母之一：它既非应用独占（PRIMARY_RESERVED_KEYS）也非
  // 浏览器独占（BROWSER_ONLY_PRIMARY_RESERVED_KEYS）。`allowInEditable` 是因为
  // 这个绑定的意义是——不碰鼠标就能打开（和关掉）聊天，光标停在聊天输入框里
  // 时它必须仍然生效。
  { id: "toggleChat", category: "general", defaultShortcut: primary("J"), allowInEditable: true },
  { id: "findInIssue", category: "general", defaultShortcut: primary("F"), allowInEditable: true },
  // Mod+Shift+O 对应代码编辑器里「跳到文件中的符号」：跳到你已经打开的文档的
  // 某个区块，区别于搜索文本的 Mod+F。`allowInEditable` 是因为写评论草稿时
  // 跳到另一个讨论线程是常见场景，而不是例外。
  {
    id: "openThreadNav",
    category: "general",
    defaultShortcut: createShortcutChord("O", { primary: true, shift: true }),
    allowInEditable: true,
  },
  {
    id: "archiveInboxItem",
    category: "general",
    defaultShortcut: createShortcutChord("E"),
    allowInEditable: false,
  },
  { id: "send", category: "general", defaultShortcut: primary("Enter"), allowInEditable: true },
  // 浏览器风格的历史导航（Mod+[ / Mod+]）。两个方括号键都非应用独占
  // （PRIMARY_RESERVED_KEYS）也非浏览器独占（BROWSER_ONLY_PRIMARY_RESERVED_
  // KEYS），所以在每类平台上都能录制。`allowInEditable` 为 false，保证光标停在
  // input、textarea 或编辑器里时，这个和弦不会把页面切走。
  { id: "goBack", category: "navigation", defaultShortcut: primary("["), allowInEditable: false },
  { id: "goForward", category: "navigation", defaultShortcut: primary("]"), allowInEditable: false },
  { id: "goInbox", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goChat", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goMyIssues", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goIssues", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goProjects", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goAutopilots", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goAgents", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goSquads", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goUsage", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goRuntimes", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goSkills", category: "navigation", defaultShortcut: null, allowInEditable: false },
  { id: "goSettings", category: "navigation", defaultShortcut: null, allowInEditable: false },
] as const;

/** action id → 定义的查找表。 */
export const SHORTCUT_ACTION_BY_ID = Object.fromEntries(
  SHORTCUT_ACTIONS.map((action) => [action.id, action]),
) as Record<ShortcutActionId, ShortcutActionDefinition>;

/** 纯修饰键集合——单独按下它们不构成可执行动作。 */
const MODIFIER_KEYS = new Set([
  "Alt", "AltGraph", "CapsLock", "Control", "Fn", "FnLock", "Hyper",
  "Meta", "NumLock", "ScrollLock", "Shift", "Super", "Symbol", "SymbolLock",
]);
/** 不可执行的键。 */
const NON_ACTIONABLE_KEYS = new Set(["Dead", "Process", "Unidentified"]);
/** 展示用键名映射（把 `KeyboardEvent.key` 归一成简洁标签）。 */
const KEY_LABELS: Record<string, string> = {
  " ": "Space",
  ArrowUp: "Up",
  ArrowDown: "Down",
  ArrowLeft: "Left",
  ArrowRight: "Right",
  Esc: "Escape",
  "+": "Plus",
  "-": "Minus",
  "=": "Equals",
  "_": "Underscore",
};

/** 从键盘事件里取出「动作键」；纯修饰键或不可执行键返回 null。 */
function eventKey(event: KeyboardEvent): string | null {
  // 合成事件（例如 Chrome 自动填充）可能完全没有 `key`。
  if (typeof event.key !== "string") return null;
  if (MODIFIER_KEYS.has(event.key) || NON_ACTIONABLE_KEYS.has(event.key)) return null;
  const key = KEY_LABELS[event.key] ?? event.key;
  if (key.length === 1 && /[a-z]/i.test(key)) return key.toUpperCase();
  return key;
}

/** 这个和弦是否真的可执行（有非修饰键）。 */
export function isShortcutChordActionable(shortcut: ShortcutChord): boolean {
  return shortcut.key.length > 0 &&
    !MODIFIER_KEYS.has(shortcut.key) &&
    !NON_ACTIONABLE_KEYS.has(shortcut.key);
}

/** 把键盘事件转成平台感知的结构化和弦。 */
export function shortcutFromEvent(
  event: KeyboardEvent,
  platform: ShortcutPlatform = getShortcutPlatform(),
): ShortcutChord | null {
  if (event.getModifierState?.("AltGraph")) return null;
  const key = eventKey(event);
  if (!key) return null;
  const mac = platform === "macos";
  return createShortcutChord(key, {
    primary: mac ? event.metaKey : event.ctrlKey,
    control: mac ? event.ctrlKey : false,
    meta: mac ? false : event.metaKey,
    alt: event.altKey,
    shift: event.shiftKey,
  });
}

/** 两个和弦是否完全相等（含 null 相等）。 */
export function shortcutChordEquals(
  left: ShortcutChord | null,
  right: ShortcutChord | null,
): boolean {
  if (left === null || right === null) return left === right;
  return (
    left.key === right.key &&
    left.modifiers.primary === right.modifiers.primary &&
    left.modifiers.control === right.modifiers.control &&
    left.modifiers.meta === right.modifiers.meta &&
    left.modifiers.alt === right.modifiers.alt &&
    left.modifiers.shift === right.modifiers.shift
  );
}

/** 精确匹配：事件里多按了任何修饰键都刻意视为不匹配。 */
export function shortcutMatchesEvent(
  shortcut: ShortcutChord | null,
  event: KeyboardEvent,
  platform: ShortcutPlatform = getShortcutPlatform(),
): boolean {
  // `null` 在这个边界有两种含义：一个没分配快捷键的动作，或一个不含动作键的
  // 事件（例如只按 Command/Control/Shift 本身）。两者都不得触发动作——虽然
  // 比较持久化值时 `shortcutChordEquals(null, null)` 在别处是有用的。
  if (!shortcut) return false;
  const eventShortcut = shortcutFromEvent(event, platform);
  return eventShortcut !== null && shortcutChordEquals(shortcut, eventShortcut);
}

/** 这个和弦是否就是「不加任何修饰键地按某个键」。 */
export function isPlainShortcut(
  shortcut: ShortcutChord | null,
  key: string,
): boolean {
  return shortcutChordEquals(shortcut, createShortcutChord(key));
}

/** 把和弦格式化成人类可读的键位文案（按平台）。 */
export function formatShortcut(
  shortcut: ShortcutChord | null,
  platform: ShortcutPlatform = getShortcutPlatform(),
): string {
  if (!shortcut) return "—";
  const { modifiers } = shortcut;
  const keyLabels: Record<string, string> = {
    Enter: platform === "macos" ? "↵" : "Enter",
    Backspace: platform === "macos" ? "⌫" : "Backspace",
    Delete: platform === "macos" ? "⌦" : "Delete",
    Escape: "Esc",
    Up: "↑",
    Down: "↓",
    Left: "←",
    Right: "→",
    Plus: "+",
    Minus: platform === "macos" ? "−" : "-",
    Equals: "=",
    Underscore: "_",
    Space: "Space",
  };
  const key = keyLabels[shortcut.key] ?? shortcut.key;

  if (platform === "macos") {
    return [
      modifiers.primary ? "⌘" : "",
      modifiers.control ? "⌃" : "",
      modifiers.alt ? "⌥" : "",
      modifiers.shift ? "⇧" : "",
      modifiers.meta ? "Meta" : "",
      key,
    ].join("");
  }

  return [
    modifiers.primary ? "Ctrl" : null,
    modifiers.control ? "Control" : null,
    modifiers.meta ? (platform === "windows" ? "Win" : "Super") : null,
    modifiers.alt ? "Alt" : null,
    modifiers.shift ? "Shift" : null,
    key,
  ].filter(Boolean).join("+");
}

/** 目标元素是否处于「可编辑」上下文（输入框/文本域/可编辑元素）。 */
export function isEditableShortcutTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.isContentEditable ||
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.tagName === "SELECT" ||
    target.closest("[contenteditable='true']") !== null
  );
}

const PORTAL_LAYER_SELECTOR =
  '[role="menu"], [role="dialog"], [role="alertdialog"], [role="listbox"]';

/**
 * 是否有打开的浮层（菜单、对话框、listbox）占有着键盘。
 *
 * 浮层被 portaled 到 body，所以页面级监听器仍会看到它们的按键；即便焦点从没
 * 离开页面，`data-base-ui-inert` 标记也能抓住模态层。
 */
export function isPortalLayerShortcutTarget(target: EventTarget | null): boolean {
  if (typeof document === "undefined") return false;
  if (document.querySelector("[data-base-ui-inert]") !== null) return true;
  return target instanceof Element && target.closest(PORTAL_LAYER_SELECTOR) !== null;
}

const PRIMARY_RESERVED_KEYS = new Set([
  // 浏览器与应用窗口操作不是可靠的页面动作。
  "W", "R", "Q",
  // 浏览器独占了它的设置快捷键。
  ",",
  // 基础编辑操作绝不该变成产品动作。
  "A", "C", "V", "X", "Y", "Z",
  // 浏览器缩放的加速键。
  "Equals", "Plus", "Minus", "Underscore", "0",
]);

// 浏览器 UI 围绕「标签页」独占的加速键：打印、地址栏、新建标签/窗口、书签与
// 查看源码。网页无法可靠独占它们。带额外修饰符的变体同样保留，因为好几个
// 属于操作系统或窗口管理器。
const BROWSER_ONLY_PRIMARY_RESERVED_KEYS = new Set([
  "P", "L", "T", "N", "D", "U",
]);

/** 浏览器/窗口/操作系统独占、应用无法可靠拥有的加速键。 */
export function isReservedShortcut(
  shortcut: ShortcutChord,
  platform: ShortcutPlatform = getShortcutPlatform(),
): boolean {
  const { modifiers, key } = shortcut;
  if (key === "F5") return true;
  if (modifiers.primary && PRIMARY_RESERVED_KEYS.has(key)) return true;
  if (modifiers.primary && BROWSER_ONLY_PRIMARY_RESERVED_KEYS.has(key)) {
    return true;
  }

  if (platform === "macos") {
    if (modifiers.primary && (key === "Space" || key === "Tab" || key === "M" || key === "H")) return true;
    if (modifiers.control && ["Up", "Down", "Left", "Right"].includes(key)) return true;
  } else {
    // Windows/Super 快捷键归外壳/窗口管理器所有，往往根本到不了浏览器。
    // 全部拒绝，而不是假装录下来的绑定会可靠生效。
    if (modifiers.meta) return true;
    if (modifiers.alt && (key === "Tab" || key === "F4")) return true;
  }

  return false;
}

const PLAIN_GLOBAL_RESERVED_KEYS = new Set([
  "Enter", "Space", "Tab", "Escape", "Backspace", "Delete",
  "Up", "Down", "Left", "Right", "Home", "End", "PageUp", "PageDown",
]);

/** 和弦是否带任何修饰键。 */
function hasShortcutModifier(shortcut: ShortcutChord): boolean {
  const { modifiers } = shortcut;
  return modifiers.primary || modifiers.control || modifiers.meta ||
    modifiers.alt || modifiers.shift;
}

/** 和弦是否带「命令级」修饰键（primary / control / meta）。 */
function hasCommandModifier(shortcut: ShortcutChord): boolean {
  const { modifiers } = shortcut;
  return modifiers.primary || modifiers.control || modifiers.meta;
}

/**
 * 叠加在 OS/浏览器保留键之上的「产品级」安全策略。
 *
 * 裸字母键对非可编辑导航是有用的（例如 `C`），但允许在编辑器里触发的动作，
 * 绝不能在使用者打字时被触发。Send 是一个刻意的例外：裸 Enter 被显式支持。
 */
export function isShortcutAllowedForAction(
  actionId: ShortcutActionId,
  shortcut: ShortcutChord,
  platform: ShortcutPlatform = getShortcutPlatform(),
): boolean {
  if (!isShortcutChordActionable(shortcut)) return false;
  if (isReservedShortcut(shortcut, platform)) return false;
  // Tab 在所有受支持平台上都是焦点导航（Ctrl+Tab 是浏览器标签页导航）。它
  // 永远不会是可靠的产品级绑定。
  if (shortcut.key === "Tab") return false;
  const hasModifier = hasShortcutModifier(shortcut);
  const hasCommand = hasCommandModifier(shortcut);

  if (actionId === "send") {
    return shortcutChordEquals(shortcut, createShortcutChord("Enter")) ||
      shortcutChordEquals(
        shortcut,
        createShortcutChord("Enter", { primary: true }),
      );
  }
  if (SHORTCUT_ACTION_BY_ID[actionId].allowInEditable) {
    return hasCommand;
  }
  return hasModifier || !PLAIN_GLOBAL_RESERVED_KEYS.has(shortcut.key);
}

/** 一次性解析 v1 时代、结构化和弦出现之前持久化的字符串偏好。 */
export function parseLegacyShortcut(value: string): ShortcutChord | null {
  if (!value) return null;
  const parts = value.split("+");
  const key = parts.pop();
  if (!key) return null;
  const knownModifiers = new Set(["Mod", "Ctrl", "Meta", "Alt", "Shift"]);
  if (parts.some((part) => !knownModifiers.has(part))) return null;
  return createShortcutChord(key, {
    primary: parts.includes("Mod"),
    control: parts.includes("Ctrl"),
    meta: parts.includes("Meta"),
    alt: parts.includes("Alt"),
    shift: parts.includes("Shift"),
  });
}

// 本文件实现快捷键的「用户覆盖」store：用户可把默认快捷键改成自己的组合
// （或禁用），这里负责持久化、清洗、冲突检测与按 action 解析最终快捷键。

"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import {
  SHORTCUT_ACTIONS,
  SHORTCUT_ACTION_BY_ID,
  isShortcutAllowedForAction,
  parseLegacyShortcut,
  shortcutChordEquals,
  type ShortcutActionId,
  type ShortcutChord,
} from "./definitions";

/** 用户覆盖：action id → 快捷键（null 表示禁用该 action）。 */
export type ShortcutOverrides = Partial<Record<ShortcutActionId, ShortcutChord | null>>;

const SHORTCUT_ACTION_IDS = new Set<string>(
  SHORTCUT_ACTIONS.map((action) => action.id),
);

/** 是否为普通对象。 */
function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/**
 * 清洗单个快捷键值：null 放行（禁用）；结构不合法返回 undefined（丢弃）；
 * 合法则返回带完整修饰符布尔值的对象。
 */
function sanitizeShortcutChord(value: unknown): ShortcutChord | null | undefined {
  if (value === null) return null;
  if (!isRecord(value) || typeof value.key !== "string" || value.key.length === 0) {
    return undefined;
  }
  const modifiers = value.modifiers;
  if (!isRecord(modifiers)) return undefined;
  const { primary, control, meta, alt, shift } = modifiers;
  if (
    typeof primary !== "boolean" ||
    typeof control !== "boolean" ||
    typeof meta !== "boolean" ||
    typeof alt !== "boolean" ||
    typeof shift !== "boolean"
  ) {
    return undefined;
  }
  return {
    key: value.key,
    modifiers: {
      primary,
      control,
      meta,
      alt,
      shift,
    },
  };
}

/** 丢弃未知 action 与畸形和弦，别让存储内容弄崩匹配/UI。 */
export function sanitizeShortcutOverrides(value: unknown): ShortcutOverrides {
  if (!isRecord(value)) return {};
  const overrides: ShortcutOverrides = {};
  for (const [actionId, rawShortcut] of Object.entries(value)) {
    if (!SHORTCUT_ACTION_IDS.has(actionId)) continue;
    const shortcut = sanitizeShortcutChord(rawShortcut);
    if (
      shortcut !== undefined &&
      (shortcut === null || isShortcutAllowedForAction(
        actionId as ShortcutActionId,
        shortcut,
      ))
    ) {
      overrides[actionId as ShortcutActionId] = shortcut;
    }
  }
  return overrides;
}

/** store 内部状态。 */
interface ShortcutState {
  overrides: ShortcutOverrides;
  setShortcut: (actionId: ShortcutActionId, shortcut: ShortcutChord | null) => void;
  resetShortcut: (actionId: ShortcutActionId) => void;
  resetAll: () => void;
}

/** 持久化版本的迁移：v1 的字符串偏好迁到 v2 的结构化和弦。 */
export function migrateShortcutState(
  persisted: unknown,
  version: number,
): { overrides: ShortcutOverrides } {
  if (version >= 2) {
    const current = isRecord(persisted) ? persisted : undefined;
    return { overrides: sanitizeShortcutOverrides(current?.overrides) };
  }
  const previous = isRecord(persisted) && isRecord(persisted.overrides)
    ? persisted.overrides
    : {};
  const overrides: ShortcutOverrides = {};
  for (const [actionId, value] of Object.entries(previous)) {
    if (!SHORTCUT_ACTION_IDS.has(actionId)) continue;
    if (value === null) {
      overrides[actionId as ShortcutActionId] = null;
      continue;
    }
    if (typeof value !== "string") continue;
    const shortcut = parseLegacyShortcut(value);
    // 无效的旧数据意味着「用默认」，而不是「禁用该动作」。
    if (shortcut) overrides[actionId as ShortcutActionId] = shortcut;
  }
  return { overrides: sanitizeShortcutOverrides(overrides) };
}

/** 快捷键覆盖 store：只持久化与默认不同的覆盖项。 */
export const useShortcutStore = create<ShortcutState>()(
  persist(
    (set) => ({
      overrides: {},
      setShortcut: (actionId, shortcut) =>
        set((state) => {
          if (shortcut && !isShortcutAllowedForAction(actionId, shortcut)) {
            return state;
          }
          const next = { ...state.overrides };
          if (shortcutChordEquals(shortcut, SHORTCUT_ACTION_BY_ID[actionId].defaultShortcut)) {
            delete next[actionId];
          } else {
            next[actionId] = shortcut;
          }
          return { overrides: next };
        }),
      resetShortcut: (actionId) =>
        set((state) => {
          const next = { ...state.overrides };
          delete next[actionId];
          return { overrides: next };
        }),
      resetAll: () => set({ overrides: {} }),
    }),
    {
      name: "multica_keyboard_shortcuts",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ overrides: state.overrides }),
      version: 2,
      migrate: migrateShortcutState,
      // Zustand 只在版本不同时才调 `migrate`。每次水合也要清洗一遍，让损坏的
      // 当前版本存储也能安全读取。
      merge: (persisted, current) => {
        const stored = isRecord(persisted) ? persisted : undefined;
        return {
          ...current,
          overrides: sanitizeShortcutOverrides(stored?.overrides),
        };
      },
    },
  ),
);

/**
 * 解析某个 action 最终生效的快捷键：有覆盖用覆盖（null 即禁用），否则用默认。
 */
export function resolveShortcut(
  overrides: ShortcutOverrides,
  actionId: ShortcutActionId,
): ShortcutChord | null {
  return Object.prototype.hasOwnProperty.call(overrides, actionId)
    ? overrides[actionId] ?? null
    : SHORTCUT_ACTION_BY_ID[actionId].defaultShortcut;
}

/** React 侧读取某个 action 的快捷键。 */
export function useShortcut(actionId: ShortcutActionId): ShortcutChord | null {
  return useShortcutStore((state) => resolveShortcut(state.overrides, actionId));
}

/** 非 React 侧读取某个 action 的快捷键。 */
export function getShortcut(actionId: ShortcutActionId): ShortcutChord | null {
  return resolveShortcut(useShortcutStore.getState().overrides, actionId);
}

/** 找与目标快捷键撞车的另一个 action（允许在设置里警告冲突）。 */
export function findShortcutConflict(
  actionId: ShortcutActionId,
  shortcut: ShortcutChord,
): ShortcutActionId | null {
  for (const action of SHORTCUT_ACTIONS) {
    if (
      action.id !== actionId &&
      shortcutChordEquals(getShortcut(action.id), shortcut)
    ) {
      return action.id;
    }
  }
  return null;
}

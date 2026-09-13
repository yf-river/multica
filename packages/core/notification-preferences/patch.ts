// 本文件提供「通知偏好」补丁的纯函数：从新旧两份偏好里推导最小原子补丁、
// 把补丁应用回完整偏好、以及只回滚仍属于本次乐观修改的那部分。

import type {
  NotificationGroupKey,
  NotificationGroupValue,
  NotificationPreferences,
} from "../types";

/** 全部可配置的通知分组 key。 */
const NOTIFICATION_GROUP_KEYS: readonly NotificationGroupKey[] = [
  "assignments",
  "status_changes",
  "comments",
  "mentions",
  "updates",
  "agent_activity",
  "system_notifications",
];

/** 读取某分组的偏好值；没写就视为默认值 "all"。 */
function preferenceValue(
  preferences: NotificationPreferences,
  key: NotificationGroupKey,
): NotificationGroupValue {
  return preferences[key] ?? "all";
}

/**
 * 把设置界面产出的完整偏好对象，转成最小的原子补丁。
 * 缺失的 key 表示默认值（"all"），所以只有「和默认不同」的项才会进补丁。
 *
 * @param previous - 改动前的完整偏好
 * @param next - 改动后的完整偏好
 * @returns 只含发生变化的 key 的补丁对象
 */
export function deriveNotificationPreferencePatch(
  previous: NotificationPreferences,
  next: NotificationPreferences,
): NotificationPreferences {
  const patch: NotificationPreferences = {};

  for (const key of NOTIFICATION_GROUP_KEYS) {
    const previousValue = preferenceValue(previous, key);
    const nextValue = preferenceValue(next, key);
    if (previousValue !== nextValue) {
      patch[key] = nextValue;
    }
  }

  return patch;
}

/**
 * 把一份补丁应用到当前完整偏好上，同时保持「值为 all」的键稀疏（即删除键，
 * 而不是显式写 "all"）。
 *
 * @param current - 当前的完整偏好
 * @param patch - 要应用的补丁
 * @returns 应用后的完整偏好
 */
export function applyNotificationPreferencePatch(
  current: NotificationPreferences,
  patch: NotificationPreferences,
): NotificationPreferences {
  const next = { ...current };

  for (const key of NOTIFICATION_GROUP_KEYS) {
    const value = patch[key];
    if (value === "muted") {
      next[key] = value;
    } else if (value === "all") {
      delete next[key];
    }
  }

  return next;
}

/**
 * 只回滚「仍然与本变更的乐观补丁一致」的值。
 * 之后另一个开关把同一个 key 又改了的，以那个为准。
 *
 * @param current - 当前（可能已被后续操作改过）的完整偏好
 * @param patch - 本次变更的乐观补丁
 * @param previous - 本次变更前的完整偏好
 * @returns 回滚后的完整偏好
 */
export function rollbackNotificationPreferencePatch(
  current: NotificationPreferences,
  patch: NotificationPreferences,
  previous: NotificationPreferences,
): NotificationPreferences {
  const next = { ...current };

  for (const key of NOTIFICATION_GROUP_KEYS) {
    const patchedValue = patch[key];
    if (
      patchedValue === undefined ||
      preferenceValue(current, key) !== patchedValue
    ) {
      continue;
    }

    const previousValue = preferenceValue(previous, key);
    if (previousValue === "all") {
      delete next[key];
    } else {
      next[key] = previousValue;
    }
  }

  return next;
}

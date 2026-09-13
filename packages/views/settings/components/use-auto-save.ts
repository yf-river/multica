// 本文件提供 useAutoSave 钩子：对设置项做防抖自动保存，并保证请求串行、旧响应不会覆盖新值。

"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { SettingsSaveStatus } from "./settings-layout";

/** useAutoSave 的配置项。 */
interface UseAutoSaveOptions<T> {
  /** 当前正在编辑的值。 */
  value: T;
  /** 已经成功保存到服务端的值，用来判断是否有未保存的改动。 */
  savedValue: T;
  /** 真正执行保存的异步函数。 */
  onSave: (value: T) => Promise<void>;
  /** 保存成功后的回调。 */
  onSuccess?: (value: T) => void;
  /** 保存失败后的回调。 */
  onError?: (error: unknown) => void;
  /** 是否启用自动保存，默认启用。 */
  enabled?: boolean;
  /** 防抖延迟（毫秒），默认 650ms。 */
  delay?: number;
  /** 判断两个值是否相等的比较函数。 */
  isEqual: (left: T, right: T) => boolean;
}

/** useAutoSave 的返回值。 */
interface AutoSaveResult<T> {
  /** 当前保存状态（空闲/保存中/已保存/出错）。 */
  status: SettingsSaveStatus;
  /** 立即用最新值触发一次保存。 */
  flush: () => void;
  /** 立即用指定值触发一次保存（跳过防抖）。 */
  saveNow: (value: T) => void;
}

/**
 * 对「文本量较大」的设置项做防抖自动保存，同时把保存请求串行化。
 * 如果用户在某个保存请求还在进行中时又修改了内容，只会把最新的值排队，
 * 等当前请求结束后再保存它——这样较慢的旧响应永远不会覆盖更新的请求。
 * @param options - 配置项，见 UseAutoSaveOptions。
 * @returns 保存状态以及 flush、saveNow 两个手动触发保存的方法。
 */
export function useAutoSave<T>({
  value,
  savedValue,
  onSave,
  onSuccess,
  onError,
  enabled = true,
  delay = 650,
  isEqual,
}: UseAutoSaveOptions<T>): AutoSaveResult<T> {
  const [status, setStatus] = useState<SettingsSaveStatus>("idle");
  const mountedRef = useRef(true);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const savingRef = useRef(false);
  const queuedRef = useRef<T | null>(null);
  const latestValueRef = useRef(value);
  const persistedRef = useRef(savedValue);
  const observedSavedRef = useRef(savedValue);
  const enabledRef = useRef(enabled);
  const onSaveRef = useRef(onSave);
  const onSuccessRef = useRef(onSuccess);
  const onErrorRef = useRef(onError);
  const isEqualRef = useRef(isEqual);

  // 每次渲染都同步最新的值/配置到 ref，保证异步回调里拿到的始终是最新版本。
  latestValueRef.current = value;
  enabledRef.current = enabled;
  onSaveRef.current = onSave;
  onSuccessRef.current = onSuccess;
  onErrorRef.current = onError;
  isEqualRef.current = isEqual;

  // 当外部传入的「已保存值」发生变化（如被别处刷新），同步更新本地基准值。
  if (!isEqual(savedValue, observedSavedRef.current)) {
    observedSavedRef.current = savedValue;
    persistedRef.current = savedValue;
  }

  const runSave = useCallback(async (next: T) => {
    // 未启用、或值没变就不保存。
    if (!enabledRef.current || isEqualRef.current(next, persistedRef.current)) {
      return;
    }
    // 已有保存请求在进行中：只记录最新值，等当前请求结束后再处理，避免并发覆盖。
    if (savingRef.current) {
      queuedRef.current = next;
      return;
    }

    savingRef.current = true;
    let succeeded = false;
    if (mountedRef.current) setStatus("saving");
    try {
      await onSaveRef.current(next);
      persistedRef.current = next;
      succeeded = true;
    } catch (error) {
      if (mountedRef.current) setStatus("error");
      onErrorRef.current?.(error);
    } finally {
      savingRef.current = false;
      const queued = queuedRef.current;
      queuedRef.current = null;
      // 如果保存期间又有新值排队，就继续保存它；否则若本次成功则标记为已保存。
      if (queued && !isEqualRef.current(queued, persistedRef.current)) {
        void runSave(queued);
      } else if (succeeded && mountedRef.current) {
        setStatus("saved");
        onSuccessRef.current?.(next);
      }
    }
  }, []);

  // 立即保存：先清掉待触发的防抖定时器，再直接执行保存。
  const saveNow = useCallback(
    (next: T) => {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
      void runSave(next);
    },
    [runSave],
  );

  const flush = useCallback(() => {
    saveNow(latestValueRef.current);
  }, [saveNow]);

  useEffect(() => {
    if (timerRef.current) clearTimeout(timerRef.current);

    // 未启用或值没变化时清空定时器；未启用时把状态归位到空闲。
    if (!enabled || isEqual(value, persistedRef.current)) {
      timerRef.current = null;
      if (!enabled) setStatus("idle");
      return;
    }

    // 有改动且启用：先标记为保存中，再等待防抖延迟后真正保存。
    setStatus("saving");
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      void runSave(latestValueRef.current);
    }, delay);

    // 组件卸载或依赖变化时，清理掉尚未触发的定时器。
    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
    };
  }, [delay, enabled, isEqual, runSave, value]);

  useEffect(() => {
    // 每次挂载都把 mounted 标志恢复为 true。React 严格模式下会执行「初始化-清理-再初始化」，
    // 如果不在这里恢复，mountedRef 会被卡在 false，导致状态一直停留在「保存中」。
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  return { status, flush, saveNow };
}

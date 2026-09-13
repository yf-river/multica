"use client";

// 订阅 WebSocket 事件与重连回调的 React hooks（内部对接 WSProvider 上下文）。
import { useEffect } from "react";
import type { WSEventType } from "../types";
import { useWS } from "./provider";

type EventHandler = (payload: unknown, actorId?: string, actorType?: string) => void;

/**
 * 订阅一个 WebSocket 事件并在事件到达时调用 handler 的 hook。
 * 卸载时自动退订。
 * @param event 要订阅的 WS 事件类型。
 * @param handler 事件处理器（载荷 + 触发者 id/类型）。
 */
export function useWSEvent(event: WSEventType, handler: EventHandler) {
  const { subscribe } = useWS();

  useEffect(() => {
    const unsub = subscribe(event, handler);
    return unsub;
  }, [event, handler, subscribe]);
}

/**
 * 注册一个在 WebSocket 重连时执行的回调的 hook。
 * 网络中断后重取组件本地数据很常用。
 * @param callback 重连后要执行的函数。
 */
export function useWSReconnect(callback: () => void) {
  const { onReconnect } = useWS();

  useEffect(() => {
    const unsub = onReconnect(callback);
    return unsub;
  }, [callback, onReconnect]);
}

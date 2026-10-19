"use client";
import { createContext } from "react";

export interface BuildInfo {
  builtAt: string;
  commit: string;
  digest: string;
}

// 页面身份固定为首次渲染的产物，服务端变化只提示刷新。
export const BuildInfoContext = createContext<{
  build: BuildInfo | null;
  updateAvailable: boolean;
  reload: () => void;
}>({ build: null, updateAvailable: false, reload: () => {} });

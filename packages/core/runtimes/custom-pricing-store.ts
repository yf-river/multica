"use client";

// 用户为"我们没有维护稳定费率"的模型手填的价格 store（费用估算时使用）。
import { create } from "zustand";
import { createJSONStorage, persist, type StateStorage } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";

// 用户为没有内置维护费率的模型（OpenRouter / Codex / Hermes / Kimi 等）自行补充的价格。
// 我们追不完这些供应商发布的每个模型，所以空状态诊断里让用户填自己的费率。
// 全局（不按工作区）存储，因为 `gpt-5.5-mini` 的费率不因你在看哪个工作区而变。
export interface CustomModelPricing {
  /** 输入每百万 token 价格（美元）。 */
  input: number;
  /** 输出每百万 token 价格（美元）。 */
  output: number;
  /** 缓存命中的读价。 */
  cacheRead: number;
  /** 缓存写入价。 */
  cacheWrite: number;
}

export interface CustomPricingState {
  /** model id → 用户手填的价格。 */
  pricings: Record<string, CustomModelPricing>;
  /** 设置某个模型的价格。 */
  setCustomPricing: (model: string, pricing: CustomModelPricing) => void;
  /** 移除某个模型的手填价格（回退到默认/未知）。 */
  removeCustomPricing: (model: string) => void;
}

// 我们的 StorageAdapter（同步 getItem 返回 string | null）结构上是 zustand
// StateStorage 的子集，可以直接经类型断言喂进去。
const stateStorage = defaultStorage as unknown as StateStorage;

export const useCustomPricingStore = create<CustomPricingState>()(
  persist(
    (set) => ({
      pricings: {},
      setCustomPricing: (model, pricing) =>
        set((state) => ({
          pricings: { ...state.pricings, [model]: pricing },
        })),
      removeCustomPricing: (model) =>
        set((state) => {
          if (!(model in state.pricings)) return state;
          const next = { ...state.pricings };
          delete next[model];
          return { pricings: next };
        }),
    }),
    {
      name: "multica_runtime_custom_pricing",
      storage: createJSONStorage(() => stateStorage),
    },
  ),
);

// 非 React 调用方的 vanilla 读取入口（packages/views/runtimes/utils.ts 的
// `resolvePricing` 辅助在做成本估算时从这里读）。
/** 读取某模型的手填价格；未填过返回 undefined。 */
export function getCustomPricing(model: string): CustomModelPricing | undefined {
  return useCustomPricingStore.getState().pricings[model];
}

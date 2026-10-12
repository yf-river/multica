import { create } from "zustand";

// Bounded, ephemeral reading positions; no content or credentials are retained.
export const useCookingScroll = create<{
  positions: Record<string, number>;
  save: (key: string, position: number) => void;
}>((set) => ({
  positions: {},
  save: (key, position) =>
    set((state) => ({
      positions: Object.fromEntries(
        [
          ...Object.entries(state.positions).filter(([id]) => id !== key),
          [key, position],
        ].slice(-30),
      ),
    })),
}));

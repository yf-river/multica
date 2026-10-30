import type { ComponentType } from "react";
export type Translate = (key: string, params?: Record<string, string | number>) => string;
export interface StatsProps {
  useProjection: (key: string) => unknown;
  usePerformanceUsage: <T>(selector: (mode: string) => T) => T;
  t: Translate;
}
export const ActivityPill: ComponentType<StatsProps>;
export const UsagePill: ComponentType<StatsProps>;

import type { ComponentType } from "react";
import type { Translate } from "./stats";
export const ContextMeter: ComponentType<{ useProjection: (key: string) => unknown; t: Translate }>;

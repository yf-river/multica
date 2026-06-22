import { describe, expect, it } from "vitest";
import {
  DEFAULT_TRAINING_WORKBENCH_TAB,
  DEFAULT_TRAINING_WORKBENCH_VIEW,
  trainingWorkbenchTabFromView,
} from "./index";

describe("training workbench navigation", () => {
  it("uses the demo dashboard as the production default entry", () => {
    expect(DEFAULT_TRAINING_WORKBENCH_TAB).toBe("演示看板");
    expect(DEFAULT_TRAINING_WORKBENCH_VIEW).toBe("demo-dashboard");
    expect(trainingWorkbenchTabFromView(null)).toBe("演示看板");
    expect(trainingWorkbenchTabFromView("missing-view")).toBe("演示看板");
  });

  it("keeps legacy prompt-library deep links on the prompt library tab", () => {
    expect(trainingWorkbenchTabFromView("prompts")).toBe("提示词库");
  });
});

import { describe, expect, it } from "vitest";
import {
  DEFAULT_TRAINING_WORKBENCH_TAB,
  DEFAULT_TRAINING_WORKBENCH_VIEW,
  trainingWorkbenchTabFromView,
  trainingWorkbenchShowsPromptEditor,
  trainingWorkbenchTitleFromView,
} from "./index";

describe("training workbench navigation", () => {
  it("uses the run dashboard as the default entry", () => {
    expect(DEFAULT_TRAINING_WORKBENCH_TAB).toBe("运行看板");
    expect(DEFAULT_TRAINING_WORKBENCH_VIEW).toBe("runs");
    expect(trainingWorkbenchTabFromView(null)).toBe("运行看板");
    expect(trainingWorkbenchTabFromView("missing-view")).toBe("运行看板");
    expect(trainingWorkbenchTabFromView("demo-dashboard")).toBe("运行看板");
  });

  it("keeps legacy prompt-library deep links on the prompt library tab", () => {
    expect(trainingWorkbenchTabFromView("prompts")).toBe("提示词库");
  });

  it("only shows the prompt editor on the prompt library route", () => {
    expect(trainingWorkbenchShowsPromptEditor("prompts")).toBe(true);
    expect(trainingWorkbenchShowsPromptEditor("runs")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("run-history")).toBe(false);
  });

  it("builds distinct Chinese titles for desktop training tabs", () => {
    expect(trainingWorkbenchTitleFromView(null)).toBe("训练与评估 · 运行看板");
    expect(trainingWorkbenchTitleFromView("prompts")).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("run-history")).toBe("训练与评估 · 运行历史");
    expect(trainingWorkbenchTitleFromView("missing-view")).toBe("训练与评估 · 运行看板");
  });
});

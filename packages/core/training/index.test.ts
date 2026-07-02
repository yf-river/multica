import { describe, expect, it } from "vitest";
import {
  DEFAULT_TRAINING_WORKBENCH_TAB,
  DEFAULT_TRAINING_WORKBENCH_VIEW,
  TRAINING_WORKBENCH_VIEWS,
  trainingWorkbenchTabFromView,
  trainingWorkbenchShowsPromptEditor,
  trainingWorkbenchTitleFromView,
} from "./index";

describe("training workbench navigation", () => {
  it("uses the prompt library as the default entry", () => {
    expect(DEFAULT_TRAINING_WORKBENCH_TAB).toBe("提示词库");
    expect(DEFAULT_TRAINING_WORKBENCH_VIEW).toBe("prompts");
    expect(trainingWorkbenchTabFromView(null)).toBe("提示词库");
    expect(trainingWorkbenchTabFromView("missing-view")).toBe("提示词库");
    expect(trainingWorkbenchTabFromView("evaluation-runs")).toBe("评测记录");
  });

  it("exposes the core training loop routes in navigation", () => {
    expect(TRAINING_WORKBENCH_VIEWS.map((item) => item.view)).toEqual([
      "prompts",
      "datasets",
      "test-suites",
      "evaluation-runs",
    ]);
  });

  it("maps current prompt-library deep links onto the prompt library tab", () => {
    expect(trainingWorkbenchTabFromView("prompts")).toBe("提示词库");
  });

  it("only shows the prompt editor on the prompt library route", () => {
    expect(trainingWorkbenchShowsPromptEditor("prompts")).toBe(true);
    expect(trainingWorkbenchShowsPromptEditor("datasets")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("test-suites")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("evaluation-runs")).toBe(false);
  });

  it("builds distinct Chinese titles for desktop training tabs", () => {
    expect(trainingWorkbenchTitleFromView(null)).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("prompts")).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("evaluation-runs")).toBe("训练与评估 · 评测记录");
    expect(trainingWorkbenchTitleFromView("missing-view")).toBe("训练与评估 · 提示词库");
  });

  it("falls back unknown views to the prompt library", () => {
    expect(trainingWorkbenchTabFromView("missing-view")).toBe("提示词库");
  });
});

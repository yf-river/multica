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
    expect(trainingWorkbenchTabFromView("demo-dashboard")).toBe("评测记录");
  });

  it("exposes the core training loop routes in navigation", () => {
    expect(TRAINING_WORKBENCH_VIEWS.map((item) => item.view)).toEqual([
      "prompts",
      "datasets",
      "test-suites",
      "evaluation-runs",
    ]);
  });

  it("maps legacy prompt-library deep links onto the prompt library tab", () => {
    expect(trainingWorkbenchTabFromView("prompts")).toBe("提示词库");
    expect(trainingWorkbenchTabFromView("debug-runs")).toBe("提示词库");
    expect(trainingWorkbenchTabFromView("prompt-playground")).toBe("提示词库");
    expect(trainingWorkbenchTabFromView("agent-playground")).toBe("提示词库");
  });

  it("only shows the prompt editor on the prompt library route", () => {
    expect(trainingWorkbenchShowsPromptEditor("prompts")).toBe(true);
    expect(trainingWorkbenchShowsPromptEditor("debug-runs")).toBe(true);
    expect(trainingWorkbenchShowsPromptEditor("prompt-playground")).toBe(true);
    expect(trainingWorkbenchShowsPromptEditor("agent-playground")).toBe(true);
    expect(trainingWorkbenchShowsPromptEditor("runs")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("evaluation-runs")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("run-history")).toBe(false);
  });

  it("builds distinct Chinese titles for desktop training tabs", () => {
    expect(trainingWorkbenchTitleFromView(null)).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("prompts")).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("debug-runs")).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("prompt-playground")).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("agent-playground")).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("evaluation-runs")).toBe("训练与评估 · 评测记录");
    expect(trainingWorkbenchTitleFromView("run-history")).toBe("训练与评估 · 评测记录");
    expect(trainingWorkbenchTitleFromView("missing-view")).toBe("训练与评估 · 提示词库");
  });

  it("maps removed experiment routes onto their replacement surfaces", () => {
    expect(trainingWorkbenchTabFromView("experiments")).toBe("测试套件");
    expect(trainingWorkbenchTabFromView("optimization-runs")).toBe("评测记录");
    expect(trainingWorkbenchTabFromView("runs")).toBe("评测记录");
  });
});

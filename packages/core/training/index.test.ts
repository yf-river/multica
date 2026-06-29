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
    expect(trainingWorkbenchTabFromView("demo-dashboard")).toBe("运行看板");
  });

  it("only exposes the asset and debug workbench routes in navigation", () => {
    expect(TRAINING_WORKBENCH_VIEWS.map((item) => item.view)).toEqual([
      "prompts",
      "debug-runs",
      "datasets",
      "test-suites",
    ]);
  });

  it("keeps legacy prompt-library deep links on the prompt library tab", () => {
    expect(trainingWorkbenchTabFromView("prompts")).toBe("提示词库");
  });

  it("only shows the prompt editor on the prompt library route", () => {
    expect(trainingWorkbenchShowsPromptEditor("prompts")).toBe(true);
    expect(trainingWorkbenchShowsPromptEditor("debug-runs")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("prompt-playground")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("agent-playground")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("runs")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("evaluation-runs")).toBe(false);
    expect(trainingWorkbenchShowsPromptEditor("run-history")).toBe(false);
  });

  it("builds distinct Chinese titles for desktop training tabs", () => {
    expect(trainingWorkbenchTitleFromView(null)).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("prompts")).toBe("训练与评估 · 提示词库");
    expect(trainingWorkbenchTitleFromView("debug-runs")).toBe("训练与评估 · 调试运行");
    expect(trainingWorkbenchTitleFromView("prompt-playground")).toBe("训练与评估 · 提示词调试场");
    expect(trainingWorkbenchTitleFromView("agent-playground")).toBe("训练与评估 · 智能体调试场");
    expect(trainingWorkbenchTitleFromView("evaluation-runs")).toBe("训练与评估 · 评测记录");
    expect(trainingWorkbenchTitleFromView("run-history")).toBe("训练与评估 · 运行历史");
    expect(trainingWorkbenchTitleFromView("missing-view")).toBe("训练与评估 · 提示词库");
  });

  it("keeps legacy prompt and agent playground navigation labels separate", () => {
    expect(trainingWorkbenchTabFromView("debug-runs")).toBe("调试运行");
    expect(trainingWorkbenchTabFromView("prompt-playground")).toBe("提示词调试场");
    expect(trainingWorkbenchTabFromView("agent-playground")).toBe("智能体调试场");
  });
});

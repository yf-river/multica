import { describe, expect, it } from "vitest";
import {
  DEFAULT_TRAINING_WORKBENCH_TAB,
  DEFAULT_TRAINING_WORKBENCH_VIEW,
  TRAINING_WORKBENCH_VIEWS,
  TRAINING_WORKBENCH_VIEWS_BY_SECTION,
  debugWorkbenchPath,
  evaluationWorkbenchPath,
  trainingWorkbenchCanonicalPath,
  trainingWorkbenchCanonicalRouteFromView,
  trainingWorkbenchSectionFromView,
  trainingWorkbenchTabFromView,
  trainingWorkbenchShowsPromptEditor,
  trainingWorkbenchTitleFromView,
  trainingWorkbenchViewFromCanonicalRoute,
  trainingWorkbenchViewFromRoute,
} from "./index";

describe("training workbench navigation", () => {
  it("uses the prompt library as the default entry", () => {
    expect(DEFAULT_TRAINING_WORKBENCH_TAB).toBe("提示词库");
    expect(DEFAULT_TRAINING_WORKBENCH_VIEW).toBe("prompts");
    expect(trainingWorkbenchTabFromView(null)).toBe("提示词库");
    expect(trainingWorkbenchTabFromView("missing-view")).toBe("提示词库");
    expect(trainingWorkbenchTabFromView("evaluation-runs")).toBe("评测记录");
    expect(trainingWorkbenchTabFromView("datasets")).toBe("用例库");
  });

  it("exposes the core training loop routes in navigation", () => {
    expect(TRAINING_WORKBENCH_VIEWS.map((item) => item.view)).toEqual([
      "prompts",
      "datasets",
      "test-suites",
      "evaluation-runs",
    ]);
  });

  it("groups views into debug and evaluation sections", () => {
    expect(TRAINING_WORKBENCH_VIEWS_BY_SECTION.debug.map((item) => item.view)).toEqual(["prompts"]);
    expect(TRAINING_WORKBENCH_VIEWS_BY_SECTION.evaluation.map((item) => item.view)).toEqual([
      "datasets",
      "test-suites",
      "evaluation-runs",
    ]);
    expect(trainingWorkbenchSectionFromView("prompts")).toBe("debug");
    expect(trainingWorkbenchSectionFromView("datasets")).toBe("evaluation");
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
    expect(trainingWorkbenchTitleFromView(null)).toBe("调试 · 提示词库");
    expect(trainingWorkbenchTitleFromView("prompts")).toBe("调试 · 提示词库");
    expect(trainingWorkbenchTitleFromView("datasets")).toBe("评估 · 用例库");
    expect(trainingWorkbenchTitleFromView("evaluation-runs")).toBe("评估 · 评测记录");
    expect(trainingWorkbenchTitleFromView("missing-view")).toBe("调试 · 提示词库");
  });

  it("builds canonical debug and evaluation paths while keeping legacy route parsing", () => {
    const paths = { debug: () => "/acme/debug", evaluation: () => "/acme/evaluation" };

    expect(trainingWorkbenchCanonicalRouteFromView("prompts")).toBe("prompts");
    expect(trainingWorkbenchCanonicalRouteFromView("evaluation-runs")).toBe("runs");
    expect(trainingWorkbenchViewFromRoute("evaluation-runs")).toBe("evaluation-runs");
    expect(trainingWorkbenchViewFromRoute("runs")).toBe("evaluation-runs");
    expect(trainingWorkbenchViewFromCanonicalRoute("debug", "datasets")).toBe("prompts");
    expect(trainingWorkbenchViewFromCanonicalRoute("evaluation", "prompts")).toBe("datasets");
    expect(debugWorkbenchPath(paths.debug(), "prompts")).toBe("/acme/debug/prompts");
    expect(evaluationWorkbenchPath(paths.evaluation(), "evaluation-runs")).toBe("/acme/evaluation/runs");
    expect(trainingWorkbenchCanonicalPath(paths, "prompts")).toBe("/acme/debug/prompts");
    expect(trainingWorkbenchCanonicalPath(paths, "datasets")).toBe("/acme/evaluation/datasets");
  });

  it("falls back unknown views to the prompt library", () => {
    expect(trainingWorkbenchTabFromView("missing-view")).toBe("提示词库");
  });
});

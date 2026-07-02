import { describe, expect, it } from "vitest";
import { Navigate } from "react-router-dom";
import { PromptLibraryPage } from "@multica/views/prompt-library";
import { appRoutes, TrainingLegacyRedirect } from "./routes";

describe("desktop training routes", () => {
  const workspaceRoute = appRoutes[0]?.children?.find((route) => route.path === ":workspaceSlug");
  const childRoutes = workspaceRoute?.children ?? [];

  it("redirects the training index to the prompt library", () => {
    const training = childRoutes.find((route) => route.path === "training");
    expect(training?.handle).toMatchObject({ title: "训练与评估" });
    const indexRoute = training?.children?.find((route) => route.index);
    expect(indexRoute?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: "prompts", replace: true }),
    });
  });

  it.each([
    ["prompt-library", "../training/prompts", "训练与评估"],
    ["evaluation", "../run-reviews", "运行复盘"],
    ["eval", "../run-reviews", "运行复盘"],
  ])("redirects legacy %s route into training", (routePath, target, title) => {
    const route = childRoutes.find((item) => item.path === routePath);
    expect(route?.handle).toMatchObject({ title });
    expect(route?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: target, replace: true }),
    });
  });

  it.each([
    ["prompts", "训练与评估", PromptLibraryPage, { activeView: "prompts" }],
    ["datasets", "训练与评估", PromptLibraryPage, { activeView: "datasets" }],
    ["test-suites", "训练与评估", PromptLibraryPage, { activeView: "test-suites" }],
    ["evaluation-runs", "训练与评估", PromptLibraryPage, { activeView: "evaluation-runs" }],
  ])("maps training/%s to the matching training and evaluation view", (routePath, title, component, props) => {
    const trainingRoute = childRoutes.find((route) => route.path === "training");
    const childRoute = trainingRoute?.children?.find((route) => route.path === routePath);
    expect(childRoute?.handle).toMatchObject({ title });
    expect(childRoute?.element).toMatchObject({
      type: component,
      props: expect.objectContaining(props),
    });
  });

  it.each([
    ["runs", Navigate, { to: "../../run-reviews", replace: true }],
    ["run-history", TrainingLegacyRedirect, { to: "../evaluation-runs" }],
    ["debug-runs", TrainingLegacyRedirect, { to: "../prompts" }],
    ["prompt-playground", TrainingLegacyRedirect, { to: "../prompts" }],
    ["agent-playground", TrainingLegacyRedirect, { to: "../prompts" }],
    ["experiments", TrainingLegacyRedirect, { to: "../test-suites" }],
    ["optimization-runs", TrainingLegacyRedirect, { to: "../evaluation-runs" }],
  ])("redirects legacy training/%s routes", (routePath, component, props) => {
    const trainingRoute = childRoutes.find((route) => route.path === "training");
    const childRoute = trainingRoute?.children?.find((route) => route.path === routePath);
    expect(childRoute?.element).toMatchObject({
      type: component,
      props: expect.objectContaining(props),
    });
  });
});

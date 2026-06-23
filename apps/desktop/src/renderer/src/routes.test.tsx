import { describe, expect, it } from "vitest";
import { Navigate } from "react-router-dom";
import { PromptLibraryPage } from "@multica/views/prompt-library";
import { appRoutes } from "./routes";

describe("desktop training routes", () => {
  const workspaceRoute = appRoutes[0]?.children?.find((route) => route.path === ":workspaceSlug");
  const childRoutes = workspaceRoute?.children ?? [];

  it("redirects the training index to the run dashboard", () => {
    const training = childRoutes.find((route) => route.path === "training");
    expect(training?.handle).toMatchObject({ title: "训练与评估" });
    const indexRoute = training?.children?.find((route) => route.index);
    expect(indexRoute?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: "runs", replace: true }),
    });
  });

  it.each([
    ["prompt-library", "../training/prompts"],
    ["evaluation", "../training/runs"],
    ["eval", "../training/runs"],
  ])("redirects legacy %s route into training", (routePath, target) => {
    const route = childRoutes.find((item) => item.path === routePath);
    expect(route?.handle).toMatchObject({ title: "训练与评估" });
    expect(route?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: target, replace: true }),
    });
  });

  it.each([
    "runs",
    "prompts",
    "prompt-playground",
    "agent-playground",
    "datasets",
    "test-suites",
    "experiments",
    "optimization-runs",
    "run-history",
  ])("maps training/%s to the shared training and evaluation page", (routePath) => {
    const trainingRoute = childRoutes.find((route) => route.path === "training");
    const childRoute = trainingRoute?.children?.find((route) => route.path === routePath);
    expect(childRoute?.handle).toMatchObject({ title: "训练与评估" });
    expect(childRoute?.element).toMatchObject({ type: PromptLibraryPage });
  });
});

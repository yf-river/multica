import { describe, expect, it } from "vitest";
import { Navigate } from "react-router-dom";
import { PromptLibraryPage } from "@multica/views/prompt-library";
import { AgentPlaygroundPage } from "@multica/views/agent-playground";
import { appRoutes } from "./routes";

describe("desktop debug and evaluation routes", () => {
  const workspaceRoute = appRoutes[0]?.children?.find((route) => route.path === ":workspaceSlug");
  const childRoutes = workspaceRoute?.children ?? [];

  it("redirects debug and evaluation indexes to their default views", () => {
    const debug = childRoutes.find((route) => route.path === "debug");
    const evaluation = childRoutes.find((route) => route.path === "evaluation");

    expect(debug?.handle).toMatchObject({ title: "调试" });
    expect(evaluation?.handle).toMatchObject({ title: "评估" });

    const debugIndex = debug?.children?.find((route) => route.index);
    expect(debugIndex?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: "prompts", replace: true }),
    });

    const evaluationIndex = evaluation?.children?.find((route) => route.index);
    expect(evaluationIndex?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: "datasets", replace: true }),
    });
  });

  it.each([
    ["debug", "prompts", "调试", PromptLibraryPage, { activeView: "prompts" }],
    ["debug", "agent-playground", "调试", AgentPlaygroundPage, {}],
    ["evaluation", "datasets", "评估", PromptLibraryPage, { activeView: "datasets" }],
    ["evaluation", "test-suites", "评估", PromptLibraryPage, { activeView: "test-suites" }],
    ["evaluation", "runs", "评估", PromptLibraryPage, { activeView: "evaluation-runs" }],
  ])("maps %s/%s to the matching view", (parentPath, routePath, title, component, props) => {
    const parentRoute = childRoutes.find((route) => route.path === parentPath);
    const childRoute = parentRoute?.children?.find((route) => route.path === routePath);
    expect(childRoute?.handle).toMatchObject({ title });
    expect(childRoute?.element).toMatchObject({
      type: component,
      props: expect.objectContaining(props),
    });
  });

  it("keeps legacy training routes as redirects", () => {
    const trainingRoute = childRoutes.find((route) => route.path === "training");
    expect(trainingRoute?.handle).toMatchObject({ title: "训练与评估" });

    const indexRoute = trainingRoute?.children?.find((route) => route.index);
    expect(indexRoute?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: "../debug/prompts", replace: true }),
    });

    const redirects = [
      ["prompts", "../../debug/prompts"],
      ["agent-playground", "../../debug/agent-playground"],
      ["datasets", "../../evaluation/datasets"],
      ["test-suites", "../../evaluation/test-suites"],
      ["evaluation-runs", "../../evaluation/runs"],
    ] as const;
    for (const [routePath, to] of redirects) {
      const childRoute = trainingRoute?.children?.find((route) => route.path === routePath);
      expect(childRoute?.element).toMatchObject({
        type: Navigate,
        props: expect.objectContaining({ to, replace: true }),
      });
    }
  });

  it("does not keep legacy training aliases", () => {
    const trainingRoute = childRoutes.find((route) => route.path === "training");
    const paths = new Set((trainingRoute?.children ?? []).map((route) => route.path));
    for (const removed of ["runs", "run-history", "debug-runs", "prompt-playground", "experiments", "optimization-runs"]) {
      expect(paths.has(removed)).toBe(false);
    }
  });
});

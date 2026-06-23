import { describe, expect, it } from "vitest";
import { Navigate } from "react-router-dom";
import { PromptLibraryPage } from "@multica/views/prompt-library";
import { appRoutes } from "./routes";

describe("desktop training routes", () => {
  const workspaceRoute = appRoutes[0]?.children?.find((route) => route.path === ":workspaceSlug");
  const childRoutes = workspaceRoute?.children ?? [];

  it("maps training to the shared training and evaluation page", () => {
    const training = childRoutes.find((route) => route.path === "training");
    expect(training?.handle).toMatchObject({ title: "训练与评估" });
    expect(training?.element).toMatchObject({ type: PromptLibraryPage });
  });

  it.each([
    ["prompt-library", "../training?view=prompts"],
    ["evaluation", "../training?view=demo-dashboard"],
    ["eval", "../training?view=demo-dashboard"],
  ])("redirects legacy %s route into training", (routePath, target) => {
    const route = childRoutes.find((item) => item.path === routePath);
    expect(route?.handle).toMatchObject({ title: "训练与评估" });
    expect(route?.element).toMatchObject({
      type: Navigate,
      props: expect.objectContaining({ to: target, replace: true }),
    });
  });
});

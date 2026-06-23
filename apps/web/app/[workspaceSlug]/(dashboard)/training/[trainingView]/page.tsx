import { redirect } from "next/navigation";
import { PromptLibraryPage, TrainingWorkbenchPage } from "@multica/views/prompt-library";
import {
  TRAINING_WORKBENCH_ROUTE_BY_VIEW,
  trainingWorkbenchPath,
  trainingWorkbenchViewFromRoute,
} from "@multica/core/training";

export default async function TrainingViewPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string; trainingView: string }>;
}) {
  const { workspaceSlug, trainingView } = await params;
  const view = trainingWorkbenchViewFromRoute(trainingView);
  const canonicalRoute = TRAINING_WORKBENCH_ROUTE_BY_VIEW[view];

  if (canonicalRoute !== trainingView) {
    const baseTrainingPath = `/${encodeURIComponent(workspaceSlug)}/training`;
    redirect(trainingWorkbenchPath(baseTrainingPath, view));
  }

  return view === "runs" ||
    view === "run-history" ||
    view === "datasets" ||
    view === "test-suites" ||
    view === "experiments" ||
    view === "optimization-runs" ? (
    <TrainingWorkbenchPage activeView={view} />
  ) : (
    <PromptLibraryPage activeView={view} showPromptEditor />
  );
}

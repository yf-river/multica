import { redirect } from "next/navigation";
import { PromptLibraryPage } from "@multica/views/prompt-library";
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

  return <PromptLibraryPage activeView={view} />;
}

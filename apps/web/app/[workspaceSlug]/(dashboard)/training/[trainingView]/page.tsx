import { redirect } from "next/navigation";
import { PromptLibraryPage, TrainingWorkbenchPage } from "@multica/views/prompt-library";
import {
  TRAINING_WORKBENCH_ROUTE_BY_VIEW,
  trainingWorkbenchPath,
  trainingWorkbenchViewFromRoute,
} from "@multica/core/training";

const RUN_REVIEW_ROUTES = new Set(["runs", "run-history", "demo-dashboard"]);
const DEBUG_RUN_ROUTES = new Set(["prompt-playground", "agent-playground"]);
const PROMPT_ROUTES = new Set(["debug-runs"]);
const TEST_SUITE_ROUTES = new Set(["experiments"]);
const EVALUATION_RUN_ROUTES = new Set(["optimization-runs"]);
type TrainingSearchParams = Record<string, string | string[] | undefined>;

function searchSuffix(
  searchParams: TrainingSearchParams,
  options: { mode?: string } = {},
): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(searchParams)) {
    if (value === undefined) continue;
    if (Array.isArray(value)) {
      for (const item of value) search.append(key, item);
    } else {
      search.set(key, value);
    }
  }
  if (options.mode && !search.has("mode")) {
    search.set("mode", options.mode);
  }
  const query = search.toString();
  return query ? `?${query}` : "";
}

export default async function TrainingViewPage({
  params,
  searchParams,
}: {
  params: Promise<{ workspaceSlug: string; trainingView: string }>;
  searchParams: Promise<TrainingSearchParams>;
}) {
  const { workspaceSlug, trainingView } = await params;
  const resolvedSearchParams = await searchParams;
  const workspaceBase = `/${encodeURIComponent(workspaceSlug)}`;
  const baseTrainingPath = `${workspaceBase}/training`;

  if (trainingView === "run-history") {
    redirect(`${baseTrainingPath}/evaluation-runs${searchSuffix(resolvedSearchParams)}`);
  }
  if (RUN_REVIEW_ROUTES.has(trainingView)) {
    redirect(`${workspaceBase}/run-reviews${searchSuffix(resolvedSearchParams)}`);
  }
  if (DEBUG_RUN_ROUTES.has(trainingView)) {
    redirect(`${baseTrainingPath}/prompts${searchSuffix(resolvedSearchParams)}`);
  }
  if (PROMPT_ROUTES.has(trainingView)) {
    redirect(`${baseTrainingPath}/prompts${searchSuffix(resolvedSearchParams)}`);
  }
  if (TEST_SUITE_ROUTES.has(trainingView)) {
    redirect(`${baseTrainingPath}/test-suites${searchSuffix(resolvedSearchParams)}`);
  }
  if (EVALUATION_RUN_ROUTES.has(trainingView)) {
    redirect(`${baseTrainingPath}/evaluation-runs${searchSuffix(resolvedSearchParams)}`);
  }
  const view = trainingWorkbenchViewFromRoute(trainingView);
  const canonicalRoute = TRAINING_WORKBENCH_ROUTE_BY_VIEW[view];

  if (canonicalRoute !== trainingView) {
    redirect(`${trainingWorkbenchPath(baseTrainingPath, view)}${searchSuffix(resolvedSearchParams)}`);
  }

  if (view === "prompts") return <PromptLibraryPage activeView={view} showPromptEditor />;

  return <TrainingWorkbenchPage activeView={view} />;
}

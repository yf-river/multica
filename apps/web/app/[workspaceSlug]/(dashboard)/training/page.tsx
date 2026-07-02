import { redirect } from "next/navigation";
import { trainingWorkbenchPath, trainingWorkbenchViewFromRoute } from "@multica/core/training";

type TrainingSearchParams = { view?: string } & Record<string, string | string[] | undefined>;

function searchSuffix(searchParams: TrainingSearchParams, options: { mode?: string } = {}): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(searchParams)) {
    if (key === "view" || value === undefined) continue;
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

export default async function TrainingPage({
  params,
  searchParams,
}: {
  params: Promise<{ workspaceSlug: string }>;
  searchParams: Promise<TrainingSearchParams>;
}) {
  const { workspaceSlug } = await params;
  const resolvedSearchParams = await searchParams;
  const { view } = resolvedSearchParams;
  const workspaceBase = `/${encodeURIComponent(workspaceSlug)}`;
  const baseTrainingPath = `${workspaceBase}/training`;
  if (view === "run-history") {
    redirect(`${baseTrainingPath}/evaluation-runs${searchSuffix(resolvedSearchParams)}`);
  }
  if (view === "runs" || view === "demo-dashboard") {
    redirect(`${workspaceBase}/run-reviews${searchSuffix(resolvedSearchParams)}`);
  }
  if (view === "debug-runs" || view === "prompt-playground" || view === "agent-playground") {
    redirect(`${baseTrainingPath}/prompts${searchSuffix(resolvedSearchParams)}`);
  }
  if (view === "experiments") {
    redirect(`${baseTrainingPath}/test-suites${searchSuffix(resolvedSearchParams)}`);
  }
  if (view === "optimization-runs") {
    redirect(`${baseTrainingPath}/evaluation-runs${searchSuffix(resolvedSearchParams)}`);
  }
  redirect(`${trainingWorkbenchPath(baseTrainingPath, trainingWorkbenchViewFromRoute(view ?? null))}${searchSuffix(resolvedSearchParams)}`);
}

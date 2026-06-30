import { TrainingWorkbenchPage } from "@multica/views/prompt-library";
import type { TrainingWorkbenchViewId } from "@multica/core/training";

type TrainingSearchParams = Record<string, string | string[] | undefined>;

function activeViewFromMode(searchParams: TrainingSearchParams): TrainingWorkbenchViewId {
  const mode = Array.isArray(searchParams.mode) ? searchParams.mode[0] : searchParams.mode;
  if (mode === "experiment") return "experiments";
  if (mode === "optimize") return "optimization-runs";
  return "test-suites";
}

export default async function TrainingTestSuitesPage({
  searchParams,
}: {
  searchParams: Promise<TrainingSearchParams>;
}) {
  return <TrainingWorkbenchPage activeView={activeViewFromMode(await searchParams)} />;
}

import { redirect } from "next/navigation";
import { trainingWorkbenchPath, trainingWorkbenchViewFromRoute } from "@multica/core/training";

export default async function TrainingPage({
  params,
  searchParams,
}: {
  params: Promise<{ workspaceSlug: string }>;
  searchParams: Promise<{ view?: string }>;
}) {
  const { workspaceSlug } = await params;
  const { view } = await searchParams;
  const baseTrainingPath = `/${encodeURIComponent(workspaceSlug)}/training`;
  redirect(trainingWorkbenchPath(baseTrainingPath, trainingWorkbenchViewFromRoute(view ?? null)));
}

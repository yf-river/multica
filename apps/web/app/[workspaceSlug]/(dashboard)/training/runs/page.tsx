import { redirect } from "next/navigation";

export default async function TrainingRunsPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(`/${encodeURIComponent(workspaceSlug)}/run-reviews`);
}

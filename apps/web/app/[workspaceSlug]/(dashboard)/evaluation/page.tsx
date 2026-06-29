import { redirect } from "next/navigation";

export default async function LegacyEvaluationPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(`/${encodeURIComponent(workspaceSlug)}/run-reviews`);
}

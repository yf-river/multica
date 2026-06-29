import { redirect } from "next/navigation";

export default async function TrainingAgentPlaygroundPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(`/${encodeURIComponent(workspaceSlug)}/training/debug-runs`);
}

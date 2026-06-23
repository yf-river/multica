import { redirect } from "next/navigation";

export default async function LegacyEvalPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(`/${encodeURIComponent(workspaceSlug)}/training/runs`);
}

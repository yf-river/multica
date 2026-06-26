import { redirect } from "next/navigation";

export default async function LegacyUsagePage({
  params,
  searchParams,
}: {
  params: Promise<{ workspaceSlug: string }>;
  searchParams: Promise<{ issue?: string }>;
}) {
  const { workspaceSlug } = await params;
  const { issue } = await searchParams;
  const query = issue ? `?issue=${encodeURIComponent(issue)}` : "";
  redirect(`/${encodeURIComponent(workspaceSlug)}/run-reviews${query}`);
}

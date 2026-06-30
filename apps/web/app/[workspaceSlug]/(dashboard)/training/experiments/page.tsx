import { redirect } from "next/navigation";

type TrainingSearchParams = Record<string, string | string[] | undefined>;

function searchSuffix(searchParams: TrainingSearchParams): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(searchParams)) {
    if (value === undefined) continue;
    if (Array.isArray(value)) {
      for (const item of value) search.append(key, item);
    } else {
      search.set(key, value);
    }
  }
  if (!search.has("mode")) {
    search.set("mode", "experiment");
  }
  const query = search.toString();
  return query ? `?${query}` : "";
}

export default async function TrainingExperimentsPage({
  params,
  searchParams,
}: {
  params: Promise<{ workspaceSlug: string }>;
  searchParams: Promise<TrainingSearchParams>;
}) {
  const { workspaceSlug } = await params;
  const resolvedSearchParams = await searchParams;
  redirect(`/${encodeURIComponent(workspaceSlug)}/training/test-suites${searchSuffix(resolvedSearchParams)}`);
}

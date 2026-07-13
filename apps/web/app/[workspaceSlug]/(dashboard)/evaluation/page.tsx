import { redirect } from "next/navigation";
import {
  searchParamsSuffix,
  type RouteSearchParams,
} from "../../../../platform/search-params";

export default async function EvaluationPage({
  params,
  searchParams,
}: {
  params: Promise<{ workspaceSlug: string }>;
  searchParams: Promise<RouteSearchParams>;
}) {
  const { workspaceSlug } = await params;
  redirect(
    `/${encodeURIComponent(workspaceSlug)}/evaluation/datasets${searchParamsSuffix(await searchParams)}`,
  );
}

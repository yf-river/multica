import { readBuildInfo } from "@/platform/build-info";

export const dynamic = "force-dynamic";

export async function GET() {
  return Response.json(await readBuildInfo(), { headers: { "Cache-Control": "no-store" } });
}

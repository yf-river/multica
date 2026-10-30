import { api } from "@multica/core/api";
import { asRecord } from "./native-dist/client/services";

// 原版页面只通过此适配访问 Multica，不使用 DSH 网页的 cookie 或相对路由。
export async function insightsFetch(
  route: RequestInfo | URL,
  init?: RequestInit,
): Promise<Response> {
  const path = String(route);
  if (path === "prices") return Response.json(await api.getInsightPrices());
  const body =
    typeof init?.body === "string" ? asRecord(JSON.parse(init.body)) : null;
  const id = typeof body?.sessionId === "string" ? body.sessionId : "";
  if (path.endsWith("/detail")) {
    const { session } = await api.getContextInsight(id);
    const head = asRecord(session.projections.contextTimeline);
    return Response.json({
      ok: true,
      value: head ? { ...head, head, rev: session.seq } : null,
    });
  }
  if (path.endsWith("/commands")) {
    const { session } = await api.getContextInsight(id);
    const head = asRecord(session.projections.contextTimeline);
    const list = Array.isArray(head?.commands) ? head.commands : [];
    const before =
      typeof body?.beforeSeq === "number" ? body.beforeSeq : Infinity;
    const filtered = list.filter((value) => {
      const row = asRecord(value);
      return typeof row?.seq === "number" && row.seq < before;
    });
    const limit =
      typeof body?.limit === "number" ? Math.min(100, body.limit) : 100;
    return Response.json({
      ok: true,
      value: {
        commands: filtered.slice(-limit),
        atStart: filtered.length <= limit,
        total: list.length,
      },
    });
  }
  if (path.endsWith("/skills")) {
    const { sessions } = await api.listContextInsights();
    const names = new Set<string>();
    for (const session of sessions) {
      const activity = asRecord(session.projections.contextActivity);
      const days = asRecord(activity?.days);
      for (const day of Object.values(days ?? {})) {
        for (const name of Object.keys(asRecord(asRecord(day)?.skills) ?? {}))
          names.add(name);
      }
    }
    return Response.json({
      ok: true,
      value: { skills: [...names].map((name) => ({ name })) },
    });
  }
  throw new Error(`Unsupported insight endpoint: ${path}`);
}

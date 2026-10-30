"use client";

import { useNavigation } from "../navigation";

export function useInsightsView() {
  const nav = useNavigation();
  const change = (open: boolean) => {
    const params = new URLSearchParams(nav.searchParams);
    if (open) params.set("view", "insights");
    else {
      params.delete("view");
      params.delete("insight");
      params.delete("run");
    }
    nav.replace(`${nav.pathname}${params.size ? "?" + params : ""}${nav.hash}`);
  };
  return {
    insights: nav.searchParams.get("view") === "insights",
    taskId: nav.searchParams.get("run") ?? undefined,
    sessionKey: nav.searchParams.get("insight") ?? undefined,
    change,
  };
}

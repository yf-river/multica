"use client";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { BuildInfoContext, type BuildInfo } from "@multica/core/config";

export function BuildInfoProvider({ initialBuild, children }: {
  initialBuild: BuildInfo | null;
  children: React.ReactNode;
}) {
  const [build] = useState(initialBuild);
  const { data: current } = useQuery<BuildInfo | null>({
    queryKey: ["web-build"],
    queryFn: async () => {
      const response = await fetch("/api/version", { cache: "no-store" });
      if (!response.ok) throw new Error("无法检查当前版本");
      return response.json();
    },
    enabled: !!build,
    refetchInterval: 60_000,
    refetchOnWindowFocus: "always",
  });
  const updateAvailable = !!(build && current &&
    (build.builtAt !== current.builtAt || build.commit !== current.commit || build.digest !== current.digest));
  return <BuildInfoContext.Provider value={{ build, updateAvailable, reload: () => window.location.reload() }}>
    {children}
  </BuildInfoContext.Provider>;
}

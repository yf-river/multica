"use client";

import { useEffect } from "react";
import { usePathname } from "next/navigation";
import { parseTabSubject } from "@multica/core/paths";
import { useConfigStore } from "@multica/core/config";

/** 工作区标题由 WorkspaceDocumentTitle 管理；此处补充登录、引导等页面。 */
export function EnvironmentDocumentTitle() {
  const pathname = usePathname();
  const name = useConfigStore((state) => state.environmentName);
  useEffect(() => {
    if (!name || parseTabSubject(pathname).kind !== "unknown") return;
    const prefix = `[${name}] `;
    if (!document.title.startsWith(prefix)) document.title = prefix + document.title;
  }, [name, pathname]);
  return null;
}

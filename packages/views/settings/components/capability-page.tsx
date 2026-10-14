"use client";
import { CollapsedNavTrigger } from "../../layout/page-header";
import { McpTab } from "./mcp-tab";
import { QuickActionsTab } from "./quick-actions-tab";
export function CapabilityPage({ kind }: { kind: "mcp" | "quick-actions" }) {
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="p-4 sm:p-6">
        <CollapsedNavTrigger />
        <div className="mx-auto max-w-6xl">
          {kind === "mcp" ? <McpTab /> : <QuickActionsTab />}
        </div>
      </div>
    </div>
  );
}

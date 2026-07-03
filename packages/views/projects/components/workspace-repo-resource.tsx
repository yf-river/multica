"use client";

import type {
  GongfengRepoResourceRef,
  WorkspaceRepo,
} from "@multica/core/types";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";

export function isGongfengRepoURL(url: string): boolean {
  try {
    return new URL(url).hostname.toLowerCase() === "git.code.tencent.com";
  } catch {
    return false;
  }
}

function workspaceRepoDisplayName(repo: WorkspaceRepo): string {
  const projectPath = workspaceRepoProjectPath(repo);
  if (projectPath) {
    return projectPath.split("/").filter(Boolean).pop() || projectPath;
  }
  return inferRepoNameFromURL(repo.url) || repo.url;
}

function workspaceRepoProjectPath(repo: WorkspaceRepo): string {
  const projectPath = repo.project_path?.trim();
  if (projectPath) return projectPath;
  return inferProjectPathFromGongfengURL(repo.url);
}

export function workspaceRepoSearchText(repo: WorkspaceRepo): string {
  return [
    workspaceRepoDisplayName(repo),
    workspaceRepoProjectPath(repo),
    repo.url,
    repo.default_branch,
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

export function normalizeRepoSearch(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9\u4e00-\u9fa5]+/g, "");
}

export function inferProjectPathFromGongfengURL(url: string): string {
  try {
    const parsed = new URL(url);
    const parts = parsed.pathname.split("/").filter(Boolean);
    const boundary = parts.findIndex((part) =>
      ["commits", "commit", "tree", "blob", "merge_requests"].includes(part),
    );
    return (boundary > 0 ? parts.slice(0, boundary) : parts).join("/");
  } catch {
    return "";
  }
}

export function inferRepoNameFromURL(url: string): string {
  const projectPath = inferProjectPathFromGongfengURL(url);
  if (projectPath) return projectPath.split("/").filter(Boolean).pop() || "";
  return "";
}

export function buildGongfengResourceRefFromWorkspaceRepo(
  url: string,
  repo: WorkspaceRepo | undefined,
): Partial<GongfengRepoResourceRef> & { url: string } {
  const branch = repo?.default_branch?.trim();
  if (!branch) return { url };
  const headCommit = repo?.head_commit?.trim();
  const commitSHA = repo?.commit_sha?.trim() || headCommit;
  const connectionStatus = repo?.connection_status?.trim();
  const syncStatus = repo?.sync_status?.trim();
  const testStatus = repo?.test_status?.trim();
  const lastTestedAt = repo?.last_tested_at?.trim();
  const lastSyncedAt = repo?.last_synced_at?.trim();
  return {
    url,
    provider: "gongfeng",
    ...(repo?.project_path ? { project_path: repo.project_path } : {}),
    resource_kind: "branch",
    ref: branch,
    branch,
    ...(headCommit ? { head_commit: headCommit } : {}),
    ...(commitSHA ? { commit_sha: commitSHA } : {}),
    ...(connectionStatus ? { connection_status: connectionStatus } : {}),
    ...(syncStatus ? { sync_status: syncStatus } : {}),
    ...(testStatus ? { test_status: testStatus } : {}),
    ...(lastTestedAt ? { last_tested_at: lastTestedAt } : {}),
    ...(lastSyncedAt ? { last_synced_at: lastSyncedAt } : {}),
  };
}

export function WorkspaceRepoDisplayText({
  repo,
  className,
}: {
  repo: WorkspaceRepo;
  className?: string;
}) {
  const name = workspaceRepoDisplayName(repo);
  const projectPath = workspaceRepoProjectPath(repo);
  const detail = projectPath && projectPath !== name ? projectPath : repo.url;
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            title={`${name} · ${repo.url}`}
            className={cn("min-w-0 flex-1 text-left", className)}
          >
            <span className="block truncate font-medium text-foreground">
              {name}
            </span>
            <span className="block truncate text-[10px] leading-3 text-muted-foreground">
              {detail}
            </span>
          </span>
        }
      />
      <TooltipContent side="top" align="start" className="max-w-sm break-all">
        {repo.url}
      </TooltipContent>
    </Tooltip>
  );
}

export type ProjectStatus = "planned" | "in_progress" | "paused" | "completed" | "cancelled";

export type ProjectPriority = "urgent" | "high" | "medium" | "low" | "none";

export interface Project {
  id: string;
  title: string;
  description: string | null;
  icon: string | null;
  status: ProjectStatus;
  priority: ProjectPriority;
  lead_type: "member" | "agent" | null;
  lead_id: string | null;
  created_at: string;
  issue_count: number;
  done_count: number;
  resource_count: number;
}

export interface CreateProjectRequest {
  title: string;
  description?: string;
  icon?: string;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent";
  lead_id?: string;
  // Resources to attach in the same transaction as the project. Server returns
  // 4xx (and rolls back) if any one is invalid or duplicate.
  resources?: CreateProjectResourceRequest[];
}

export type UpdateProjectRequest = Partial<
  Omit<CreateProjectRequest, "resources" | "description" | "icon" | "lead_type" | "lead_id">
> & {
  description?: string | null;
  icon?: string | null;
  lead_type?: "member" | "agent" | null;
  lead_id?: string | null;
};

// ProjectResource is a typed pointer from a project to an external resource.
// The resource_ref shape depends on resource_type. New types add a case in
// validateAndNormalizeResourceRef on the server and a renderer in the UI.
//
// Known types (UI must default-case unknown server-side additions):
//   - github_repo: cloud-side git checkout, ref = { url, default_branch_hint? }
//   - gongfeng_repo: Tencent Gongfeng/GitCode repo context,
//     ref = { provider, url, project_path, resource_kind, ref? }
//   - local_directory: in-place agent execution on a specific daemon,
//     ref = { local_path, daemon_id, label? }
type ProjectResourceType =
  | "github_repo"
  | "gongfeng_repo"
  | "local_directory"
  | (string & {});

interface GithubRepoResourceRef {
  url: string;
  default_branch_hint?: string;
}

export interface LocalDirectoryResourceRef {
  local_path: string;
  daemon_id: string;
  label?: string;
}

export interface GongfengRepoResourceRef {
  provider: string;
  url: string;
  project_path: string;
  resource_kind: "project" | "branch" | "commits" | "commit" | "tag" | "file" | "merge_request";
  ref?: string;
  head_commit?: string;
  branch?: string;
  commit_sha?: string;
  connection_status?: string;
  sync_status?: string;
  test_status?: string;
  last_tested_at?: string;
  last_synced_at?: string;
  title?: string;
}

type ProjectResourceRef =
  | GithubRepoResourceRef
  | GongfengRepoResourceRef
  | LocalDirectoryResourceRef
  | Record<string, unknown>;

export interface ProjectResource {
  id: string;
  project_id: string;
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label: string | null;
}

export interface CreateProjectResourceRequest {
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label?: string;
  position?: number;
}

// resource_type is immutable server-side; partial-update payload mirrors that.
// Sending only the field(s) you want to change is fine — the server merges
// the request body with the existing row, including resource_ref shortcuts.
export type UpdateProjectResourceRequest = Partial<
  Omit<CreateProjectResourceRequest, "resource_type" | "label">
> & {
  label?: string | null;
};

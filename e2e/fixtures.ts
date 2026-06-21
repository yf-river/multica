/**
 * TestApiClient — lightweight API helper for E2E test data setup/teardown.
 *
 * Uses raw fetch so E2E tests have zero build-time coupling to the web app.
 */

import "./env";
import pg from "pg";

// `||` (not `??`) so an empty `NEXT_PUBLIC_API_URL=` in .env still falls
// back to localhost. dotenv sets unset-vs-empty both as "" — treating them
// the same matches user intent.
const API_BASE = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
const DATABASE_URL = process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

interface TestWorkspace {
  id: string;
  name: string;
  slug: string;
}

interface PromptLibraryItem {
  id: string;
  name: string;
  prompt_type: string;
}

interface PromptEvaluationAsset {
  id: string;
  prompt_id: string | null;
  name: string;
  asset_type: string;
  status: string;
  payload: Record<string, unknown>;
}

interface PromptEvaluationRun {
  id: string;
  asset_id: string;
  run_kind: string;
  status: string;
  total_cases: number;
  passed_cases: number;
  failed_cases: number;
  pass_rate: number;
  task_id: string | null;
}

export class TestApiClient {
  private token: string | null = null;
  private workspaceSlug: string | null = null;
  private workspaceId: string | null = null;
  private account: string | null = null;
  private createdIssueIds: string[] = [];

  async login(account: string, name: string) {
    const res = await fetch(`${API_BASE}/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ account, password: "e2e-password" }),
    });
    if (!res.ok) {
      throw new Error(`login failed: ${res.status}`);
    }
    const data = await res.json();

    this.token = data.token;
    this.account = account;

    if (name && data.user?.name !== name) {
      await this.authedFetch("/api/me", {
        method: "PATCH",
        body: JSON.stringify({ name }),
      });
    }

    return data;
  }

  async getWorkspaces(): Promise<TestWorkspace[]> {
    const res = await this.authedFetch("/api/workspaces");
    return res.json();
  }

  setWorkspaceId(id: string) {
    this.workspaceId = id;
  }

  setWorkspaceSlug(slug: string) {
    this.workspaceSlug = slug;
  }

  async ensureWorkspace(name = "E2E Workspace", slug = "e2e-workspace") {
    const workspaces = await this.getWorkspaces();
    const workspace = workspaces.find((item) => item.slug === slug);
    if (workspace) {
      this.workspaceId = workspace.id;
      this.workspaceSlug = workspace.slug;
      return workspace;
    }

    const res = await this.authedFetch("/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ name, slug }),
    });
    if (res.ok) {
      const created = (await res.json()) as TestWorkspace;
      this.workspaceId = created.id;
      this.workspaceSlug = created.slug;
      return created;
    }

    const refreshed = await this.getWorkspaces();
    const created = refreshed.find((item) => item.slug === slug);
    if (created) {
      this.workspaceId = created.id;
      this.workspaceSlug = created.slug;
      return created;
    }

    throw new Error(`Failed to ensure workspace ${slug}: ${res.status} ${res.statusText}`);
  }

  async markUserOnboarded() {
    if (!this.account) {
      throw new Error("Cannot mark E2E user onboarded before login");
    }

    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const result = await client.query(
        `
          UPDATE "user"
          SET
            onboarded_at = COALESCE(onboarded_at, now()),
            onboarding_questionnaire = COALESCE(onboarding_questionnaire, '{}'::jsonb)
              || '{"source":["friends_colleagues"],"source_other":null,"source_skipped":false}'::jsonb
          WHERE account = $1
        `,
        [this.account],
      );
      if (result.rowCount !== 1) {
        throw new Error(`Failed to mark E2E user onboarded: ${this.account}`);
      }
    } finally {
      await client.end();
    }
  }

  async createIssue(title: string, opts?: Record<string, unknown>) {
    const res = await this.authedFetch("/api/issues", {
      method: "POST",
      body: JSON.stringify({ title, ...opts }),
    });
    const issue = await res.json();
    this.createdIssueIds.push(issue.id);
    return issue;
  }

  async deleteIssue(id: string) {
    await this.authedFetch(`/api/issues/${id}`, { method: "DELETE" });
  }

  async listPromptLibraryItems(): Promise<PromptLibraryItem[]> {
    const res = await this.authedFetch("/api/prompt-library");
    if (!res.ok) {
      throw new Error(`list prompt library items failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async deletePromptLibraryItem(id: string) {
    await this.authedFetch(`/api/prompt-library/${id}`, { method: "DELETE" });
  }

  async listPromptEvaluationAssets(params?: { asset_type?: string; prompt_id?: string }): Promise<PromptEvaluationAsset[]> {
    const search = new URLSearchParams();
    if (params?.asset_type) search.set("asset_type", params.asset_type);
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    const res = await this.authedFetch(`/api/prompt-evaluation-assets${search.toString() ? `?${search}` : ""}`);
    if (!res.ok) {
      throw new Error(`list prompt evaluation assets failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async deletePromptEvaluationAsset(id: string) {
    await this.authedFetch(`/api/prompt-evaluation-assets/${id}`, { method: "DELETE" });
  }

  async updatePromptEvaluationAsset(id: string, data: Record<string, unknown>): Promise<PromptEvaluationAsset> {
    const res = await this.authedFetch(`/api/prompt-evaluation-assets/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    if (!res.ok) {
      throw new Error(`update prompt evaluation asset failed: ${res.status}`);
    }
    return res.json();
  }

  async listPromptEvaluationRuns(params?: { asset_id?: string; status?: string; limit?: number }): Promise<PromptEvaluationRun[]> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.status) search.set("status", params.status);
    if (params?.limit) search.set("limit", String(params.limit));
    const res = await this.authedFetch(`/api/prompt-evaluation-runs${search.toString() ? `?${search}` : ""}`);
    if (!res.ok) {
      throw new Error(`list prompt evaluation runs failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async cleanupPromptArtifactsByPrefix(prefix: string) {
    const assets = await this.listPromptEvaluationAssets();
    for (const asset of assets) {
      if (asset.name.startsWith(prefix)) {
        try {
          await this.deletePromptEvaluationAsset(asset.id);
        } catch {
          /* ignore */
        }
      }
    }
    const prompts = await this.listPromptLibraryItems();
    for (const prompt of prompts) {
      if (prompt.name.startsWith(prefix)) {
        try {
          await this.deletePromptLibraryItem(prompt.id);
        } catch {
          /* ignore */
        }
      }
    }
  }

  /** Clean up all issues created during this test. */
  async cleanup() {
    for (const id of this.createdIssueIds) {
      try {
        await this.deleteIssue(id);
      } catch {
        /* ignore — may already be deleted */
      }
    }
    this.createdIssueIds = [];
  }

  getToken() {
    return this.token;
  }

  getAccount() {
    if (!this.account) {
      throw new Error("Test API client is not logged in");
    }
    return this.account;
  }

  private async authedFetch(path: string, init?: RequestInit) {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...((init?.headers as Record<string, string>) ?? {}),
    };
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    if (this.workspaceSlug) headers["X-Workspace-Slug"] = this.workspaceSlug;
    else if (this.workspaceId) headers["X-Workspace-ID"] = this.workspaceId;
    return fetch(`${API_BASE}${path}`, { ...init, headers });
  }
}

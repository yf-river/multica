import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError } from "./client";

const CHAT_IDEMPOTENCY_KEY = "11111111-1111-4111-8111-111111111111";
const AUTOPILOT_IDEMPOTENCY_KEY = "22222222-2222-4222-8222-222222222222";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ApiClient", () => {
  it("classifies malformed JSON by whether the request may have committed", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation((_url: string, init?: RequestInit) =>
      Promise.resolve(new Response(
        init?.method === "POST" ? "<html>proxy error</html>" : "",
        { status: 200, headers: { "Content-Type": "text/html" } },
      )),
    ));
    const client = new ApiClient("https://api.example.test");

    await expect(client.listAgents()).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: false,
    });
    await expect(client.probeWorkspaceRepo("workspace-1", { url: "https://example.test/repo" }))
      .rejects.toMatchObject({
        code: "api_response_contract_invalid",
        mayHaveCommitted: false,
      });
    await expect(client.createAgent({
      name: "Agent",
      runtime_id: "runtime-1",
    })).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: true,
    });
  });

  it("classifies transport failures by whether the request may have committed", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("connection reset")));
    const client = new ApiClient("https://api.example.test");

    await expect(client.listAgents()).rejects.toMatchObject({
      name: "ApiTransportError",
      mayHaveCommitted: false,
    });
    await expect(client.createChatSession(
      { agent_id: "agent-1", title: "hello" },
      CHAT_IDEMPOTENCY_KEY,
    )).rejects.toMatchObject({
      name: "ApiTransportError",
      mayHaveCommitted: true,
    });
  });

  it("sends the required idempotency key when creating a chat session", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "session-1",
      workspace_id: "ws-1",
      agent_id: "agent-1",
      creator_id: "user-1",
    }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await client.createChatSession(
      { agent_id: "agent-1", title: "hello" },
      CHAT_IDEMPOTENCY_KEY,
    );

    expect((fetchMock.mock.calls[0]![1]?.headers as Record<string, string>)["Idempotency-Key"])
      .toBe(CHAT_IDEMPOTENCY_KEY);
  });

  it("keeps void 204 mutations successful", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    const client = new ApiClient("https://api.example.test");

    await expect(client.deleteChatSession("session-1")).resolves.toBeUndefined();
  });

  it("validates inbox and notification preference responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42 }), { status: 200, headers: { "Content-Type": "application/json" } }),
    )));
    const client = new ApiClient("https://api.example.test");
    await expect(client.listInbox()).resolves.toEqual([]);
    await expect(client.markInboxRead("inbox-1")).rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.getNotificationPreferences()).resolves.toEqual({ workspace_id: "", preferences: {} });
  });

  it("validates agent and secret env responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ cancelled: "invalid" }), { status: 200, headers: { "Content-Type": "application/json" } }),
    )));
    const client = new ApiClient("https://api.example.test");
    await expect(client.listAgents()).resolves.toEqual([]);
    await expect(client.getAgent("agent-1")).resolves.toMatchObject({ id: "", skills: [] });
    await expect(client.getAgentEnv("agent-1")).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: false,
    });
    await expect(client.updateAgentEnv("agent-1", { custom_env: { TOKEN: "new" } }))
      .rejects.toMatchObject({
        code: "api_response_contract_invalid",
        mayHaveCommitted: true,
      });
    await expect(client.cancelAgentTasks("agent-1")).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: true,
    });
  });

  it("validates task reads and never manufactures successful task mutations", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42, status: "running" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.getAgentTaskSnapshot()).resolves.toEqual([]);
    await expect(client.getIssueExecutionTree("issue-1"))
      .resolves.toMatchObject({ root: { tasks: [], children: [] }, summary: {} });
    await expect(client.cancelTask("issue-1", "task-1")).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: true,
    });
    await expect(client.rerunIssue("issue-1", "task-1")).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: true,
    });
  });

  it("rejects an agent environment response for a different agent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({
        agent_id: "agent-2",
        custom_env: { TOKEN: "secret" },
      }), { status: 200, headers: { "Content-Type": "application/json" } }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.getAgentEnv("agent-1")).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: false,
    });
    await expect(client.updateAgentEnv("agent-1", { custom_env: { TOKEN: "new" } }))
      .rejects.toMatchObject({
        code: "api_response_contract_invalid",
        mayHaveCommitted: true,
      });
  });

  it("rejects empty identifiers in a successful chat send response", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({
        message_id: "",
        task_id: "",
        created_at: "",
      }), { status: 201, headers: { "Content-Type": "application/json" } }),
    ));
    const client = new ApiClient("https://api.example.test");

    await expect(client.sendChatMessage("session-1", "hello", CHAT_IDEMPOTENCY_KEY)).rejects.toMatchObject({
      code: "api_response_contract_invalid",
      mayHaveCommitted: true,
    });
  });

  it("validates project resource read and mutation responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.listProjectResources("project-1"))
      .resolves.toMatchObject({ resources: [], total: 0 });
    await expect(client.createProjectResource("project-1", {
      resource_type: "github_repo",
      resource_ref: { url: "https://example.test/repo" },
    })).rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.updateProjectResource("project-1", "resource-1", {
      label: "Repository",
    })).rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.syncProjectResource("project-1", "resource-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid" });
  });

  it("validates Autopilot reads and never manufactures successful mutations", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42, status: "running" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.getAutopilot("autopilot-1"))
      .resolves.toMatchObject({ autopilot: { id: "" }, triggers: [] });
    await expect(client.listAutopilotRuns("autopilot-1"))
      .resolves.toMatchObject({ runs: [], total: 0 });
    await expect(client.createAutopilot({
      title: "Daily review",
      assignee_type: "agent",
      assignee_id: "agent-1",
      execution_mode: "run_only",
      trigger: { kind: "webhook" },
    })).rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.triggerAutopilot("autopilot-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.rotateAutopilotTriggerWebhookToken("autopilot-1", "trigger-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
  });

  it("validates Project and Skill reads and mutations", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.listProjects()).resolves.toMatchObject({ projects: [], total: 0 });
    await expect(client.getProject("project-1")).resolves.toMatchObject({ id: "" });
    await expect(client.listSkills()).resolves.toEqual([]);
    await expect(client.getSkill("skill-1")).resolves.toMatchObject({ id: "", files: [] });
    await expect(client.createProject({ title: "Project" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.createSkill({ name: "Skill" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
  });

  it("validates Label, Pin and Squad member reads and mutations", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.listLabels()).resolves.toMatchObject({ labels: [], total: 0 });
    await expect(client.listPins()).resolves.toEqual([]);
    await expect(client.listSquadMembers("squad-1")).resolves.toEqual([]);
    await expect(client.createLabel({ name: "bug", color: "#ff0000" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.createPin({ item_type: "issue", item_id: "issue-1" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.addSquadMember("squad-1", {
      member_type: "agent",
      member_id: "agent-1",
    })).rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
  });

  it("validates Issue utility reads and rejects empty mutation success", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.searchIssues({ q: "bug" }))
      .resolves.toEqual({ issues: [], total: 0 });
    await expect(client.getChildIssueProgress()).resolves.toEqual({ progress: [] });
    await expect(client.getAssigneeFrequency()).resolves.toEqual([]);
    await expect(client.listAttachments("issue-1")).resolves.toEqual([]);
    await expect(client.quickCreateIssue({ prompt: "Create issue", agent_id: "agent-1" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.createFeedback({ message: "Broken" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.batchUpdateIssues(["issue-1"], { status: "done" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
  });

  it("whitelists external credential responses without exposing secret fields", async () => {
    const profile = {
      id: "profile-1",
      user_id: "user-1",
      scope: "account",
      provider: "gongfeng",
      name: "Gongfeng",
      secret_binding: {
        configured: true,
        redacted: true,
        mode: "encrypted_secret",
        hint: "****abcd",
      },
      capabilities: {},
      status: "verified",
      last_verified_at: "2026-07-11T00:00:00Z",
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
      token: "must-not-cross-boundary",
      encrypted_secret: "ciphertext",
    };
    vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      const body = url.endsWith("/test")
        ? {
            provider: "gongfeng",
            secret_binding: profile.secret_binding,
            status: "verified",
            last_verified_at: "2026-07-11T00:00:00Z",
            token: "must-not-cross-boundary",
          }
        : init?.method
          ? profile
          : { profiles: [profile] };
      return Promise.resolve(new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }));
    }));
    const client = new ApiClient("https://api.example.test");

    const listed = await client.listExternalCredentialProfiles("gongfeng");
    const created = await client.createExternalCredentialProfile({
      provider: "gongfeng",
      token: "input-secret",
    });
    const updated = await client.updateExternalCredentialProfile("profile-1", {
      name: "Updated",
    });
    const tested = await client.testExternalCredentialProfile({
      provider: "gongfeng",
      token: "input-secret",
    });

    for (const value of [listed.profiles[0], created, updated, tested]) {
      expect(value).not.toHaveProperty("token");
      expect(value).not.toHaveProperty("encrypted_secret");
    }
  });

  it("validates Lark installation state without exposing installation secrets", async () => {
    const installation = {
      id: "installation-1",
      workspace_id: "workspace-1",
      agent_id: "agent-1",
      app_id: "app-1",
      bot_open_id: "bot-1",
      installer_user_id: "user-1",
      status: "active",
      region: "feishu",
      installed_at: "2026-07-11T00:00:00Z",
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
      app_secret_encrypted: "must-not-cross-boundary",
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        installations: [installation],
        configured: true,
        install_supported: true,
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        session_id: "",
        qr_code_url: "",
        expires_in_seconds: 0,
        poll_interval_seconds: 0,
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        status: "success",
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        workspace_id: "",
        installation_id: "",
        lark_open_id: "",
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    const listed = await client.listLarkInstallations("workspace-1");
    expect(listed.installations[0]).not.toHaveProperty("app_secret_encrypted");
    await expect(client.beginLarkInstall("workspace-1", "agent-1", "feishu"))
      .rejects.toMatchObject({
        code: "api_response_contract_invalid",
        mayHaveCommitted: true,
      });
    await expect(client.getLarkInstallStatus("workspace-1", "session-1"))
      .rejects.toMatchObject({
        code: "api_response_contract_invalid",
        mayHaveCommitted: false,
      });
    await expect(client.redeemLarkBindingToken("binding-token"))
      .rejects.toMatchObject({
        code: "api_response_contract_invalid",
        mayHaveCommitted: true,
      });
  });

  it("fails closed on unsafe GitHub navigation and strips installation internals", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        configured: true,
        url: "javascript:alert(1)",
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        installations: [{
          id: "installation-1",
          workspace_id: "workspace-1",
          installation_id: 42,
          account_login: "multica",
          account_type: "Organization",
          account_avatar_url: null,
          created_at: "2026-07-11T00:00:00Z",
          private_key: "must-not-cross-boundary",
        }],
        configured: true,
        can_manage: true,
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        pull_requests: [{
          id: "pr-1",
          workspace_id: "workspace-1",
          repo_owner: "ChainWeaver/ida",
          repo_name: "user-center",
          number: 61234,
          title: "Current Gongfeng merge request",
          state: "open",
          html_url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/61234",
          branch: null,
          author_login: null,
          author_avatar_url: null,
          merged_at: null,
          closed_at: null,
          pr_created_at: "",
          pr_updated_at: "",
        }],
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        pull_requests: [{
          id: "pr-unsafe",
          workspace_id: "workspace-1",
          repo_owner: "multica",
          repo_name: "multica",
          number: 1,
          title: "Unsafe",
          state: "open",
          html_url: "javascript:alert(1)",
          branch: null,
          author_login: null,
          author_avatar_url: null,
          merged_at: null,
          closed_at: null,
          pr_created_at: "",
          pr_updated_at: "",
        }],
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(client.getGitHubConnectURL("workspace-1"))
      .resolves.toEqual({ configured: false });
    const installations = await client.listGitHubInstallations("workspace-1");
    expect(installations.installations[0]).not.toHaveProperty("private_key");
    await expect(client.listIssuePullRequests("issue-1"))
      .resolves.toMatchObject({
        pull_requests: [{
          html_url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/61234",
        }],
      });
    await expect(client.listIssuePullRequests("issue-1"))
      .resolves.toEqual({ pull_requests: [] });
  });

  it("validates workspace, repository, and member responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.listWorkspaces()).resolves.toEqual([]);
    await expect(client.resolveWorkspaceRepo("workspace-1", { url: "https://example.test/repo" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.probeWorkspaceRepo("workspace-1", { url: "https://example.test/repo" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.listMembers("workspace-1")).resolves.toEqual([]);
    await expect(client.createMember("workspace-1", { account: "ada", role: "member" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid" });
  });

  it("validates auth responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ token: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.login("ada", "secret")).rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.issueCliToken()).rejects.toMatchObject({ code: "api_response_contract_invalid" });
  });

  it("validates issue, comment, and reaction write responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ id: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )));
    const client = new ApiClient("https://api.example.test");

    await expect(client.getIssue("issue-1")).resolves.toMatchObject({
      id: "",
      metadata: {},
    });
    await expect(client.createComment("issue-1", "hello")).rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.addReaction("comment-1", "👍")).rejects.toMatchObject({ code: "api_response_contract_invalid" });
    await expect(client.addIssueReaction("issue-1", "👍")).rejects.toMatchObject({ code: "api_response_contract_invalid" });
  });

  it("validates runtime profile lists instead of trusting typed JSON", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ runtime_profiles: [{ id: 42 }] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(client.listRuntimeProfiles("workspace-1")).resolves.toEqual([]);
  });

  it("fails closed on malformed runtime mutations and async request states", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ));
    const client = new ApiClient("https://api.example.test");

    await expect(client.listRuntimes()).resolves.toEqual([]);
    await expect(client.archiveAgentsAndDeleteRuntime("runtime-1", []))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.updateRuntime("runtime-1", { scope: "workspace" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.initiateListModels("runtime-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.getListModelsResult("runtime-1", "request-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: false });
    await expect(client.initiateListLocalSkills("runtime-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.getListLocalSkillsResult("runtime-1", "request-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: false });
    await expect(client.initiateImportLocalSkill("runtime-1", { skill_key: "skill-1" }))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: true });
    await expect(client.getImportLocalSkillResult("runtime-1", "request-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: false });
  });

  it("binds runtime async responses to both runtime and request identities", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({
        id: "request-other",
        runtime_id: "runtime-other",
        status: "pending",
        supported: true,
        created_at: "2026-07-11T00:00:00Z",
        updated_at: "2026-07-11T00:00:00Z",
      }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ));
    const client = new ApiClient("https://api.example.test");

    await expect(client.getListModelsResult("runtime-1", "request-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: false });
    await expect(client.getListLocalSkillsResult("runtime-1", "request-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: false });
    await expect(client.getImportLocalSkillResult("runtime-1", "request-1"))
      .rejects.toMatchObject({ code: "api_response_contract_invalid", mayHaveCommitted: false });
  });

  it("preserves HTTP status on failed requests", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "workspace slug already exists" }), {
          status: 409,
          statusText: "Conflict",
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const client = new ApiClient("https://api.example.test");

    try {
      await client.createWorkspace({ name: "Test", slug: "test" });
      throw new Error("expected createWorkspace to fail");
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({
        message: "workspace slug already exists",
        status: 409,
        statusText: "Conflict",
      });
    }
  });

  it("uses the expected HTTP contract for autopilot endpoints", async () => {
    const autopilot = {
      id: "ap-1",
      workspace_id: "workspace-1",
      title: "Daily triage",
      description: null,
      project_id: null,
      assignee_type: "agent",
      assignee_id: "agent-1",
      status: "active",
      execution_mode: "create_issue",
      issue_title_template: null,
      created_by_type: "member",
      created_by_id: "user-1",
      last_run_at: null,
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
      subscribers: [],
    };
    const trigger = {
      id: "tr-1",
      autopilot_id: "ap-1",
      kind: "schedule",
      enabled: true,
      cron_expression: "0 9 * * *",
      timezone: "UTC",
      next_run_at: null,
      webhook_token: null,
      label: null,
      last_fired_at: null,
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
    };
    const run = {
      id: "run-1",
      autopilot_id: "ap-1",
      trigger_id: null,
      source: "manual",
      status: "running",
      issue_id: null,
      task_id: "task-1",
      triggered_at: "2026-07-11T00:00:00Z",
      completed_at: null,
      failure_reason: null,
      trigger_payload: null,
      result: null,
      created_at: "2026-07-11T00:00:00Z",
    };
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      let body: unknown = autopilot;
      if (url.includes("/runs?")) body = { runs: [], total: 0 };
      else if (url.endsWith("/trigger")) body = run;
      else if (url.includes("/triggers")) body = trigger;
      else if (url.endsWith("/api/autopilots?status=active")) body = { autopilots: [], total: 0 };
      else if (url.endsWith("/api/autopilots") && init?.method === "POST") {
        body = { ...autopilot, initial_trigger: trigger };
      }
      else if (url.endsWith("/api/autopilots/ap-1") && (init?.method ?? "GET") === "GET") {
        body = { autopilot, triggers: [] };
      }
      return Promise.resolve(new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }));
    });
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");

    await client.listAutopilots({ status: "active" });
    await client.getAutopilot("ap-1");
    await client.createAutopilot({
      title: "Daily triage",
      project_id: "project-1",
      assignee_type: "agent",
      assignee_id: "agent-1",
      execution_mode: "create_issue",
      trigger: {
        kind: "schedule",
        cron_expression: "0 9 * * *",
        timezone: "UTC",
      },
    });
    await client.updateAutopilot("ap-1", { status: "paused", project_id: null });
    await client.deleteAutopilot("ap-1");
    await client.triggerAutopilot("ap-1", AUTOPILOT_IDEMPOTENCY_KEY);
    await client.listAutopilotRuns("ap-1", { limit: 10, offset: 20 });
    await client.createAutopilotTrigger("ap-1", {
      kind: "schedule",
      cron_expression: "0 9 * * *",
      timezone: "UTC",
    });
    await client.updateAutopilotTrigger("ap-1", "tr-1", { enabled: false });
    await client.deleteAutopilotTrigger("ap-1", "tr-1");
    await client.rotateAutopilotTriggerWebhookToken("ap-1", "tr-1");

    const calls = fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
      headers: init?.headers,
    }));

    expect(calls).toMatchObject([
      { url: "https://api.example.test/api/autopilots?status=active", method: "GET" },
      { url: "https://api.example.test/api/autopilots/ap-1", method: "GET" },
      {
        url: "https://api.example.test/api/autopilots",
        method: "POST",
        body: JSON.stringify({
          title: "Daily triage",
          project_id: "project-1",
          assignee_type: "agent",
          assignee_id: "agent-1",
          execution_mode: "create_issue",
          trigger: {
            kind: "schedule",
            cron_expression: "0 9 * * *",
            timezone: "UTC",
          },
        }),
        headers: expect.objectContaining({
          "Idempotency-Key": expect.any(String),
        }),
      },
      {
        url: "https://api.example.test/api/autopilots/ap-1",
        method: "PATCH",
        body: JSON.stringify({ status: "paused", project_id: null }),
      },
      { url: "https://api.example.test/api/autopilots/ap-1", method: "DELETE" },
      {
        url: "https://api.example.test/api/autopilots/ap-1/trigger",
        method: "POST",
        headers: expect.objectContaining({
          "Idempotency-Key": AUTOPILOT_IDEMPOTENCY_KEY,
        }),
      },
      { url: "https://api.example.test/api/autopilots/ap-1/runs?limit=10&offset=20", method: "GET" },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers",
        method: "POST",
        body: JSON.stringify({
          kind: "schedule",
          cron_expression: "0 9 * * *",
          timezone: "UTC",
        }),
      },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1",
        method: "PATCH",
        body: JSON.stringify({ enabled: false }),
      },
      { url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1", method: "DELETE" },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1/rotate-webhook-token",
        method: "POST",
      },
    ]);
  });

  it("retries an unknown autopilot trigger outcome with the same key", async () => {
    const run = {
      id: "run-1",
      autopilot_id: "ap-1",
      trigger_id: null,
      source: "manual",
      status: "running",
      issue_id: null,
      task_id: "task-1",
      triggered_at: "2026-07-11T00:00:00Z",
      completed_at: null,
      failure_reason: null,
      trigger_payload: null,
      result: null,
      created_at: "2026-07-11T00:00:00Z",
    };
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("response connection reset"))
      .mockResolvedValueOnce(
        new Response(JSON.stringify(run), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(
      client.triggerAutopilot("ap-1", AUTOPILOT_IDEMPOTENCY_KEY),
    ).resolves.toMatchObject({ id: "run-1", task_id: "task-1" });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const [, init] of fetchMock.mock.calls) {
      expect((init?.headers as Record<string, string>)["Idempotency-Key"])
        .toBe(AUTOPILOT_IDEMPOTENCY_KEY);
    }
  });

  it("retries an unknown autopilot create outcome with the same key", async () => {
    const response = {
      id: "ap-1",
      workspace_id: "workspace-1",
      title: "Daily triage",
      description: null,
      project_id: null,
      assignee_type: "agent",
      assignee_id: "agent-1",
      status: "active",
      execution_mode: "run_only",
      issue_title_template: null,
      created_by_type: "member",
      created_by_id: "user-1",
      last_run_at: null,
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
      subscribers: [],
      initial_trigger: {
        id: "trigger-1",
        autopilot_id: "ap-1",
        kind: "webhook",
        enabled: true,
        cron_expression: null,
        timezone: null,
        next_run_at: null,
        webhook_token: "awt_test",
        label: null,
        last_fired_at: null,
        created_at: "2026-07-11T00:00:00Z",
        updated_at: "2026-07-11T00:00:00Z",
      },
    };
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("response connection reset"))
      .mockResolvedValueOnce(
        new Response(JSON.stringify(response), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(client.createAutopilot({
      title: "Daily triage",
      assignee_type: "agent",
      assignee_id: "agent-1",
      execution_mode: "run_only",
      trigger: { kind: "webhook" },
    }, AUTOPILOT_IDEMPOTENCY_KEY)).resolves.toMatchObject({
      id: "ap-1",
      initial_trigger: { id: "trigger-1" },
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const [, init] of fetchMock.mock.calls) {
      expect((init?.headers as Record<string, string>)["Idempotency-Key"])
        .toBe(AUTOPILOT_IDEMPOTENCY_KEY);
    }
  });

  it("sends offset when listing prompt evaluation runs", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ items: [], total: 0 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.listPromptEvaluationRuns({
      since: "2026-07-01T00:00:00.000Z",
      limit: 200,
      offset: 400,
    });

    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/prompt-evaluation-runs?since=2026-07-01T00%3A00%3A00.000Z&limit=200&offset=400",
      expect.any(Object),
    );
  });

  it("emits X-Client-* headers when identity is configured", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test", {
      identity: { platform: "desktop", version: "1.2.3", os: "macos" },
    });
    await client.listWorkspaces();

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Client-Platform"]).toBe("desktop");
    expect(headers["X-Client-Version"]).toBe("1.2.3");
    expect(headers["X-Client-OS"]).toBe("macos");
  });

  it("omits X-Client-* headers when identity is not configured", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.listWorkspaces();

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Client-Platform"]).toBeUndefined();
    expect(headers["X-Client-Version"]).toBeUndefined();
    expect(headers["X-Client-OS"]).toBeUndefined();
  });

  it("uses the expected HTTP contract for comment trigger preview and suppress", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ agents: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({
          id: "comment-1",
          issue_id: "issue-1",
          author_type: "member",
          author_id: "user-1",
          content: "hello",
          type: "comment",
          parent_id: null,
          reactions: [],
          attachments: [],
          created_at: "2026-06-05T00:00:00Z",
          updated_at: "2026-06-05T00:00:00Z",
        }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({
          id: "comment-1",
          issue_id: "issue-1",
          author_type: "member",
          author_id: "user-1",
          content: "updated",
          type: "comment",
          parent_id: null,
          reactions: [],
          attachments: [],
          created_at: "2026-06-05T00:00:00Z",
          updated_at: "2026-06-05T00:01:00Z",
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.previewCommentTriggers("issue-1", "hello", "parent-1", "comment-1");
    await client.createComment(
      "issue-1",
      "hello",
      "comment",
      "parent-1",
      ["attachment-1"],
      ["agent-1"],
    );
    await client.updateComment("comment-1", "updated", ["attachment-1"], ["agent-1"]);

    expect(fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method,
      body: init?.body,
    }))).toMatchObject([
      {
        url: "https://api.example.test/api/issues/issue-1/comments/trigger-preview",
        method: "POST",
        body: JSON.stringify({ content: "hello", parent_id: "parent-1", editing_comment_id: "comment-1" }),
      },
      {
        url: "https://api.example.test/api/issues/issue-1/comments",
        method: "POST",
        body: JSON.stringify({
          content: "hello",
          type: "comment",
          parent_id: "parent-1",
          attachment_ids: ["attachment-1"],
          suppress_agent_ids: ["agent-1"],
        }),
      },
      {
        url: "https://api.example.test/api/comments/comment-1",
        method: "PUT",
        body: JSON.stringify({
          content: "updated",
          attachment_ids: ["attachment-1"],
          suppress_agent_ids: ["agent-1"],
        }),
      },
    ]);
  });

  describe("getAttachment", () => {
    it("returns the parsed attachment for a well-formed response", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(
            JSON.stringify({
              id: "att-1",
              workspace_id: "ws-1",
              issue_id: null,
              comment_id: null,
              uploader_type: "member",
              uploader_id: "u-1",
              filename: "report.md",
              url: "https://static.example.test/ws/att-1.md",
              download_url:
                "https://static.example.test/ws/att-1.md?Policy=p&Signature=s&Key-Pair-Id=k",
              content_type: "text/markdown",
              size_bytes: 123,
              created_at: "2026-05-11T00:00:00Z",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const att = await client.getAttachment("att-1");

      expect(att.id).toBe("att-1");
      expect(att.download_url).toContain("Policy=");
    });

    it("falls back to an empty attachment when the response is missing download_url", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({ id: "att-1" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const att = await client.getAttachment("att-1");

      // parseWithFallback returns the EMPTY_ATTACHMENT record so callers can
      // safely read `download_url` without crashing — they'll see "" and
      // surface a user-facing error instead of opening `undefined`.
      expect(att.id).toBe("");
      expect(att.download_url).toBe("");
    });
  });

  describe("getAttachmentTextContent", () => {
    it("returns body text and the original content type from the X-* header", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response("# heading\n\nbody\n", {
            status: 200,
            headers: {
              "Content-Type": "text/plain; charset=utf-8",
              "X-Original-Content-Type": "text/markdown",
            },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const { text, originalContentType } =
        await client.getAttachmentTextContent("att-1");

      expect(text).toBe("# heading\n\nbody\n");
      expect(originalContentType).toBe("text/markdown");
    });

    it("throws PreviewTooLargeError on 413", async () => {
      const { PreviewTooLargeError } = await import("./client");
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response("", { status: 413, statusText: "Payload Too Large" }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      await expect(client.getAttachmentTextContent("att-1")).rejects.toBeInstanceOf(
        PreviewTooLargeError,
      );
    });

    it("throws PreviewUnsupportedError on 415", async () => {
      const { PreviewUnsupportedError } = await import("./client");
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response("", { status: 415, statusText: "Unsupported Media Type" }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      await expect(client.getAttachmentTextContent("att-1")).rejects.toBeInstanceOf(
        PreviewUnsupportedError,
      );
    });
  });

  describe("listChatMessagesPage current contract", () => {
    const jsonResponse = (body: unknown, status: number, statusText = "") =>
      new Response(JSON.stringify(body), {
        status,
        statusText,
        headers: { "Content-Type": "application/json" },
      });

    it("uses only the paged endpoint and falls back on a malformed body", async () => {
      const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ messages: "invalid" }, 200));
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const page = await client.listChatMessagesPage("session-1", { limit: 50 });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock.mock.calls[0]![0]).toBe(
        "https://api.example.test/api/chat/sessions/session-1/messages/page?limit=50",
      );
      expect(page).toEqual({ messages: [], limit: 50, has_more: false, next_cursor: null });
    });

    it("propagates a missing current paged route without legacy fallback", async () => {
      const fetchMock = vi
        .fn()
        .mockResolvedValue(jsonResponse({ error: "not found" }, 404, "Not Found"));
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await expect(client.listChatMessagesPage("session-1")).rejects.toBeInstanceOf(ApiError);
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
  });

  describe("cancelTaskById response parsing", () => {
    const taskResponse = {
      id: "task-1",
      agent_id: "agent-1",
      runtime_id: "runtime-1",
      issue_id: "",
      status: "cancelled",
      priority: 0,
      dispatched_at: null,
      started_at: null,
      completed_at: "2026-06-12T06:40:00Z",
      result: null,
      error: null,
      created_at: "2026-06-12T06:39:00Z",
    };

    it("parses the cancelled chat message payload", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({
          ...taskResponse,
          cancelled_chat_message: {
            chat_session_id: "session-1",
            message_id: "message-1",
            content: "restore me",
            restore_to_input: true,
          },
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const result = await client.cancelTaskById("task-1");

      expect(fetchMock.mock.calls[0]).toMatchObject([
        "https://api.example.test/api/tasks/task-1/cancel",
        { method: "POST" },
      ]);
      expect(result.cancelled_chat_message).toEqual({
        chat_session_id: "session-1",
        message_id: "message-1",
        content: "restore me",
        restore_to_input: true,
      });
    });

    it("treats a null cancelled chat message as absent", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({
            ...taskResponse,
            cancelled_chat_message: null,
          }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const result = await client.cancelTaskById("task-1");

      expect(result.id).toBe("task-1");
      expect(result.cancelled_chat_message).toBeUndefined();
    });

    it.each([
      ["a missing task id", { ...taskResponse, id: undefined }],
      [
        "a malformed cancelled chat message",
        {
          ...taskResponse,
          cancelled_chat_message: {
            chat_session_id: "session-1",
            message_id: "message-1",
            content: "restore me",
            restore_to_input: "true",
          },
        },
      ],
      ["a null body", null],
    ])("rejects a malformed successful response for %s", async (_label, body) => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify(body), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      await expect(client.cancelTaskById("task-1")).rejects.toMatchObject({
        code: "api_response_contract_invalid",
        mayHaveCommitted: true,
      });
    });
  });

  describe("chat attachment wiring", () => {
    it("uploadFile includes chat_session_id in the FormData body", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({
          id: "att-1",
          url: "https://cdn/x",
          download_url: "https://cdn/x?download=1",
          filename: "hi.png",
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const file = new File(["hi"], "hi.png", { type: "image/png" });
      await client.uploadFile(file, { chatSessionId: "session-123" });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      const [url, init] = fetchMock.mock.calls[0]!;
      expect(url).toBe("https://api.example.test/api/upload-file");
      expect(init?.method).toBe("POST");
      const body = init?.body as FormData;
      expect(body).toBeInstanceOf(FormData);
      expect(body.get("chat_session_id")).toBe("session-123");
      expect(body.get("issue_id")).toBeNull();
      expect(body.get("comment_id")).toBeNull();
    });

    it("sendChatMessage serialises attachment_ids onto the JSON body when present", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({
          message_id: "m1",
          task_id: "t1",
          created_at: "2026-07-11T00:00:00Z",
        }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await client.sendChatMessage("session-1", "hello", CHAT_IDEMPOTENCY_KEY, ["att-1", "att-2"]);

      const [, init] = fetchMock.mock.calls[0]!;
      expect(JSON.parse(init?.body as string)).toEqual({
        content: "hello",
        attachment_ids: ["att-1", "att-2"],
      });
      expect((init?.headers as Record<string, string>)["Idempotency-Key"])
        .toBe(CHAT_IDEMPOTENCY_KEY);
    });

    it("sendChatMessage omits attachment_ids when the list is empty or undefined", async () => {
      const fetchMock = vi.fn().mockImplementation(() =>
        Promise.resolve(
          new Response(JSON.stringify({
            message_id: "m1",
            task_id: "t1",
            created_at: "2026-07-11T00:00:00Z",
          }), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await client.sendChatMessage("session-1", "hello", CHAT_IDEMPOTENCY_KEY);
      await client.sendChatMessage(
        "session-1",
        "again",
        "22222222-2222-4222-8222-222222222222",
        [],
      );

      expect(JSON.parse(fetchMock.mock.calls[0]![1]?.body as string)).toEqual({ content: "hello" });
      expect(JSON.parse(fetchMock.mock.calls[1]![1]?.body as string)).toEqual({ content: "again" });
    });
  });
});

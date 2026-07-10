// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import common from "../../../locales/zh-Hans/common.json";
import agents from "../../../locales/zh-Hans/agents.json";

const apiMock = vi.hoisted(() => ({
  getAgentEnv: vi.fn(),
  updateAgentEnv: vi.fn(),
}));

const toastMock = vi.hoisted(() => ({
  error: vi.fn(),
  success: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("sonner", () => ({ toast: toastMock }));

import { EnvTab } from "./env-tab";

const agent: Agent = {
  id: "agent-1",
  workspace_id: "workspace-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  custom_env_key_count: 1,
  scope: "workspace",
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-07-11T00:00:00Z",
  updated_at: "2026-07-11T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function renderEnvTab(props: { onSaved?: () => void } = {}) {
  return render(
    <I18nProvider
      locale="zh-Hans"
      resources={{ "zh-Hans": { common, agents } }}
    >
      <EnvTab agent={agent} {...props} />
    </I18nProvider>,
  );
}

describe("EnvTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("keeps values locked when the reveal request fails", async () => {
    apiMock.getAgentEnv.mockRejectedValueOnce(new Error("响应格式异常"));
    const user = userEvent.setup();
    renderEnvTab();

    await user.click(screen.getByRole("button", { name: "解锁并编辑" }));

    await waitFor(() => expect(apiMock.getAgentEnv).toHaveBeenCalledWith("agent-1"));
    expect(screen.getByRole("button", { name: "解锁并编辑" })).toBeTruthy();
    expect(screen.queryByPlaceholderText("KEY")).toBeNull();
    expect(toastMock.error).toHaveBeenCalledWith("响应格式异常");
  });

  it("preserves the edited draft when saving fails", async () => {
    apiMock.getAgentEnv.mockResolvedValueOnce({
      agent_id: "agent-1",
      custom_env: { TOKEN: "old-value" },
    });
    apiMock.updateAgentEnv.mockRejectedValueOnce(new Error("保存结果无法确认"));
    const onSaved = vi.fn();
    const user = userEvent.setup();
    renderEnvTab({ onSaved });

    await user.click(screen.getByRole("button", { name: "解锁并编辑" }));
    const valueInput = await screen.findByDisplayValue("old-value");
    await user.clear(valueInput);
    await user.type(valueInput, "new-value");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(apiMock.updateAgentEnv).toHaveBeenCalledWith(
      "agent-1",
      { custom_env: { TOKEN: "new-value" } },
    ));
    expect(screen.getByDisplayValue("new-value")).toBeTruthy();
    expect(onSaved).not.toHaveBeenCalled();
    expect(toastMock.success).not.toHaveBeenCalled();
    expect(toastMock.error).toHaveBeenCalledWith("保存结果无法确认");
  });
});

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent } from "@multica/core/types";
import enChat from "../../locales/zh-Hans/chat.json";
import enIssues from "../../locales/zh-Hans/issues.json";

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => (
    <span data-testid={`avatar-${actorId}`} />
  ),
}));

import { AgentDropdown, getVisibleChatAgents } from "./chat-window";

const TEST_RESOURCES = { "zh-Hans": { chat: enChat, issues: enIssues } };

function makeAgent(overrides: Partial<Agent> & Pick<Agent, "id" | "name" | "owner_id">): Agent {
  return {
    workspace_id: "ws-1",
    runtime_id: "runtime-1",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "workspace",
    status: "idle",
    max_concurrent_tasks: 1,
    model: "sonnet",
    skills: [],
    created_at: new Date(0).toISOString(),
    updated_at: new Date(0).toISOString(),
    archived_at: null,
    archived_by: null,
    ...overrides,
    id: overrides.id,
    name: overrides.name,
    owner_id: overrides.owner_id,
  };
}

const agents = [
  makeAgent({ id: "mine-alpha", name: "Alpha", owner_id: "user-1" }),
  makeAgent({ id: "mine-zhang", name: "张三", owner_id: "user-1" }),
  makeAgent({ id: "other-beta", name: "Beta", owner_id: "user-2" }),
  makeAgent({ id: "other-gamma", name: "Gamma", owner_id: "user-2" }),
];

function renderDropdown(onSelect = vi.fn()) {
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <AgentDropdown
        agents={agents}
        activeAgent={agents[0]!}
        userId="user-1"
        onSelect={onSelect}
      />
    </I18nProvider>,
  );
  fireEvent.click(screen.getByText("Alpha"));
  return { onSelect };
}

describe("AgentDropdown", () => {
  it("聊天可选智能体默认排除验收造数", () => {
    const visible = getVisibleChatAgents(
      [
        ...agents,
        makeAgent({
          id: "fixture-curl-codex",
          name: "curl Codex 验收 Agent 1782145202049",
          owner_id: "user-1",
          description: "端到端验收造数",
        }),
        makeAgent({
          id: "multica-coding",
          name: "Multica 训练评估智能体",
          owner_id: "user-1",
          description: "正式内置智能体",
        }),
      ],
      "user-1",
      "owner",
    );

    expect(visible.map((agent) => agent.id)).not.toContain("fixture-curl-codex");
    expect(visible.map((agent) => agent.id)).toContain("multica-coding");
  });

  it("从聊天输入区向上打开共享选择器", async () => {
    renderDropdown();

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveAttribute("data-side", "top");
  });

  it("按智能体名称同时过滤我的智能体和其他分组", async () => {
    renderDropdown();

    const input = await screen.findByRole("textbox", { name: "筛选选项" });
    fireEvent.change(input, { target: { value: "ta" } });
    const dialog = screen.getByRole("dialog");

    expect(within(dialog).queryByText("Alpha")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("张三")).not.toBeInTheDocument();
    expect(within(dialog).getByText("Beta")).toBeInTheDocument();
    expect(within(dialog).queryByText("Gamma")).not.toBeInTheDocument();
    expect(within(dialog).getByText("其他")).toBeInTheDocument();
  });

  it("通过拼音匹配我的智能体", async () => {
    renderDropdown();

    const input = await screen.findByRole("textbox", { name: "筛选选项" });
    fireEvent.change(input, { target: { value: "zhang" } });
    const dialog = screen.getByRole("dialog");

    expect(within(dialog).getByText("张三")).toBeInTheDocument();
    expect(within(dialog).getByText("我的智能体")).toBeInTheDocument();
    expect(within(dialog).queryByText("Alpha")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Beta")).not.toBeInTheDocument();
  });

  it("没有智能体匹配时显示共享空态", async () => {
    renderDropdown();

    const input = await screen.findByRole("textbox", { name: "筛选选项" });
    fireEvent.change(input, { target: { value: "missing" } });

    expect(screen.getByText("无结果")).toBeInTheDocument();
    expect(screen.queryByText("我的智能体")).not.toBeInTheDocument();
    expect(screen.queryByText("其他")).not.toBeInTheDocument();
  });

  it("智能体选择器行左对齐", async () => {
    renderDropdown();

    const dialog = await screen.findByRole("dialog");
    const alphaRow = Array.from(
      dialog.querySelectorAll<HTMLButtonElement>("button[data-picker-item]"),
    ).find((row) => row.textContent?.includes("Alpha"));

    expect(alphaRow).toBeDefined();
    expect(alphaRow).toHaveClass("text-left");
  });

  it("保留当前智能体标记，并可选择另一个智能体", async () => {
    const { onSelect } = renderDropdown();

    const dialog = screen.getByRole("dialog");
    const alphaRow = within(dialog).getByText("Alpha").closest("button");
    expect(alphaRow).not.toBeNull();
    expect(alphaRow!.querySelector("svg:not(.invisible)")).not.toBeNull();

    fireEvent.click(within(dialog).getByText("Beta"));

    expect(onSelect).toHaveBeenCalledWith(agents[2]);
    await waitFor(() => {
      expect(screen.queryByRole("textbox", { name: "筛选选项" })).not.toBeInTheDocument();
    });
  });
});

// @vitest-environment jsdom

import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Agent, PromptLibraryItem, PromptLibraryTrial, PromptLibraryVersion } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { PromptTrialPanel, PromptVersionHistory } from "./prompt-editor-panels";

const prompt = {
  id: "prompt-1",
  name: "登录排查",
  description: "",
  content: "分析 {{issue}}",
  version: 5,
} satisfies PromptLibraryItem;

function version(number: number): PromptLibraryVersion {
  return {
    id: `version-${number}`,
    version: number,
    name: prompt.name,
    description: "",
    content: `版本 ${number} 内容`,
    source_candidate_id: null,
    change_note: "",
    created_at: `2026-07-0${number}T00:00:00Z`,
  };
}

function trial(number: number): PromptLibraryTrial {
  return {
    id: `trial-${number}`,
    agent_id: number === 1 ? "agent-1" : "missing-agent",
    variables: number === 1 ? { issue: "登录失败" } : {},
    status: "completed",
    output_preview: `结果 ${number}`,
    created_at: `2026-07-${String(number).padStart(2, "0")}T00:00:00Z`,
  };
}

describe("PromptVersionHistory", () => {
  it("shows the unsaved contract before the first immutable version exists", () => {
    renderWithI18n(
      <PromptVersionHistory selected={null} versions={[]} activeVersionId={null} onSelectVersion={vi.fn()} loading={false} />,
    );

    expect(screen.getByText("保存后会生成第一个不可变版本记录。")).toBeInTheDocument();
  });

  it("shows at most four versions and reports the selected version id", () => {
    const onSelectVersion = vi.fn();
    renderWithI18n(
      <PromptVersionHistory
        selected={prompt}
        versions={[version(5), version(4), version(3), version(2), version(1)]}
        activeVersionId="version-5"
        onSelectVersion={onSelectVersion}
        loading={false}
      />,
    );

    expect(screen.getAllByRole("button")).toHaveLength(4);
    expect(screen.queryByText("版本 1")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /版本 4/ }));
    expect(onSelectVersion).toHaveBeenCalledWith("version-4");
  });
});

describe("PromptTrialPanel", () => {
  const agent = { id: "agent-1", name: "排障 Agent" } as Agent;
  const activeVersion = version(5);

  it("requires every template variable and forwards variable changes", () => {
    const onVariablesChange = vi.fn();
    const { rerender } = renderWithI18n(
      <PromptTrialPanel
        selected={prompt}
        activeVersion={activeVersion}
        agents={[agent]}
        agentsLoading={false}
        selectedAgentId={agent.id}
        onSelectedAgentIdChange={vi.fn()}
        variableNames={["issue"]}
        variables={{ issue: "" }}
        onVariablesChange={onVariablesChange}
        trials={[]}
        trialsLoading={false}
        running={false}
        onRun={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: "试跑" })).toBeDisabled();
    fireEvent.change(screen.getByRole("textbox", { name: "变量：issue" }), { target: { value: "登录失败" } });
    const update = onVariablesChange.mock.calls[0]?.[0] as (current: Record<string, string>) => Record<string, string>;
    expect(update({ issue: "" })).toEqual({ issue: "登录失败" });

    rerender(
      <PromptTrialPanel
        selected={prompt}
        activeVersion={activeVersion}
        agents={[agent]}
        agentsLoading={false}
        selectedAgentId={agent.id}
        onSelectedAgentIdChange={vi.fn()}
        variableNames={["issue"]}
        variables={{ issue: "登录失败" }}
        onVariablesChange={onVariablesChange}
        trials={[]}
        trialsLoading={false}
        running={false}
        onRun={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: "试跑" })).toBeEnabled();
  });

  it("shows at most five recent trials and maps known agent names", () => {
    renderWithI18n(
      <PromptTrialPanel
        selected={prompt}
        activeVersion={activeVersion}
        agents={[agent]}
        agentsLoading={false}
        selectedAgentId={agent.id}
        onSelectedAgentIdChange={vi.fn()}
        variableNames={[]}
        variables={{}}
        onVariablesChange={vi.fn()}
        trials={[trial(1), trial(2), trial(3), trial(4), trial(5), trial(6)]}
        trialsLoading={false}
        running={false}
        onRun={vi.fn()}
      />,
    );

    expect(screen.getAllByText("排障 Agent")).toHaveLength(2);
    expect(screen.getAllByText("未知 Agent")).toHaveLength(4);
    expect(screen.getByText("变量：issue=登录失败")).toBeInTheDocument();
    expect(screen.queryByText("结果 6")).not.toBeInTheDocument();
  });
});

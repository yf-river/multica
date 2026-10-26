import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import common from "../locales/zh-Hans/common.json";

const mocks = vi.hoisted(() => ({
  query: vi.fn(),
  mutation: vi.fn(),
  submit: vi.fn(),
  retry: vi.fn(),
}));
vi.mock("@tanstack/react-query", () => ({
  useQuery: mocks.query,
  useMutation: mocks.mutation,
  useQueryClient: () => ({}),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));
vi.mock("@multica/core/runtimes/interactions", () => ({
  taskInteractionOptions: () => ({}),
  respondTaskInteractionOptions: () => ({}),
}));
import { TaskInteractionPanel } from "./task-interaction-panel";

function show() {
  return render(
    <I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { common } }}>
      <TaskInteractionPanel taskId="task" active />
    </I18nProvider>,
  );
}
const prompt = {
  kind: "question",
  title: "Choose scope",
  questions: [
    {
      id: "scope",
      question: "Which scope?",
      options: [{ label: "Current workspace" }, { label: "All workspaces" }],
      multiSelect: false,
    },
  ],
};
beforeEach(() => {
  vi.clearAllMocks();
  mocks.query.mockReturnValue({
    data: {
      available: true,
      interactions: [
        {
          id: "request",
          taskId: "task",
          provider: "codex",
          status: "pending",
          prompt,
        },
      ],
    },
    refetch: mocks.retry,
  });
  mocks.mutation.mockReturnValue({
    isPending: false,
    isError: false,
    mutate: mocks.submit,
  });
});
describe("runtime interaction", () => {
  it("requires an explicit answer and preserves a typed alternative across refresh renders", () => {
    const view = show();
    expect(
      screen.getByRole("button", { name: common.runtime_interaction.submit }),
    ).toBeDisabled();
    fireEvent.click(screen.getByRole("radio", { name: "Current workspace" }));
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "Only selected modules" },
    });
    view.rerender(
      <I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { common } }}>
        <TaskInteractionPanel taskId="task" active />
      </I18nProvider>,
    );
    expect(
      screen.getByRole("radio", { name: "Current workspace" }),
    ).not.toBeChecked();
    fireEvent.click(
      screen.getByRole("button", { name: common.runtime_interaction.submit }),
    );
    expect(mocks.submit).toHaveBeenCalledWith({
      id: "request",
      answer: {
        decision: "allow",
        answers: { scope: ["Only selected modules"] },
      },
    });
  });
  it("allows denial without filling questions", () => {
    show();
    fireEvent.click(
      screen.getByRole("button", { name: common.runtime_interaction.deny }),
    );
    expect(mocks.submit).toHaveBeenCalledWith({
      id: "request",
      answer: { decision: "deny" },
    });
  });
  it("reports unavailable data and lets the user retry", () => {
    mocks.query.mockReturnValue({
      data: { available: false, interactions: [] },
      refetch: mocks.retry,
    });
    show();
    expect(screen.getByRole("alert")).toHaveTextContent(
      common.runtime_interaction.load_error,
    );
    fireEvent.click(
      screen.getByRole("button", { name: common.runtime_interaction.retry }),
    );
    expect(mocks.retry).toHaveBeenCalledOnce();
  });
  it("renders persisted answers as saved and expired requests without approval controls", () => {
    mocks.query.mockReturnValue({
      data: {
        available: true,
        interactions: [
          {
            id: "one",
            taskId: "task",
            status: "responded",
            prompt,
            response: {
              decision: "allow",
              answers: { scope: ["Existing answer"] },
            },
          },
          { id: "two", taskId: "task", status: "expired", prompt },
        ],
      },
    });
    show();
    fireEvent.click(screen.getByText(common.runtime_interaction.history));
    expect(screen.getByText("Existing answer")).toBeVisible();
    expect(screen.getByText(common.runtime_interaction.saved)).toBeVisible();
    expect(screen.getByText(common.runtime_interaction.expired)).toBeVisible();
    expect(
      screen.queryByRole("button", { name: common.runtime_interaction.allow }),
    ).not.toBeInTheDocument();
  });
});

it("renders the full plan after process exit and requires an explicit decision", () => {
  mocks.query.mockReturnValue({
    data: {
      available: true,
      interactions: [
        {
          id: "plan",
          taskId: "task",
          provider: "dsh",
          status: "pending",
          deliveryMode: "suspended",
          prompt: {
            kind: "plan",
            title: "Release plan",
            planMarkdown:
              "# Release plan\n\nRead first. **Wait for approval.**",
            questions: [],
          },
        },
      ],
    },
  });
  render(
    <I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { common } }}>
      <TaskInteractionPanel taskId="task" active={false} />
    </I18nProvider>,
  );
  expect(screen.getByText("Wait for approval.")).toBeInTheDocument();
  expect(mocks.submit).not.toHaveBeenCalled();
  fireEvent.change(
    screen.getByRole("textbox", {
      name: common.runtime_interaction.plan_feedback,
    }),
    { target: { value: "Add rollback." } },
  );
  fireEvent.click(
    screen.getByRole("button", {
      name: common.runtime_interaction.keep_planning,
    }),
  );
  expect(mocks.submit).toHaveBeenLastCalledWith({
    id: "plan",
    answer: { decision: "keep_planning", feedback: "Add rollback." },
  });
  fireEvent.click(
    screen.getByRole("button", {
      name: common.runtime_interaction.approve_plan,
    }),
  );
  expect(mocks.submit).toHaveBeenLastCalledWith({
    id: "plan",
    answer: { decision: "approved", feedback: "Add rollback." },
  });
  fireEvent.click(
    screen.getByRole("button", {
      name: common.runtime_interaction.cancel_plan,
    }),
  );
  expect(mocks.submit).toHaveBeenLastCalledWith({
    id: "plan",
    answer: { decision: "cancelled" },
  });
});

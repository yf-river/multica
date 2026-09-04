// @vitest-environment jsdom

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../../test/i18n";
import { PillButton } from "../../../common/pill-button";
import { AssigneePicker } from "./assignee-picker";
import { PriorityPicker } from "./priority-picker";

const queryOptions: Array<{ queryKey?: readonly unknown[]; enabled?: boolean }> = [];

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (options: { queryKey?: readonly unknown[]; enabled?: boolean }) => {
    queryOptions.push(options);
    return { data: [] };
  },
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: null }) => unknown) =>
    selector({ user: null }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Unknown" }),
}));

afterEach(cleanup);
beforeEach(() => {
  queryOptions.length = 0;
});

describe("deferred picker triggers", () => {
  it("renders generated content when triggerRender only supplies an empty shell", () => {
    renderWithI18n(
      <>
        <PriorityPicker
          priority="none"
          onUpdate={() => {}}
          triggerRender={<PillButton />}
        />
        <AssigneePicker
          assigneeType={null}
          assigneeId={null}
          onUpdate={() => {}}
          triggerRender={<PillButton />}
        />
      </>,
    );

    expect(
      screen.getByRole("button", { name: "No priority" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Unassigned" }),
    ).toBeInTheDocument();
  });

  it("keeps assignee directories disabled until the picker opens", async () => {
    renderWithI18n(
      <AssigneePicker
        assigneeType={null}
        assigneeId={null}
        onUpdate={() => {}}
      />,
    );

    const directoryQueries = () =>
      queryOptions.filter((option) =>
        ["members", "agents", "squads", "assignee-frequency"].includes(
          String(option.queryKey?.[2]),
        ),
      );
    expect(directoryQueries()).not.toHaveLength(0);
    expect(directoryQueries().every((option) => option.enabled === false)).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Unassigned" }));

    await waitFor(() => {
      expect(directoryQueries().some((option) => option.enabled === true)).toBe(true);
    });
  });
});

/**
 * @vitest-environment jsdom
 */
import { createElement } from "react";
import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AgentAvailability } from "@multica/core/agents";
import type { ChatPendingTask } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { TaskStatusPill } from "./task-status-pill";

function renderStatus(status: string, availability: AgentAvailability) {
  renderWithI18n(
    createElement(TaskStatusPill, {
      pendingTask: {
        task_id: "task-1",
        status,
        created_at: "2026-07-17T10:00:00.000Z",
      } as ChatPendingTask,
      taskMessages: [],
      availability,
    }),
  );
}

afterEach(() => {
  vi.useRealTimers();
});

describe("TaskStatusPill", () => {
  it.each([
    ["queued", "online", "排队中"],
    ["queued", "offline", "离线"],
    ["running", "online", "思考中"],
  ] as const)("renders %s/%s as %s", (status, availability, label) => {
    vi.useFakeTimers();
    vi.setSystemTime("2026-07-17T10:00:05.000Z");

    renderStatus(status, availability);

    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(/5s/)).toBeInTheDocument();
  });
});

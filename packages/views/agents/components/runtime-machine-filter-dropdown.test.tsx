// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales-test/en/common.json";
import enAgents from "../../locales-test/en/agents.json";
import type { RuntimeMachine } from "../../runtimes/components/runtime-machines";
import { RuntimeMachineFilterDropdown } from "./runtime-machine-filter-dropdown";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

function makeMachine(overrides: Partial<RuntimeMachine> = {}): RuntimeMachine {
  return {
    id: "machine-1",
    daemonId: "daemon-1",
    title: "dev.local",
    subtitle: "x86_64 macOS",
    deviceInfo: "dev.local · x86_64 macOS",
    cliVersion: "1.0.0",
    mode: "local",
    section: "remote",
    health: "online",
    runtimes: [],
    onlineCount: 1,
    issueCount: 0,
    runningCount: 0,
    queuedCount: 0,
    providerNames: ["claude"],
    lastSeenAt: "2026-05-17T11:59:50Z",
    ...overrides,
  };
}

function renderDropdown(
  machines: RuntimeMachine[],
  value: string | null,
  onChange: (id: string | null) => void,
  agentCountByMachine: Map<string, number>,
  totalAgentCount = Array.from(agentCountByMachine.values()).reduce((sum, n) => sum + n, 0),
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <RuntimeMachineFilterDropdown
          machines={machines}
          value={value}
          onChange={onChange}
          agentCountByMachine={agentCountByMachine}
          totalAgentCount={totalAgentCount}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("RuntimeMachineFilterDropdown", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("shows all-runtime and machine counts", () => {
    const machines = [
      makeMachine({ id: "m-remote", title: "dev.local" }),
      makeMachine({ id: "m-cloud", title: "Multica cloud", section: "cloud", mode: "cloud" }),
    ];
    const counts = new Map([
      ["m-remote", 2],
      ["m-cloud", 5],
    ]);
    renderDropdown(machines, null, vi.fn(), counts);
    const trigger = screen.getByTestId("agents-runtime-filter");
    expect(trigger.textContent).toContain("All runtimes");
    expect(trigger.textContent).toContain("7");
  });

  it("groups remote and cloud machines", () => {
    const machines = [
      makeMachine({ id: "m-remote", title: "build-server" }),
      makeMachine({ id: "m-cloud", title: "Multica cloud", section: "cloud", mode: "cloud" }),
    ];
    renderDropdown(machines, null, vi.fn(), new Map([["m-remote", 2], ["m-cloud", 3]]));
    fireEvent.click(screen.getByTestId("agents-runtime-filter"));
    expect(screen.getByText("Remote")).toBeTruthy();
    expect(screen.getByText("Cloud")).toBeTruthy();
    expect(screen.getByText("build-server")).toBeTruthy();
    expect(screen.getByText("Multica cloud")).toBeTruthy();
  });

  it("changes the selected scope", () => {
    const machines = [makeMachine({ id: "m-remote", title: "build-server" })];
    const onChange = vi.fn();
    renderDropdown(machines, "m-remote", onChange, new Map([["m-remote", 1]]));
    fireEvent.click(screen.getByTestId("agents-runtime-filter"));
    fireEvent.click(screen.getByRole("menuitem", { name: /All runtimes/ }));
    expect(onChange).toHaveBeenCalledWith(null);
    fireEvent.click(screen.getByTestId("agents-runtime-filter"));
    fireEvent.click(screen.getByRole("menuitem", { name: /build-server/ }));
    expect(onChange).toHaveBeenCalledWith("m-remote");
  });

  it("exposes menuitem semantics and an empty state", () => {
    renderDropdown([], null, vi.fn(), new Map());
    fireEvent.click(screen.getByTestId("agents-runtime-filter"));
    expect(screen.getByRole("menu")).toBeTruthy();
    expect(screen.getByText("No machines yet")).toBeTruthy();
  });

  it("uses an explicit total for the all-runtime badge", () => {
    renderDropdown(
      [makeMachine({ id: "m-remote" })],
      null,
      vi.fn(),
      new Map([["m-remote", 3]]),
      5,
    );
    expect(screen.getByTestId("agents-runtime-filter").textContent).toContain("5");
  });
});

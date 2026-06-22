import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/zh-Hans/common.json";
import enOnboarding from "../locales/zh-Hans/onboarding.json";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, onboarding: enOnboarding } };

const { mockUser, mockSaveQuestionnaire, mockCaptureEvent } = vi.hoisted(() => ({
  mockUser: { value: null as null | Record<string, unknown> },
  mockSaveQuestionnaire: vi.fn(),
  mockCaptureEvent: vi.fn(),
}));

vi.mock("@multica/core/auth", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/auth")>(
      "@multica/core/auth",
    );
  const useAuthStore = Object.assign(
    (selector: (s: { user: unknown }) => unknown) =>
      selector({ user: mockUser.value }),
    { getState: () => ({ user: mockUser.value }) },
  );
  return { ...actual, useAuthStore };
});

vi.mock("@multica/core/onboarding", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/onboarding")>(
      "@multica/core/onboarding",
    );
  return { ...actual, saveQuestionnaire: mockSaveQuestionnaire };
});

vi.mock("@multica/core/analytics", () => ({
  captureEvent: mockCaptureEvent,
  setPersonProperties: vi.fn(),
}));

import { SourceBackfillModal } from "./source-backfill-modal";

function setUser(partial: Record<string, unknown> | null) {
  mockUser.value = partial;
}

function wipeDismissCounters() {
  for (let i = window.localStorage.length - 1; i >= 0; i--) {
    const k = window.localStorage.key(i);
    if (k && k.startsWith("multica.source_backfill.dismiss.")) {
      window.localStorage.removeItem(k);
    }
  }
}

/**
 * Default tests run with reduced-motion *on* so the modal's entrance
 * delay short-circuits and the dialog opens synchronously — keeps the
 * behavioural tests focused on selection / submit / skip semantics
 * rather than fighting timers. The dedicated entrance-delay test below
 * overrides this to assert the deferred-open path.
 */
function mockPrefersReducedMotion(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    configurable: true,
    value: (q: string) => ({
      matches: q.includes("reduce") ? matches : false,
      media: q,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  });
}

beforeEach(() => {
  mockSaveQuestionnaire.mockReset();
  mockSaveQuestionnaire.mockResolvedValue(undefined);
  mockCaptureEvent.mockReset();
  setUser(null);
  wipeDismissCounters();
  mockPrefersReducedMotion(true);
});

afterEach(() => {
  wipeDismissCounters();
});

function renderModal() {
  return render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <SourceBackfillModal />
    </I18nProvider>,
  );
}

describe("SourceBackfillModal", () => {
  it("does not render when there is no user", () => {
    renderModal();
    expect(
      screen.queryByText(/你是从哪里了解到 Multica 的/i),
    ).not.toBeInTheDocument();
  });

  it("does not render for onboarded users even when source is missing", () => {
    setUser({
      id: "u1",
      onboarded_at: "2026-01-01T00:00:00Z",
      onboarding_questionnaire: { source: [] },
    });
    renderModal();
    expect(
      screen.queryByText(/你是从哪里了解到 Multica 的/i),
    ).not.toBeInTheDocument();
    expect(mockCaptureEvent).not.toHaveBeenCalled();
    expect(mockSaveQuestionnaire).not.toHaveBeenCalled();
  });
});

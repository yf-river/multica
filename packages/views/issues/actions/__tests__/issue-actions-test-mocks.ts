// eslint-disable-next-line import-x/no-extraneous-dependencies -- test-only mock module
import { vi } from "vitest";
import "../../test/current-issue-boundary-mocks";
import "../../test/current-workspace-query-mock";

type MockFunction = ReturnType<typeof vi.fn>;

export const mockOpenModal: MockFunction = vi.fn();

vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(
    (selector?: (state: { open: typeof mockOpenModal }) => unknown) => {
      const state = { open: mockOpenModal };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ open: mockOpenModal }) },
  ),
}));

vi.mock("../../../navigation", () => ({
  useNavigation: () => ({
    push: vi.fn(),
    pathname: "/test/issues/issue-1",
    searchParams: new URLSearchParams(),
    back: vi.fn(),
    replace: vi.fn(),
    getShareableUrl: (path: string) => `https://app.multica.com${path}`,
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

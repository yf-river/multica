import type { ReactNode } from "react";
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/zh-Hans/common.json";
import enWorkspace from "../locales/zh-Hans/workspace.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, workspace: enWorkspace },
};

const mockPush = vi.hoisted(() => vi.fn());
const mockCreateWorkspaceMutate = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: mockPush }),
}));

vi.mock("@multica/core/workspace/mutations", () => ({
  useCreateWorkspace: () => ({
    mutate: mockCreateWorkspaceMutate,
    isPending: false,
  }),
}));

vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h1>{children}</h1>,
  DialogDescription: ({ children }: { children: ReactNode }) => (
    <p>{children}</p>
  ),
}));

vi.mock("sonner", () => ({
  toast: {
    error: mockToastError,
  },
}));

import { CreateWorkspaceModal } from "./create-workspace";

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderModal(props: { onClose: () => void }) {
  return render(<CreateWorkspaceModal {...props} />, { wrapper: I18nWrapper });
}

describe("CreateWorkspaceModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("auto-generates the slug until the user edits it", async () => {
    const user = userEvent.setup();
    renderModal({ onClose: vi.fn() });

    const nameInput = screen.getByPlaceholderText("我的工作区");
    const slugInput = screen.getByPlaceholderText("my-workspace");

    await user.type(nameInput, "My Team");
    expect(slugInput).toHaveValue("my-team");

    await user.clear(slugInput);
    await user.type(slugInput, "custom-team");
    await user.clear(nameInput);
    await user.type(nameInput, "Renamed Team");

    expect(slugInput).toHaveValue("custom-team");
  });

  it("shows a specific slug conflict error on 409 responses", async () => {
    const user = userEvent.setup();
    mockCreateWorkspaceMutate.mockImplementation(
      (
        _data: unknown,
        options: { onError: (error: unknown) => void },
      ) => {
        options.onError({ status: 409 });
      },
    );

    renderModal({ onClose: vi.fn() });

    await user.type(screen.getByPlaceholderText("我的工作区"), "My Team");
    await user.click(screen.getByRole("button", { name: "创建工作区" }));

    await waitFor(() => {
      expect(
        screen.getByText("该工作区 URL 已被占用。"),
      ).toBeInTheDocument();
    });

    expect(mockToastError).toHaveBeenCalledWith(
      "请换一个工作区 URL",
    );
    expect(mockCreateWorkspaceMutate).toHaveBeenCalledWith(
      { name: "My Team", slug: "my-team" },
      expect.any(Object),
    );
  });

  it("navigates into the newly created workspace after success", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    mockCreateWorkspaceMutate.mockImplementation(
      (
        _data: unknown,
        options: { onSuccess: (ws: { slug: string }) => void },
      ) => {
        options.onSuccess({ slug: "my-team" });
      },
    );

    renderModal({ onClose });

    await user.type(screen.getByPlaceholderText("我的工作区"), "My Team");
    await user.click(screen.getByRole("button", { name: "创建工作区" }));

    expect(onClose).toHaveBeenCalled();
    expect(mockPush).toHaveBeenCalledWith("/my-team/issues");
  });
});

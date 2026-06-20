import type { ReactNode } from "react";
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render as rtlRender, screen, type RenderOptions } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enSettings from "../../locales/zh-Hans/settings.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function render(ui: React.ReactElement, options?: RenderOptions) {
  return rtlRender(ui, { wrapper: I18nWrapper, ...options });
}

// The shared Dialog is a Base UI portal that's awkward to test — strip it to
// simple pass-through wrappers. The typed-confirmation logic lives in the
// dialog body, not in Base UI, so this doesn't reduce coverage.
vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div>{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h1>{children}</h1>,
  DialogDescription: ({ children }: { children: ReactNode }) => <p>{children}</p>,
  DialogFooter: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

import { DeleteWorkspaceDialog } from "./delete-workspace-dialog";

describe("DeleteWorkspaceDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("输入为空时禁用删除", () => {
    render(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: "删除工作区" })).toBeDisabled();
  });

  it("输入不匹配时保持删除禁用，并区分大小写", async () => {
    const user = userEvent.setup();
    render(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );

    await user.type(screen.getByRole("textbox"), "ACME"); // wrong case
    expect(screen.getByRole("button", { name: "删除工作区" })).toBeDisabled();

    await user.clear(screen.getByRole("textbox"));
    await user.type(screen.getByRole("textbox"), "acme "); // trailing space
    expect(screen.getByRole("button", { name: "删除工作区" })).toBeDisabled();
  });

  it("完全匹配时启用删除，并在点击时调用 onConfirm", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    );

    await user.type(screen.getByRole("textbox"), "acme");
    const deleteBtn = screen.getByRole("button", { name: "删除工作区" });
    expect(deleteBtn).toBeEnabled();

    await user.click(deleteBtn);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("匹配时回车提交，不匹配时忽略回车", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    );

    const input = screen.getByRole("textbox");
    await user.type(input, "acm{Enter}"); // not yet matched
    expect(onConfirm).not.toHaveBeenCalled();

    await user.type(input, "e{Enter}"); // now matches "acme"
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("取消会关闭对话框且不调用 onConfirm", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    const onConfirm = vi.fn();
    render(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open
        onOpenChange={onOpenChange}
        onConfirm={onConfirm}
      />,
    );

    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("pending 时显示加载态并禁用两个按钮", () => {
    render(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        loading
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: "删除中..." })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消" })).toBeDisabled();
  });

  it("对带空格、Unicode 和其他非 ASCII 字符的名称做字面匹配", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <DeleteWorkspaceDialog
        workspaceName="My 团队 🚀"
        open
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    );
    const input = screen.getByRole("textbox");
    await user.type(input, "My 团队 🚀");
    expect(screen.getByRole("button", { name: "删除工作区" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "删除工作区" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("待删除工作区变化时重置输入框", () => {
    const { rerender } = render(
      <DeleteWorkspaceDialog
        workspaceName="old-name"
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );
    const input = screen.getByRole("textbox") as HTMLInputElement;
    // Simulate user typing (set value directly since userEvent.type would
    // lose focus across re-renders).
    input.value = "old-name";
    rerender(
      <DeleteWorkspaceDialog
        workspaceName="new-name"
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );
    expect(screen.getByRole("textbox")).toHaveValue("");
  });

  it("clears the input when reopened so prior attempts don't leak", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );

    await user.type(screen.getByRole("textbox"), "partial");
    expect(screen.getByRole("textbox")).toHaveValue("partial");

    // Simulate close → reopen (e.g. user canceled, then clicked Delete again)
    rerender(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open={false}
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );
    rerender(
      <DeleteWorkspaceDialog
        workspaceName="acme"
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );

    expect(screen.getByRole("textbox")).toHaveValue("");
  });
});

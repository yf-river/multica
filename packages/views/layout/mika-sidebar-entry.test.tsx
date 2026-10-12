import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";
import { MikaSidebarEntry } from "./mika-sidebar-entry";

const h = vi.hoisted(() => ({ mika: null as { id: string; name: string; avatar_url: string } | null, isActive: false, mutate: vi.fn(), push: vi.fn(), close: vi.fn(), toast: vi.fn() }));
vi.mock("@multica/core/onboarding", () => ({ useMikaConversation: () => ({ mika: h.mika, isActive: h.isActive, open: { mutateAsync: h.mutate, isPending: false } }) }));
vi.mock("@multica/core/auth", () => { const store = Object.assign((selector: (state: unknown) => unknown) => selector({ user: { id: "user" } }), { getState: () => ({ user: { id: "user" } }) }); return { useAuthStore: store }; });
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ chat: () => "/one/chat", chatSession: (id: string) => `/one/chat?session=${id}` }) }));
vi.mock("@multica/core/workspace/avatar-url", () => ({ resolvePublicFileUrl: (value: string) => value }));
vi.mock("../navigation", () => ({ useNavigation: () => ({ pathname: "/one/chat", searchParams: new URLSearchParams("session=s"), push: h.push }) }));
vi.mock("sonner", () => ({ toast: { error: h.toast } }));
vi.mock("@multica/ui/components/ui/sidebar", () => ({ SidebarMenuItem: ({ children }: { children: React.ReactNode }) => <div>{children}</div>, SidebarMenuButton: ({ children, isActive, tooltip: _tooltip, ...props }: React.ComponentProps<"button"> & { isActive: boolean; tooltip: string }) => <button data-active={isActive ? "" : undefined} {...props}>{children}</button>, useSidebar: () => ({ setOpenMobile: h.close }) }));

beforeEach(() => { vi.clearAllMocks(); h.mika = { id: "mika", name: "Renamed assistant", avatar_url: "emoji:🌱" }; h.isActive = false; h.mutate.mockResolvedValue({ id: "existing" }); });
afterEach(cleanup);

describe("Mika sidebar", () => {
  it("uses the current name and avatar, and hides an inaccessible Mika", () => {
    const { rerender } = renderWithI18n(<MikaSidebarEntry workspaceId="ws" workspaceSlug="one" />);
    expect(screen.getByRole("button", { name: "Renamed assistant" })).toBeVisible();
    expect(screen.getByText("🌱")).toBeVisible();
    h.mika = null;
    rerender(<MikaSidebarEntry workspaceId="ws" workspaceSlug="one" />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("opens one existing session and dismisses the mobile sidebar", async () => {
    let resolve!: (value: { id: string }) => void;
    h.mutate.mockImplementation(() => new Promise((done) => { resolve = done; }));
    renderWithI18n(<MikaSidebarEntry workspaceId="ws" workspaceSlug="one" />);
    const button = screen.getByRole("button", { name: "Renamed assistant" });
    fireEvent.click(button); fireEvent.click(button);
    expect(h.mutate).toHaveBeenCalledTimes(1);
    await act(async () => resolve({ id: "existing" }));
    expect(h.push).toHaveBeenCalledWith("/one/chat?session=existing");
    expect(h.close).toHaveBeenCalledWith(false);
  });

  it("does not navigate after changing workspace while opening", async () => {
    let resolve!: (value: { id: string }) => void;
    h.mutate.mockImplementation(() => new Promise((done) => { resolve = done; }));
    const { rerender } = renderWithI18n(<MikaSidebarEntry workspaceId="ws" workspaceSlug="one" />);
    fireEvent.click(screen.getByRole("button", { name: "Renamed assistant" }));
    rerender(<MikaSidebarEntry workspaceId="ws-two" workspaceSlug="two" />);
    await act(async () => resolve({ id: "old" }));
    expect(h.push).not.toHaveBeenCalled();
  });

  it("highlights only the confirmed Mika session and reports failures", async () => {
    h.isActive = true;
    h.mutate.mockRejectedValue(new Error("offline API"));
    const { rerender } = renderWithI18n(<MikaSidebarEntry workspaceId="ws" workspaceSlug="one" />);
    const button = screen.getByRole("button", { name: "Renamed assistant" });
    expect(button).toHaveAttribute("aria-current", "page");
    h.isActive = false;
    rerender(<MikaSidebarEntry workspaceId="ws" workspaceSlug="one" />);
    expect(button).not.toHaveAttribute("aria-current");
    fireEvent.click(button);
    await waitFor(() => expect(h.toast).toHaveBeenCalledTimes(1));
    expect(h.push).not.toHaveBeenCalled();
  });
});

// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import { ProjectRepositoryBranch, RepositoryConfiguration } from "./repository-configuration";
import type { ProjectResource } from "@multica/core/types";

const { update, projectUpdate } = vi.hoisted(() => ({ update: vi.fn(), projectUpdate: vi.fn() }));
vi.mock("@tanstack/react-query", () => ({ useQuery: () => ({ data: [] }) }));
vi.mock("@multica/core/runtimes", () => ({ runtimeListOptions: () => ({}) }));
vi.mock("@multica/core/workspace/repository-queries", () => ({ useRepositoryActions: () => ({ update: { mutateAsync: update }, location: {}, removeLocation: {} }) }));
vi.mock("@multica/core/projects", () => ({ useUpdateProjectResource: () => ({ mutateAsync: projectUpdate }) }));

beforeEach(() => { vi.clearAllMocks(); update.mockResolvedValue({ revision: 2 }); projectUpdate.mockResolvedValue({}); });

describe("repository branch validation", () => {
  it("rejects invalid workspace branches before writing and saves valid branches", async () => {
    renderWithI18n(<RepositoryConfiguration wsId="w" repo={{ id: "r", url: "https://github.com/test/repo", ref: "main" }} canManage section="branch" />, { locale: "zh-Hans" });
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "bad branch" } });
    fireEvent.submit(input.closest("form")!);
    expect(update).not.toHaveBeenCalled();
    expect(screen.getByText(/分支名称无效/)).toBeTruthy();
    fireEvent.change(input, { target: { value: "release/fix" } });
    fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ data: expect.objectContaining({ ref: "release/fix" }) })));
  });

  it("preserves the project binding identity and supports clearing its override", async () => {
    const resource = { id: "p-r", resource_ref: { repository_id: "r", project_ref: "project-main", ref: "project-main" } } as ProjectResource;
    renderWithI18n(<ProjectRepositoryBranch wsId="w" projectId="p" resource={resource} />, { locale: "zh-Hans" });
    const input = screen.getByRole("textbox", { hidden: true });
    fireEvent.change(input, { target: { value: "--help" } });
    fireEvent.submit(input.closest("form")!);
    expect(projectUpdate).not.toHaveBeenCalled();
    fireEvent.change(input, { target: { value: "" } });
    fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(projectUpdate).toHaveBeenCalledWith({ resourceId: "p-r", data: { resource_ref: { repository_id: "r", ref: "" } } }));
  });
});

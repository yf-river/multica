import { describe, it, expect } from "vitest";
import { deriveGitHubSettings } from "./settings";
import { DEFAULT_WORKSPACE_SETTINGS, type Workspace, type WorkspaceSettings } from "../types";

function ws(settings: Partial<WorkspaceSettings>): Pick<Workspace, "settings"> {
  return { settings: { ...DEFAULT_WORKSPACE_SETTINGS, ...settings } };
}

describe("deriveGitHubSettings", () => {
  it("uses the current workspace defaults before a workspace is selected", () => {
    expect(deriveGitHubSettings(null)).toEqual({
      enabled: true,
      prSidebar: true,
      coAuthor: true,
      autoLinkPRs: true,
    });
  });

  it("master switch off forces every dependent flag off", () => {
    const got = deriveGitHubSettings(
      ws({
        github_enabled: false,
        github_pr_sidebar_enabled: true,
        co_authored_by_enabled: true,
        github_auto_link_prs_enabled: true,
      }),
    );
    expect(got).toEqual({
      enabled: false,
      prSidebar: false,
      coAuthor: false,
      autoLinkPRs: false,
    });
  });

  it("each sub-flag can be flipped independently when master is on", () => {
    expect(
      deriveGitHubSettings(ws({ github_pr_sidebar_enabled: false })),
    ).toMatchObject({ enabled: true, prSidebar: false, coAuthor: true, autoLinkPRs: true });

    expect(
      deriveGitHubSettings(ws({ co_authored_by_enabled: false })),
    ).toMatchObject({ enabled: true, prSidebar: true, coAuthor: false, autoLinkPRs: true });

    expect(
      deriveGitHubSettings(ws({ github_auto_link_prs_enabled: false })),
    ).toMatchObject({ enabled: true, prSidebar: true, coAuthor: true, autoLinkPRs: false });
  });
});

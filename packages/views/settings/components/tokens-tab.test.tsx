import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ExternalCredentialProfile } from "@multica/core/types";
import enCommon from "../../locales/zh-Hans/common.json";
import enSettings from "../../locales/zh-Hans/settings.json";

const mockCreateProfile = vi.hoisted(() => vi.fn());
const mockUpdateProfile = vi.hoisted(() => vi.fn());
const mockDeleteProfile = vi.hoisted(() => vi.fn());
const mockTestProfile = vi.hoisted(() => vi.fn());
const mockProfiles = vi.hoisted(() => ({
  current: [
    {
      id: "credential-1",
      user_id: "user-1",
      scope: "account",
      provider: "gongfeng",
      name: "gongfeng-default",
      secret_binding: {
        configured: true,
        redacted: true,
        mode: "encrypted_secret",
        hint: "GONGFENG_PRIVATE_TOKEN",
      },
      capabilities: {},
      status: "verified",
      last_verified_at: null,
      created_at: "2026-06-28T00:00:00Z",
      updated_at: "2026-06-28T00:00:00Z",
    },
  ] as ExternalCredentialProfile[],
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    const provider = options.queryKey?.[2];
    return {
      data: {
        profiles: typeof provider === "string"
          ? mockProfiles.current.filter((profile) => profile.provider === provider)
          : mockProfiles.current,
      },
    };
  },
}));

vi.mock("@multica/core/external-credentials", () => ({
  externalCredentialProfilesOptions: (provider?: string) => ({
    queryKey: provider
      ? ["external-credential-profiles", "list", provider]
      : ["external-credential-profiles", "list"],
  }),
  useCreateExternalCredentialProfile: () => ({ mutate: mockCreateProfile, isPending: false }),
  useUpdateExternalCredentialProfile: () => ({ mutate: mockUpdateProfile, isPending: false }),
  useDeleteExternalCredentialProfile: () => ({ mutate: mockDeleteProfile, isPending: false }),
  useTestExternalCredentialProfile: () => ({ mutate: mockTestProfile, isPending: false }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { TokensTab } from "./tokens-tab";

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

describe("TokensTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockProfiles.current = [
      {
        id: "credential-1",
        user_id: "user-1",
        scope: "account",
        provider: "gongfeng",
        name: "gongfeng-default",
        secret_binding: {
          configured: true,
          redacted: true,
          mode: "encrypted_secret",
          hint: "GONGFENG_PRIVATE_TOKEN",
        },
        capabilities: {},
        status: "verified",
        last_verified_at: null,
        created_at: "2026-06-28T00:00:00Z",
        updated_at: "2026-06-28T00:00:00Z",
      },
      {
        id: "credential-2",
        user_id: "user-1",
        scope: "account",
        provider: "tapd",
        name: "tapd-default",
        secret_binding: {
          configured: true,
          redacted: true,
          mode: "encrypted_secret",
          hint: "TAPD_ACCESS_TOKEN",
        },
        capabilities: {},
        status: "verified",
        last_verified_at: null,
        created_at: "2026-06-28T00:00:00Z",
        updated_at: "2026-06-28T00:00:00Z",
      },
    ];
  });

  it("已设置时默认隐藏工蜂凭据编辑表单", () => {
    render(<TokensTab />, { wrapper: I18nWrapper });

    expect(screen.queryByRole("heading", { name: "Multica 访问令牌" })).toBeNull();
    expect(screen.queryByPlaceholderText("令牌名称（例如：我的 CLI）")).toBeNull();
    expect(screen.queryByRole("button", { name: "创建" })).toBeNull();
    expect(screen.getByRole("heading", { name: "外部服务凭据" })).toBeTruthy();
    expect(screen.getByText(/管理智能体访问工蜂、TAPD/)).toBeTruthy();
    const panel = within(screen.getByTestId("settings-gongfeng-credential-panel"));
    expect(panel.getByRole("heading", { name: "工蜂访问凭据" })).toBeTruthy();
    expect(panel.getByText("已设置 · GONGFENG_PRIVATE_TOKEN")).toBeTruthy();
    expect(panel.getByText(/GONGFENG_ACCESS_TOKEN \/ GONGFENG_PRIVATE_TOKEN/)).toBeTruthy();
    expect(panel.getByText("注入变量")).toBeTruthy();
    expect(panel.getByText("当前绑定")).toBeTruthy();
    expect(panel.getByText("凭据方式")).toBeTruthy();
    expect(panel.getByRole("button", { name: "测试连接" })).toBeTruthy();
    expect(panel.getByRole("button", { name: "更换凭据" })).toBeTruthy();
    expect(panel.getByRole("button", { name: "移除凭据" })).toBeTruthy();
    expect(panel.queryByRole("button", { name: "访问令牌" })).toBeNull();
    expect(panel.queryByRole("button", { name: "环境变量" })).toBeNull();
    expect(panel.queryByRole("button", { name: "保存凭据" })).toBeNull();
    expect(panel.queryByPlaceholderText("输入新 token 可替换当前凭据")).toBeNull();
    expect(screen.getByTestId("settings-tapd-credential-panel")).toBeTruthy();
  });

  it("点击更换凭据后显示工蜂凭据编辑表单", async () => {
    const user = userEvent.setup();
    render(<TokensTab />, { wrapper: I18nWrapper });
    const panel = within(screen.getByTestId("settings-gongfeng-credential-panel"));

    await user.click(panel.getByRole("button", { name: "更换凭据" }));

    expect(panel.getByRole("button", { name: "访问令牌" })).toBeTruthy();
    expect(panel.getByRole("button", { name: "环境变量" })).toBeTruthy();
    expect(panel.getByPlaceholderText("输入新 token 可替换当前凭据")).toBeTruthy();
    expect(panel.getByRole("button", { name: "测试连接" })).toBeTruthy();
    expect(panel.getByRole("button", { name: "保存凭据" })).toBeTruthy();
  });

  it("已设置时点击测试连接会复测当前保存的凭据", async () => {
    const user = userEvent.setup();
    render(<TokensTab />, { wrapper: I18nWrapper });
    const panel = within(screen.getByTestId("settings-gongfeng-credential-panel"));

    await user.click(panel.getByRole("button", { name: "测试连接" }));

    expect(mockUpdateProfile).toHaveBeenCalledWith(
      { id: "credential-1", data: { verify_now: true } },
      expect.any(Object),
    );
    expect(mockTestProfile).not.toHaveBeenCalled();
    expect(mockCreateProfile).not.toHaveBeenCalled();
  });

  it("点击测试连接会调用不保存测试接口", async () => {
    const user = userEvent.setup();
    render(<TokensTab />, { wrapper: I18nWrapper });
    const panel = within(screen.getByTestId("settings-gongfeng-credential-panel"));

    await user.click(panel.getByRole("button", { name: "更换凭据" }));
    await user.click(panel.getByRole("button", { name: "环境变量" }));
    await user.click(panel.getByRole("button", { name: "测试连接" }));

    expect(mockTestProfile).toHaveBeenCalledWith(
      { provider: "gongfeng", secret_ref: "env:GONGFENG_ACCESS_TOKEN" },
      expect.any(Object),
    );
    expect(mockUpdateProfile).not.toHaveBeenCalled();
    expect(mockCreateProfile).not.toHaveBeenCalled();
  });

  it("未设置时默认显示工蜂凭据编辑表单", () => {
    mockProfiles.current = [];

    render(<TokensTab />, { wrapper: I18nWrapper });
    const panel = within(screen.getByTestId("settings-gongfeng-credential-panel"));

    expect(panel.getAllByText("未设置").length).toBeGreaterThan(0);
    expect(panel.queryByRole("button", { name: "更换凭据" })).toBeNull();
    expect(panel.queryByRole("button", { name: "移除凭据" })).toBeNull();
    expect(panel.getByRole("button", { name: "访问令牌" })).toBeTruthy();
    expect(panel.getByRole("button", { name: "环境变量" })).toBeTruthy();
    expect(panel.getByPlaceholderText("粘贴工蜂 access token")).toBeTruthy();
    expect(panel.getByRole("button", { name: "测试连接" })).toBeTruthy();
    expect(panel.getByRole("button", { name: "保存凭据" })).toBeTruthy();
  });

  it("校验失败时默认显示修复表单和错误信息", () => {
    mockProfiles.current = [
      {
        id: "credential-1",
        user_id: "user-1",
        scope: "account",
        provider: "gongfeng",
        name: "gongfeng-default",
        secret_binding: {
          configured: true,
          redacted: true,
          mode: "encrypted_secret",
          hint: "GONGFENG_PRIVATE_TOKEN",
        },
        capabilities: {},
        status: "failed",
        last_error: "token 无法访问工蜂",
        last_verified_at: null,
        created_at: "2026-06-28T00:00:00Z",
        updated_at: "2026-06-28T00:00:00Z",
      },
    ];

    render(<TokensTab />, { wrapper: I18nWrapper });
    const panel = within(screen.getByTestId("settings-gongfeng-credential-panel"));

    expect(panel.getByText("校验失败 · GONGFENG_PRIVATE_TOKEN")).toBeTruthy();
    expect(panel.queryByRole("button", { name: "更换凭据" })).toBeNull();
    expect(panel.getByRole("button", { name: "访问令牌" })).toBeTruthy();
    expect(panel.getByPlaceholderText("输入新 token 可替换当前凭据")).toBeTruthy();
    expect(panel.getByRole("button", { name: "测试连接" })).toBeTruthy();
    expect(panel.getByRole("button", { name: "保存凭据" })).toBeTruthy();
    expect(panel.getByText("token 无法访问工蜂")).toBeTruthy();
  });

  it("工蜂凭据操作按钮保持单行显示", () => {
    render(<TokensTab />, { wrapper: I18nWrapper });
    const panel = within(screen.getByTestId("settings-gongfeng-credential-panel"));
    const actions = screen.getByTestId("settings-gongfeng-credential-panel-actions");

    expect(actions.className).toContain("flex-col");
    expect(panel.getByRole("button", { name: "测试连接" }).className).toContain("w-full");
    expect(panel.getByRole("button", { name: "更换凭据" }).className).toContain("whitespace-nowrap");
    expect(panel.getByRole("button", { name: "移除凭据" }).className).toContain("whitespace-nowrap");
  });

  it("TAPD 凭据面板使用 TAPD_ACCESS_TOKEN 作为默认环境变量", async () => {
    const user = userEvent.setup();
    render(<TokensTab />, { wrapper: I18nWrapper });
    const panel = within(screen.getByTestId("settings-tapd-credential-panel"));

    expect(panel.getByRole("heading", { name: "TAPD 访问凭据" })).toBeTruthy();
    expect(panel.getAllByText(/TAPD_ACCESS_TOKEN/).length).toBeGreaterThan(0);
    await user.click(panel.getByRole("button", { name: "更换凭据" }));
    await user.click(panel.getByRole("button", { name: "环境变量" }));
    await user.click(panel.getByRole("button", { name: "测试连接" }));

    expect(mockTestProfile).toHaveBeenCalledWith(
      { provider: "tapd", secret_ref: "env:TAPD_ACCESS_TOKEN" },
      expect.any(Object),
    );
  });
});

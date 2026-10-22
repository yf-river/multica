// 本文件是「工作区」设置标签页（通用）：编辑工作区资料（logo、名称、描述、背景
// 上下文、issue 编号前缀），均自动保存；危险区提供「退出工作区」与「删除工作区」
// （删除需要输入工作区名二次确认）。

"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { LogOut } from "lucide-react";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "@multica/ui/components/ui/alert-dialog";
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useLeaveWorkspace, useDeleteWorkspace } from "@multica/core/workspace/mutations";
import {
  memberListOptions,
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import { issueKeys } from "@multica/core/issues/queries";
import { api } from "@multica/core/api";
import {
  resolvePostAuthDestination,
  useCurrentWorkspace,
  useHasOnboarded,
} from "@multica/core/paths";
import { setCurrentWorkspace } from "@multica/core/platform";
import type { Workspace } from "@multica/core/types";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import { useNavigation } from "../../navigation";
import { DeleteWorkspaceDialog } from "./delete-workspace-dialog";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
  SettingsTab,
  type SettingsSaveStatus,
} from "./settings-layout";
import { WorkspaceMenuSettings } from "./workspace-menu-settings";
import { useAutoSave } from "./use-auto-save";

/** 工作区资料编辑的草稿：名称 / 描述 / 背景上下文。 */
interface WorkspaceDetailsDraft {
  name: string;
  description: string;
  context: string;
}

/** 两份资料草稿是否相同，供自动保存判断有无改动。 */
function workspaceDetailsEqual(
  left: WorkspaceDetailsDraft,
  right: WorkspaceDetailsDraft,
) {
  return (
    left.name === right.name &&
    left.description === right.description &&
    left.context === right.context
  );
}

/**
 * 「工作区」设置标签页。包含两块：
 * - 资料区：logo 上传 + 名称/描述/背景上下文（自动保存）+ slug（只读）+ issue 前缀
 *   （失焦时先弹确认再保存，因为前缀一变会重写所有 issue 编号的读法）；
 * - 危险区：退出工作区（唯一 owner 不允许退出）与删除工作区（输入全名确认）。
 * 编辑权限：仅 owner / admin。
 */
export function WorkspaceTab() {
  const { t } = useT("settings");
  const user = useAuthStore((s) => s.user);
  const workspace = useCurrentWorkspace();
  // 这里有意用 useCurrentWorkspace 派生出 id，而不是用会抛错的 useWorkspaceId：
  // 本组件存在一种合理场景——工作区已从列表缓存消失但 URL slug 还没变（删除后缓存
  // 失效先于导航完成，或正在查看的工作区被外部删除）。下方 `!workspace` 守卫会在
  // 那个窗口期渲染 null；而用会抛错的钩子会先崩溃。
  const wsId = workspace?.id;
  const { data: members = [], isFetched: membersFetched } = useQuery({
    ...memberListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const qc = useQueryClient();
  const leaveWorkspace = useLeaveWorkspace();
  const deleteWorkspace = useDeleteWorkspace();
  const navigation = useNavigation();
  const hasOnboarded = useHasOnboarded();

  /**
   * 把用户导航到一个安全页面，目的地根据「当前缓存的列表减掉即将消失的工作区」算出。
   *
   * 调用时机随流程而异：
   *   - 删除：在删除成功「之后」再调。实时 `workspace:deleted` 处理器会跳过自发删除
   *     （见 pending-delete.ts），所以不会有别的东西和这次导航抢跑。
   *   - 退出：仍在 mutation 发起「之前」调用——`member:removed` 目前没有自发标记，
   *     若用户还停留在这个工作区的 URL 上，该事件到达时 use-realtime-sync.ts 的处理器
   *     会触发一次并发的整页跳转，与 mutation 的 invalidateQueries 重取互相竞争，
   *     输家正在进行的请求会被取消，冒泡成未处理的 CancelledError。先导航走，处理器里
   *     「当前工作区 === 已丢失工作区」的判定就会失败、它的跳转自动空转。
   *     已知债：应让退出也改成与删除一致的「先等成功、再导航」形态。
   */
  const navigateAwayFromCurrentWorkspace = () => {
    const cachedList =
      qc.getQueryData<Workspace[]>(workspaceListOptions().queryKey) ?? [];
    const remaining = cachedList.filter((w) => w.id !== workspace?.id);
    // 在导航之前先清掉「工作区上下文」单例。有三个下游会读它：
    //  1. 实时跳转处理器的「当前 === 已丢失工作区」判定（退出的 member:removed；
    //     也是删除的第二道防线）——WS 事件到达时单例仍指向已丢失工作区的话，它们会
    //     触发一次与本次导航竞争的整页跳转。
    //  2. 壳层的 gating（桌面端 `{slug && <AppSidebar />}`）——单例残留时侧边栏会
    //     继续挂载，而已删工作区已不在列表里，`useWorkspaceId` 会抛错。
    //  3. API 客户端的 `X-Workspace-Slug` 头——删除后残留旧头最坏会漏进下一个请求。
    // WorkspaceRouteLayout 在新工作区路由挂载时会重设这个单例；这里先清掉是安全的——
    // 要么下一个工作区立刻接管，要么新工作区引导浮层接管（它没有工作区上下文，null 正好）。
    setCurrentWorkspace(null, null);
    navigation.push(resolvePostAuthDestination(remaining, hasOnboarded));
  };

  const [name, setName] = useState(workspace?.name ?? "");
  const [description, setDescription] = useState(workspace?.description ?? "");
  const [context, setContext] = useState(workspace?.context ?? "");
  const [issuePrefix, setIssuePrefix] = useState(workspace?.issue_prefix ?? "");
  const [prefixSaveStatus, setPrefixSaveStatus] =
    useState<SettingsSaveStatus>("idle");
  const [actionId, setActionId] = useState<string | null>(null);
  const [confirmAction, setConfirmAction] = useState<{
    title: string;
    description: string;
    variant?: "destructive";
    onConfirm: () => Promise<void>;
  } | null>(null);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);

  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManageWorkspace = currentMember?.role === "owner" || currentMember?.role === "admin";
  const isOwner = currentMember?.role === "owner";
  // 镜像后端不变量（server/internal/handler/workspace.go:569）：工作区任何时候都要
  // 至少有一个 owner，因此唯一的 owner 不能退出。这里在前端预判，而不是等后端 400
  // 回来再变成一条令人困惑的 toast——直接禁用「退出」并告诉用户该怎么做。
  const ownerCount = members.filter((m) => m.role === "owner").length;
  const isSoleOwner = isOwner && ownerCount <= 1;
  const isSoleMember = members.length <= 1;

  // 只在用户切到另一个工作区时重置表单。以 workspace?.id（而非对象引用）为键，
  // 避免某个无关的 mutation——例如头像/logo 上传用 setQueryData 替换了缓存里的
  // Workspace 对象——把未保存的编辑内容一并冲掉。
  useEffect(() => {
    setName(workspace?.name ?? "");
    setDescription(workspace?.description ?? "");
    setContext(workspace?.context ?? "");
    setIssuePrefix(workspace?.issue_prefix ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentionally keyed on id only; see comment above
  }, [workspace?.id]);

  // 只保留字母和数字、转大写、最长 10 个字符。后端自己也会做同样的大写与去空白——
  // 这里是纯 UX 护栏，保证输入框里看到的值和最终落库的一致。
  const normalizePrefix = (raw: string) =>
    raw.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 10);

  const normalizedPrefix = normalizePrefix(issuePrefix);
  const prefixChanged =
    !!workspace && normalizedPrefix !== workspace.issue_prefix;
  const prefixInvalid = normalizedPrefix.length === 0;

  const detailsDraft = useMemo(
    () => ({ name, description, context }),
    [context, description, name],
  );
  const savedDetails = useMemo(
    () => ({
      name: workspace?.name ?? "",
      description: workspace?.description ?? "",
      context: workspace?.context ?? "",
    }),
    [workspace?.context, workspace?.description, workspace?.name],
  );
  const saveDetails = useCallback(
    async (next: WorkspaceDetailsDraft) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, next);
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
    },
    [qc, workspace],
  );
  const detailsAutoSave = useAutoSave({
    value: detailsDraft,
    savedValue: savedDetails,
    onSave: saveDetails,
    onSuccess: () =>
      toast.success(t(($) => $.workspace.toast_saved), {
        id: "settings-auto-save",
      }),
    onError: (error) =>
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.workspace.toast_save_failed),
      ),
    enabled: !!workspace && canManageWorkspace && !!name.trim(),
    isEqual: workspaceDetailsEqual,
  });

  const performPrefixSave = async (nextPrefix: string) => {
    if (!workspace) return;
    setPrefixSaveStatus("saving");
    try {
      const updated = await api.updateWorkspace(workspace.id, {
        issue_prefix: nextPrefix,
      });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      // issue 编号是读取时按前缀现算的，所以这次（确认过的）改动之后，所有缓存的
      // issue 键都过期了，必须整片失效重取。
      await qc.invalidateQueries({ queryKey: issueKeys.all(updated.id) });
      setPrefixSaveStatus("saved");
      toast.success(t(($) => $.workspace.toast_saved), {
        id: "settings-auto-save",
      });
    } catch (error) {
      setPrefixSaveStatus("error");
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.workspace.toast_save_failed),
      );
    }
  };

  const handlePrefixBlur = () => {
    if (!workspace || prefixInvalid || !prefixChanged) return;
    const nextPrefix = normalizedPrefix;
    setConfirmAction({
      title: t(($) => $.workspace.prefix_confirm_title),
      description: t(($) => $.workspace.prefix_confirm_description, {
        oldPrefix: workspace.issue_prefix,
        newPrefix: nextPrefix,
      }),
      variant: "destructive",
      onConfirm: () => performPrefixSave(nextPrefix),
    });
  };

  const handleLeaveWorkspace = () => {
    if (!workspace) return;
    setConfirmAction({
      title: t(($) => $.workspace.leave_confirm_title),
      description: t(($) => $.workspace.leave_confirm_description, { name: workspace.name }),
      variant: "destructive",
      onConfirm: async () => {
        setActionId("leave");
        navigateAwayFromCurrentWorkspace();
        try {
          await leaveWorkspace.mutateAsync(workspace.id);
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t(($) => $.workspace.toast_leave_failed));
        } finally {
          setActionId(null);
        }
      },
    });
  };

  const handleConfirmDelete = async () => {
    if (!workspace) return;
    setActionId("delete-workspace");
    // 让删除弹窗保持加载态去等 DELETE 完成，只在成功后才导航（CLAUDE.md：会导航的
    // 流程必须先等服务端成功，不做乐观删除）。实时的 `workspace:deleted` 处理器通过
    // pending-delete 注册表跳过自发删除，所以不会用整页跳转和本次导航竞争。
    // 失败时弹窗保持打开、缓存没被动过，用户原地不动。
    try {
      await deleteWorkspace.mutateAsync(workspace.id);
      setDeleteDialogOpen(false);
      navigateAwayFromCurrentWorkspace();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.workspace.toast_delete_failed));
    } finally {
      setActionId(null);
    }
  };

  if (!workspace) return null;

  return (
    <SettingsTab title={t(($) => $.page.tabs.general)}>
      <SettingsSection
        title={t(($) => $.workspace.section_general)}
        action={
          <SettingsSaveState
            status={
              prefixSaveStatus === "saving" || prefixSaveStatus === "error"
                ? prefixSaveStatus
                : detailsAutoSave.status === "idle"
                  ? prefixSaveStatus
                  : detailsAutoSave.status
            }
            savingLabel={t(($) => $.auto_save.saving)}
            savedLabel={t(($) => $.auto_save.saved)}
            errorLabel={t(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.workspace.logo_label)}
            description={t(($) => $.workspace.click_logo_hint)}
            size="none"
          >
            <div className="flex justify-start sm:justify-end">
              <AvatarUploadControl
                variant="workspace"
                value={workspace.avatar_url ?? null}
                name={workspace.name}
                size={64}
                disabled={!canManageWorkspace}
                ariaLabel={t(($) => $.workspace.change_logo_aria)}
                onUploaded={async (url) => {
                  try {
                    const updated = await api.updateWorkspace(workspace.id, {
                      avatar_url: url,
                    });
                    qc.setQueryData(
                      workspaceKeys.list(),
                      (old: Workspace[] | undefined) =>
                        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
                    );
                    toast.success(t(($) => $.workspace.toast_logo_updated), {
                      id: "settings-auto-save",
                    });
                  } catch (error) {
                    toast.error(
                      error instanceof Error
                        ? error.message
                        : t(($) => $.workspace.toast_logo_failed),
                    );
                  }
                }}
              />
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.workspace.name_label)}
            size="text"
          >
            <Input
              type="text"
              name="workspace-name"
              autoComplete="organization"
              aria-label={t(($) => $.workspace.name_label)}
              value={name}
              onChange={(event) => setName(event.target.value)}
              onBlur={detailsAutoSave.flush}
              disabled={!canManageWorkspace}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.workspace.description_label)}
            size="text"
            align="start"
          >
            <Textarea
              name="workspace-description"
              autoComplete="off"
              aria-label={t(($) => $.workspace.description_label)}
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              onBlur={detailsAutoSave.flush}
              rows={3}
              disabled={!canManageWorkspace}
              className="resize-none"
              placeholder={t(($) => $.workspace.description_placeholder)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.workspace.context_label)}
            size="text"
            align="start"
          >
            <Textarea
              name="workspace-context"
              autoComplete="off"
              aria-label={t(($) => $.workspace.context_label)}
              value={context}
              onChange={(event) => setContext(event.target.value)}
              onBlur={detailsAutoSave.flush}
              rows={4}
              disabled={!canManageWorkspace}
              className="resize-none"
              placeholder={t(($) => $.workspace.context_placeholder)}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.workspace.slug_label)}
            size="text"
          >
            <Input
              type="text"
              name="workspace-slug"
              autoComplete="off"
              spellCheck={false}
              aria-label={t(($) => $.workspace.slug_label)}
              value={workspace.slug}
              readOnly
              className="bg-muted/50 font-mono text-muted-foreground dark:bg-muted/50"
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.workspace.issue_prefix_label)}
            description={t(($) => $.workspace.issue_prefix_hint, {
              example: `${normalizedPrefix || workspace.issue_prefix}-123`,
            })}
            size="code"
          >
              <Input
                type="text"
                name="workspace-issue-prefix"
                autoComplete="off"
                autoCapitalize="characters"
                spellCheck={false}
                aria-label={t(($) => $.workspace.issue_prefix_label)}
                value={issuePrefix}
                onChange={(event) => {
                  setPrefixSaveStatus("idle");
                  setIssuePrefix(normalizePrefix(event.target.value));
                }}
                onBlur={handlePrefixBlur}
                disabled={!canManageWorkspace}
                maxLength={10}
                aria-invalid={prefixInvalid}
                className="font-mono uppercase"
                placeholder={workspace.issue_prefix}
              />
          </SettingsRow>

            {!canManageWorkspace && (
              <div className="px-4 py-3 text-caption text-muted-foreground">
                {t(($) => $.workspace.manage_hint)}
              </div>
            )}
        </SettingsCard>
      </SettingsSection>

      <WorkspaceMenuSettings key={workspace.id} workspace={workspace} canManage={canManageWorkspace} />

      {/* 危险区——等成员查询落定后再渲染，避免仅 owner 可见的「删除」按钮与
          唯一 owner 的「退出」提示在挂载后才闪现出来。 */}
      {membersFetched && (
        <SettingsSection
          title={
            <span className="inline-flex items-center gap-2">
              <LogOut className="h-4 w-4 text-muted-foreground" />
              {t(($) => $.workspace.danger_zone)}
            </span>
          }
        >
          <SettingsCard>
            <SettingsRow
              label={t(($) => $.workspace.leave_title)}
              description={
                isSoleOwner
                  ? isSoleMember
                    ? t(($) => $.workspace.leave_sole_member)
                    : t(($) => $.workspace.leave_sole_owner)
                  : t(($) => $.workspace.leave_default)
              }
            >
              <Button
                variant="outline"
                size="sm"
                onClick={handleLeaveWorkspace}
                disabled={actionId === "leave" || isSoleOwner}
              >
                {actionId === "leave" ? t(($) => $.workspace.leaving) : t(($) => $.workspace.leave_button)}
              </Button>
            </SettingsRow>

            {isOwner && (
              <SettingsRow
                label={
                  <span className="text-destructive">
                    {t(($) => $.workspace.delete_title)}
                  </span>
                }
                description={t(($) => $.workspace.delete_description)}
              >
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => setDeleteDialogOpen(true)}
                  disabled={actionId === "delete-workspace"}
                >
                  {actionId === "delete-workspace" ? t(($) => $.workspace.deleting) : t(($) => $.workspace.delete_button)}
                </Button>
              </SettingsRow>
            )}
          </SettingsCard>
        </SettingsSection>
      )}

      <AlertDialog open={!!confirmAction} onOpenChange={(v) => { if (!v) setConfirmAction(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmAction?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmAction?.description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.workspace.confirm_cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant={confirmAction?.variant === "destructive" ? "destructive" : "default"}
              onClick={async () => {
                await confirmAction?.onConfirm();
                setConfirmAction(null);
              }}
            >
              {t(($) => $.workspace.confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <DeleteWorkspaceDialog
        workspaceName={workspace.name}
        loading={actionId === "delete-workspace"}
        open={deleteDialogOpen}
        onOpenChange={(open) => {
          // 删除请求进行中时忽略关闭请求，防止用户中途误把弹窗关掉。
          if (actionId === "delete-workspace" && !open) return;
          setDeleteDialogOpen(open);
        }}
        onConfirm={handleConfirmDelete}
      />
    </SettingsTab>
  );
}

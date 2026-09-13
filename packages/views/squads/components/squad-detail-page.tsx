"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { useWorkspaceId } from "@multica/core/hooks";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { isImeComposing } from "@multica/core/utils";
import { getShortcut, shortcutMatchesEvent } from "@multica/core/shortcuts";
import { useTimeAgo } from "../../i18n";
import { agentListOptions, memberListOptions, squadMemberStatusOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { useNavigation } from "../../navigation";
import { AppLink } from "../../navigation";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { PageHeader } from "../../layout/page-header";
import { Users, Plus, Trash2, ArrowUpRight, Crown, Loader2, Pencil, FileText, Save } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { ActorAvatar } from "../../common/actor-avatar";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import { ContentEditor } from "../../editor/content-editor";
import {
  PickerItem,
  PickerSection,
  PickerEmpty,
} from "../../issues/components/pickers/property-picker";
import { ChevronDown, UserPlus } from "lucide-react";
import { toast } from "sonner";
import type { Squad, SquadMember, SquadMemberStatus, SquadMemberStatusValue, Agent, MemberWithUser } from "@multica/core/types";
import { useT } from "../../i18n";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";

/**
 * squads 模块：小队详情页。
 * 展示单个小队的完整信息：左侧检查器（头像 / 名称 / 简介 / 负责人 / 成员数
 * / 创建与更新时间，均可内联编辑），右侧标签页面板（成员列表与 Instructions）。
 * 非管理员只读；创建者/工作区管理员可管理。
 */

export function SquadDetailPage() {
  const { t } = useT("squads");
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const { pathname, push } = useNavigation();
  const queryClient = useQueryClient();
  const squadId = pathname.split("/").pop() ?? "";

  const { data: squad, refetch: refetchSquad } = useQuery<Squad>({
    queryKey: [...workspaceKeys.squads(wsId), squadId],
    queryFn: () => api.getSquad(squadId),
    enabled: !!workspace?.id && !!squadId,
  });

  const { data: members = [], refetch: refetchMembers } = useQuery<SquadMember[]>({
    queryKey: [...workspaceKeys.squads(wsId), squadId, "members"],
    queryFn: () => api.listSquadMembers(squadId),
    enabled: !!workspace?.id && !!squadId,
  });

  // 每个小队的工作中/空闲/离线状态 + 进行中 issue 快照。WS 的 task / agent /
  // daemon 事件通过 use-realtime-sync 使其失效；staleTime 是切回标签页时的
  // 安全兜底。以 member_id 为索引，让 SquadMembersTab 能 O(1) 查找对应行。
  const { data: memberStatusResp } = useQuery({
    ...squadMemberStatusOptions(wsId, squadId),
    enabled: !!workspace?.id && !!squadId,
  });
  const memberStatusById = useMemo(() => {
    const map = new Map<string, SquadMemberStatus>();
    for (const s of memberStatusResp?.members ?? []) map.set(s.member_id, s);
    return map;
  }, [memberStatusResp]);

  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: wsMembers = [] } = useQuery(memberListOptions(wsId));

  const currentUser = useAuthStore((s) => s.user);
  const myRole = useMemo(() => {
    if (!currentUser) return null;
    return wsMembers.find((m) => m.user_id === currentUser.id)?.role ?? null;
  }, [wsMembers, currentUser]);
  const isWorkspaceAdmin = myRole === "owner" || myRole === "admin";
  // 每小队的管理门槛：工作区 owner/admin 可管理所有小队；创建者管理自己
  // 创建的小队。与 server/internal/handler/squad.go 的 canManageSquad 保持
  // 一致，保证可编辑控件只在 API 会接受写入时出现，其余人看到的是只读
  // 视图而不是会 403 的控件（MUL-4223）。
  const canManage =
    isWorkspaceAdmin || (!!currentUser && squad?.creator_id === currentUser.id);

  const [showAddMember, setShowAddMember] = useState(false);
  const [confirmArchive, setConfirmArchive] = useState(false);

  const updateSquadMut = useMutation({
    mutationFn: (data: { name?: string; description?: string; instructions?: string; avatar_url?: string; leader_id?: string }) => api.updateSquad(squadId, data),
    onSuccess: () => {
      refetchSquad();
      refetchMembers();
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
    },
  });

  const addMemberMut = useMutation({
    mutationFn: (input: { type: "agent" | "member"; id: string; role?: string }) =>
      api.addSquadMember(squadId, {
        member_type: input.type,
        member_id: input.id,
        role: input.role?.trim() || undefined,
      }),
    onSuccess: () => { refetchMembers(); toast.success(t(($) => $.toasts.member_added)); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.toasts.member_add_failed)),
  });

  const removeMemberMut = useMutation({
    mutationFn: (m: SquadMember) => api.removeSquadMember(squadId, { member_type: m.member_type, member_id: m.member_id }),
    onSuccess: () => { refetchMembers(); toast.success(t(($) => $.toasts.member_removed)); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.toasts.member_remove_failed)),
  });

  const updateRoleMut = useMutation({
    mutationFn: (input: { member: SquadMember; role: string }) =>
      api.updateSquadMemberRole(squadId, {
        member_type: input.member.member_type,
        member_id: input.member.member_id,
        role: input.role,
      }),
    onSuccess: () => { refetchMembers(); toast.success(t(($) => $.toasts.role_updated)); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.toasts.role_update_failed)),
  });

  const setLeaderMut = useMutation({
    mutationFn: (agentId: string) => api.updateSquad(squadId, { leader_id: agentId }),
    onSuccess: () => {
      refetchSquad();
      refetchMembers();
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
      toast.success(t(($) => $.toasts.leader_updated));
    },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.toasts.leader_update_failed)),
  });

  const deleteMut = useMutation({
    mutationFn: () => api.deleteSquad(squadId),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) }); push(p.squads()); toast.success(t(($) => $.archive_dialog.success)); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.toasts.archive_failed)),
  });

  const getEntityName = (type: string, id: string) => {
    if (type === "agent") return agents.find((a: Agent) => a.id === id)?.name ?? id.slice(0, 8);
    return wsMembers.find((m) => m.user_id === id)?.name ?? id.slice(0, 8);
  };

  if (!squad) {
    return <SquadDetailSkeleton />;
  }

  const availableAgents = agents.filter((a: Agent) => !a.archived_at && !members.some((m) => m.member_type === "agent" && m.member_id === a.id));
  const availableMembers = wsMembers.filter((m) => !members.some((sm) => sm.member_type === "member" && sm.member_id === m.user_id));
  const isLeader = (m: SquadMember) => m.member_type === "agent" && squad.leader_id === m.member_id;
  const isArchived = (m: SquadMember) =>
    m.member_type === "agent" && !!agents.find((a: Agent) => a.id === m.member_id)?.archived_at;

  const initials = squad.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <BreadcrumbHeader
        segments={[{ href: p.squads(), label: t(($) => $.page.title) }]}
        leaf={
          <>
            <SquadHeaderAvatar squad={squad} initials={initials} />
            <h1 className="truncate text-body font-medium text-foreground">{squad.name}</h1>
          </>
        }
        actions={
          canManage ? (
            <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setConfirmArchive(true)}>
              <Trash2 className="size-3.5 mr-1" />
              {t(($) => $.inspector.archive_button)}
            </Button>
          ) : null
        }
      />

      {/* 双栏布局与 agent 详情页一致：左栏检查器（身份 + 属性 + 负责人），
          右栏带标签页的面板（成员 | Instructions）。移动端塌缩成单列堆叠。 */}
      <div className="flex flex-1 min-h-0 flex-col gap-3 overflow-y-auto p-3 md:grid md:grid-cols-[280px_minmax(0,1fr)] md:gap-4 md:overflow-hidden md:p-6 lg:grid-cols-[320px_minmax(0,1fr)]">
        <SquadDetailInspector
          squad={squad}
          memberCount={members.length}
          leaderName={getEntityName("agent", squad.leader_id)}
          creatorName={getEntityName("member", squad.creator_id)}
          canManage={canManage}
          onUploadAvatar={(url) => updateSquadMut.mutateAsync({ avatar_url: url })}
          onRename={async (next) => { await updateSquadMut.mutateAsync({ name: next.trim() }); }}
          onUpdateDescription={async (next) => { await updateSquadMut.mutateAsync({ description: next }); }}
        />

        <SquadOverviewPane
          squad={squad}
          members={members}
          memberStatusById={memberStatusById}
          canManage={canManage}
          isLeader={isLeader}
          isArchived={isArchived}
          getEntityName={getEntityName}
          onAddMemberClick={() => setShowAddMember(true)}
          createAgentHref={canManage ? `${p.newAgent()}?squad=${encodeURIComponent(squadId)}` : undefined}
          onSetLeader={(id) => setLeaderMut.mutate(id)}
          onRemoveMember={(m) => removeMemberMut.mutate(m)}
          onUpdateRole={async (m, role) => { await updateRoleMut.mutateAsync({ member: m, role }); }}
          onSaveInstructions={async (next) => { await updateSquadMut.mutateAsync({ instructions: next }); toast.success(t(($) => $.toasts.instructions_saved)); }}
          setLeaderPending={setLeaderMut.isPending}
        />
      </div>

      {showAddMember && (
        <AddMemberDialog
          availableMembers={availableMembers}
          availableAgents={availableAgents}
          onClose={() => setShowAddMember(false)}
          onSubmit={async (input) => { await addMemberMut.mutateAsync(input); }}
        />
      )}

      {confirmArchive && (
        <AlertDialog
          open
          onOpenChange={(v) => { if (!v && !deleteMut.isPending) setConfirmArchive(false); }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t(($) => $.archive_dialog.title)}</AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.archive_dialog.description, { name: squad.name })}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={deleteMut.isPending}>
                {t(($) => $.archive_dialog.cancel)}
              </AlertDialogCancel>
              <AlertDialogAction
                onClick={() => deleteMut.mutate()}
                disabled={deleteMut.isPending}
                className="bg-destructive text-white hover:bg-destructive/90"
              >
                {deleteMut.isPending
                  ? t(($) => $.archive_dialog.archiving)
                  : t(($) => $.archive_dialog.confirm)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}

// 首屏加载骨架——对齐加载后页面的双栏布局（左检查器 + 右标签面板），让切换
// 到真实内容时版面不跳动。列宽与下面 md:/lg: 断点一致。
function SquadDetailSkeleton() {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader>
        <Skeleton className="h-5 w-48" />
      </PageHeader>
      <div className="flex flex-1 min-h-0 flex-col gap-3 overflow-y-auto p-3 md:grid md:grid-cols-[280px_minmax(0,1fr)] md:gap-4 md:overflow-hidden md:p-6 lg:grid-cols-[320px_minmax(0,1fr)]">
        <div className="flex flex-col gap-4 rounded-lg border p-5">
          <Skeleton className="h-16 w-16 rounded-full" />
          <Skeleton className="h-5 w-40" />
          <Skeleton className="h-3 w-full" />
          <div className="space-y-2">
            <Skeleton className="h-3 w-3/4" />
            <Skeleton className="h-3 w-2/3" />
            <Skeleton className="h-3 w-1/2" />
          </div>
        </div>
        <div className="flex flex-col gap-4 rounded-lg border p-6">
          <div className="flex items-center gap-4">
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-4 w-24" />
          </div>
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-5/6" />
          <Skeleton className="h-4 w-4/6" />
        </div>
      </div>
    </div>
  );
}

// 页面标题旁展示的 16px 紧凑头像。未设置自定义头像时退回 Users 图标，让
// 小队在面包屑条里仍有一个可辨识的图形。
function SquadHeaderAvatar({ squad, initials }: { squad: Squad; initials: string }) {
  if (!squad.avatar_url) {
    return <Users className="h-4 w-4 text-muted-foreground" />;
  }
  return (
    <ActorAvatarBase
      name={squad.name}
      initials={initials}
      avatarUrl={resolvePublicFileUrl(squad.avatar_url)}
      size="sm"
      className="shrink-0"
    />
  );
}

// 给无法管理小队的人看的只读 64px 头像——与可编辑控件的静止态视觉一致，
// 但不带点击/上传交互。
function SquadStaticAvatar({ squad, initials }: { squad: Squad; initials: string }) {
  return (
    <div className="h-16 w-16 shrink-0 overflow-hidden rounded-full bg-muted">
      {squad.avatar_url ? (
        <ActorAvatarBase
          name={squad.name}
          initials={initials}
          avatarUrl={resolvePublicFileUrl(squad.avatar_url)}
          size="2xl"
        />
      ) : (
        <div className="flex h-full w-full items-center justify-center text-muted-foreground">
          <Users className="h-7 w-7" />
        </div>
      )}
    </div>
  );
}

// 内联名称编辑器——悬停时露出 Pencil 图标，点击打开带单行输入框的小弹层。
// 与 agent 检查器里的 NameAndDescription 编辑器保持一致。
function SquadNameEditor({
  value,
  onSave,
}: {
  value: string;
  onSave: (next: string) => Promise<void>;
}) {
  const { t } = useT("squads");
  return (
    <InlineEditPopover
      value={value}
      onSave={onSave}
      title={t(($) => $.name_editor.title)}
      placeholder={t(($) => $.name_editor.placeholder)}
      validate={(v) =>
        v.trim().length > 0 ? null : t(($) => $.name_editor.required)
      }
    >
      {(triggerProps) => (
        <button
          type="button"
          {...triggerProps}
          className="group -mx-1 inline-flex items-center gap-1.5 self-start rounded px-1 text-left text-title font-semibold leading-tight transition-colors hover:bg-accent/50"
        >
          <span>{value}</span>
          <Pencil className="h-3.5 w-3.5 shrink-0 text-transparent transition-colors group-hover:text-muted-foreground" />
        </button>
      )}
    </InlineEditPopover>
  );
}

/**
 * 通用的行内编辑弹层：由子元素渲染触发器按钮，打开带单行输入框的小弹层。
 * 校验失败停在原处，内容未变直接关闭，保存成功/失败用 toast 提示。
 */
function InlineEditPopover({
  value,
  onSave,
  title,
  placeholder,
  validate,
  children,
}: {
  value: string;
  onSave: (next: string) => Promise<void>;
  title: string;
  placeholder?: string;
  validate?: (v: string) => string | null;
  children: (triggerProps: { onClick: (e: React.MouseEvent) => void }) => ReactNode;
}) {
  const { t } = useT("squads");
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(value);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setDraft(value);
      setError(null);
    }
  }, [open, value]);

  const commit = async () => {
    const err = validate?.(draft) ?? null;
    if (err) {
      setError(err);
      return;
    }
    if (draft === value) {
      setOpen(false);
      return;
    }
    setSaving(true);
    try {
      await onSave(draft);
      setOpen(false);
      toast.success(t(($) => $.name_editor.saved));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.name_editor.save_failed));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={children({ onClick: () => setOpen(true) }) as React.ReactElement}
      />
      <PopoverContent align="start" className="w-72 p-3">
        <div className="space-y-2">
          <p className="text-caption font-medium">{title}</p>
          <Input
            autoFocus
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              if (error) setError(null);
            }}
            placeholder={placeholder}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setOpen(false);
                return;
              }
              if (isImeComposing(e)) return;
              if (e.key === "Enter") {
                e.preventDefault();
                void commit();
              }
            }}
            className="h-8"
          />
          {error && <p className="text-caption text-destructive">{error}</p>}
          <div className="flex items-center justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)} disabled={saving}>
              {t(($) => $.name_editor.cancel)}
            </Button>
            <Button size="sm" onClick={() => void commit()} disabled={saving || draft === value}>
              {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : t(($) => $.name_editor.save)}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

// 两步式「添加成员」弹窗（对齐 CreateAgentDialog 的紧凑布局）：
// 1) 选目标——成员 + agents 合在一个可搜索弹层里，每行带头像，视觉识别
//    与 issue 指派人的选择器一致；
// 2) 可选地描述该成员在本小队里承担的角色。角色框放在这里（而不是选择器
//    上），因为角色是加入时的、属于特定小队的上下文。
function AddMemberDialog({
  availableMembers,
  availableAgents,
  onClose,
  onSubmit,
}: {
  availableMembers: MemberWithUser[];
  availableAgents: Agent[];
  onClose: () => void;
  onSubmit: (input: { type: "agent" | "member"; id: string; role?: string }) => Promise<void>;
}) {
  const { t } = useT("squads");
  const [target, setTarget] = useState<{ type: "agent" | "member"; id: string; name: string } | null>(null);
  const [role, setRole] = useState("");
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerFilter, setPickerFilter] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const query = pickerFilter.trim().toLowerCase();
  const filteredMembers = availableMembers.filter((m) => m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query));
  const filteredAgents = availableAgents.filter((a) => a.name.toLowerCase().includes(query) || matchesPinyin(a.name, query));

  const canSubmit = !!target && !submitting;

  const handleSubmit = async () => {
    if (!target) return;
    setSubmitting(true);
    try {
      await onSubmit({ type: target.type, id: target.id, role });
      onClose();
    } catch {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.add_member_dialog.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.add_member_dialog.description)}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 min-w-0">
          <div>
            <Label className="text-caption text-muted-foreground">{t(($) => $.add_member_dialog.label_member)}</Label>
            <Popover open={pickerOpen} onOpenChange={(v) => { setPickerOpen(v); if (!v) setPickerFilter(""); }}>
              <PopoverTrigger className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 mt-1 text-left text-body transition-colors hover:bg-muted">
                {target ? (
                  <ActorAvatar actorType={target.type} actorId={target.id} size="sm" />
                ) : (
                  <UserPlus className="h-4 w-4 shrink-0 text-muted-foreground" />
                )}
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium">
                    {target?.name ?? t(($) => $.add_member_dialog.select_target)}
                  </div>
                  {target && (
                    <div className="truncate text-caption text-muted-foreground">
                      {t(($) => $.member_type[target.type])}
                    </div>
                  )}
                </div>
                <ChevronDown className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${pickerOpen ? "rotate-180" : ""}`} />
              </PopoverTrigger>
              <PopoverContent align="start" className="w-[var(--anchor-width)] p-0">
                <div className="px-2 py-1.5 border-b">
                  <input
                    autoFocus
                    type="text"
                    value={pickerFilter}
                    onChange={(e) => setPickerFilter(e.target.value)}
                    placeholder={t(($) => $.add_member_dialog.search_placeholder)}
                    className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
                  />
                </div>
                <div className="p-1 max-h-72 overflow-y-auto">
                  {filteredMembers.length > 0 && (
                    <PickerSection label={t(($) => $.add_member_dialog.members_section)}>
                      {filteredMembers.map((m) => (
                        <PickerItem
                          key={m.user_id}
                          selected={target?.type === "member" && target.id === m.user_id}
                          onClick={() => {
                            setTarget({ type: "member", id: m.user_id, name: m.name });
                            setPickerOpen(false);
                            setPickerFilter("");
                          }}
                        >
                          <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                          <span>{m.name}</span>
                        </PickerItem>
                      ))}
                    </PickerSection>
                  )}
                  {filteredAgents.length > 0 && (
                    <PickerSection label={t(($) => $.add_member_dialog.agents_section)}>
                      {filteredAgents.map((a) => (
                        <PickerItem
                          key={a.id}
                          selected={target?.type === "agent" && target.id === a.id}
                          onClick={() => {
                            setTarget({ type: "agent", id: a.id, name: a.name });
                            setPickerOpen(false);
                            setPickerFilter("");
                          }}
                        >
                          <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                          <span>{a.name}</span>
                        </PickerItem>
                      ))}
                    </PickerSection>
                  )}
                  {filteredMembers.length === 0 && filteredAgents.length === 0 && <PickerEmpty />}
                </div>
              </PopoverContent>
            </Popover>
          </div>

          <div>
            <Label className="text-caption text-muted-foreground">
              {t(($) => $.add_member_dialog.label_role)}{" "}
              <span className="text-muted-foreground">{t(($) => $.add_member_dialog.label_optional)}</span>
            </Label>
            <Input
              type="text"
              value={role}
              onChange={(e) => setRole(e.target.value)}
              placeholder={t(($) => $.add_member_dialog.role_placeholder)}
              className="mt-1"
              onKeyDown={(e) => {
                if (isImeComposing(e)) return;
                if (e.key === "Enter" && canSubmit) void handleSubmit();
              }}
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>{t(($) => $.add_member_dialog.cancel)}</Button>
          <Button onClick={() => void handleSubmit()} disabled={!canSubmit}>
            {submitting ? <Loader2 className="size-3.5 animate-spin" /> : t(($) => $.add_member_dialog.add)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// 行内点击即改的角色编辑行。把当前角色渲染成弱化文字；点击（或角色为空时
// 点占位符）切换成输入框，失焦 / 回车提交、Esc 取消。避免为一个通常只有
// 一两个词的小改动打开弹窗。
function RoleEditor({ value, onSave }: { value: string; onSave: (next: string) => Promise<void> }) {
  const { t } = useT("squads");
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  const [saving, setSaving] = useState(false);

  useEffect(() => { if (!editing) setDraft(value); }, [value, editing]);

  const commit = async () => {
    const next = draft.trim();
    if (next === value.trim()) { setEditing(false); return; }
    setSaving(true);
    try {
      await onSave(next);
      setEditing(false);
    } catch {
      // 错误 toast 由外层 mutation 统一处理
    } finally {
      setSaving(false);
    }
  };

  if (editing) {
    return (
      <Input
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => void commit()}
        onKeyDown={(e) => {
          if (isImeComposing(e)) return;
          if (e.key === "Enter") void commit();
          else if (e.key === "Escape") { setDraft(value); setEditing(false); }
        }}
        disabled={saving}
        placeholder={t(($) => $.role_editor.placeholder)}
        className="h-6 mt-0.5 text-caption px-1.5"
      />
    );
  }

  return (
    <button
      type="button"
      onClick={() => setEditing(true)}
      className="text-caption text-muted-foreground mt-0.5 text-left hover:text-foreground transition-colors"
    >
      {value || <span className="italic opacity-60">{t(($) => $.role_editor.empty)}</span>}
    </button>
  );
}

// ---------------------------------------------------------------------------
// SquadDetailInspector——左侧 320px 列，对齐 AgentDetailInspector。
// 承载身份（头像 / 名称 / 简介）+ 负责人 / 成员数 / 时间戳。全部可内联编辑。
// ---------------------------------------------------------------------------
function SquadDetailInspector({
  squad,
  memberCount,
  leaderName,
  creatorName,
  canManage,
  onUploadAvatar,
  onRename,
  onUpdateDescription,
}: {
  squad: Squad;
  memberCount: number;
  leaderName: string;
  creatorName: string;
  // 为 false 时身份块渲染成纯文本（不上传头像、不弹名称/简介编辑）——
  // 访客可读小队但不能编辑。与 agent 检查器的 `canEdit` 只读处理一致。
  canManage: boolean;
  onUploadAvatar: (url: string) => Promise<unknown>;
  onRename: (next: string) => Promise<void>;
  onUpdateDescription: (next: string) => Promise<void>;
}) {
  const { t } = useT("squads");
  const timeAgo = useTimeAgo();
  const initials = squad.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  return (
    <aside className="flex w-full flex-col rounded-lg border bg-background md:h-full md:min-h-0 md:overflow-y-auto">
      {/* 身份区块 */}
      <div className="flex flex-col gap-3 border-b px-5 pb-5 pt-5">
        {canManage ? (
          <>
            <AvatarUploadControl
              variant="squad"
              value={squad.avatar_url ?? null}
              name={squad.name}
              size={64}
              onUploaded={onUploadAvatar}
            />
            <div className="flex flex-col gap-1">
              <SquadNameEditor value={squad.name} onSave={onRename} />
              <SquadDescriptionEditor
                value={squad.description ?? ""}
                onSave={onUpdateDescription}
              />
            </div>
          </>
        ) : (
          <>
            <SquadStaticAvatar squad={squad} initials={initials} />
            <div className="flex flex-col gap-1">
              <span className="text-title font-semibold leading-tight">{squad.name}</span>
              {squad.description ? (
                <span className="text-caption leading-relaxed text-muted-foreground">
                  {squad.description}
                </span>
              ) : (
                <span className="text-caption italic leading-relaxed text-muted-foreground">
                  {t(($) => $.description_dialog.placeholder_empty)}
                </span>
              )}
            </div>
          </>
        )}
      </div>

      {/* 详情（只读） */}
      <div className="border-b px-5 py-4">
        <div className="mb-1 -mx-2 px-2 text-micro font-medium uppercase tracking-wider text-muted-foreground">
          {t(($) => $.inspector.details_section)}
        </div>
        <div className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5">
          <InspectorRow label={t(($) => $.details.leader)}>
            <span className="flex min-w-0 items-center gap-1.5">
              <ActorAvatar actorType="agent" actorId={squad.leader_id} size="xs" />
              <span className="truncate">{leaderName}</span>
            </span>
          </InspectorRow>
          <InspectorRow label={t(($) => $.details.members)}>
            <span className="text-muted-foreground tabular-nums">{memberCount}</span>
          </InspectorRow>
          <InspectorRow label={t(($) => $.details.created_by)}>
            <span className="flex min-w-0 items-center gap-1.5">
              <ActorAvatar actorType="member" actorId={squad.creator_id} size="xs" />
              <span className="truncate">{creatorName}</span>
            </span>
          </InspectorRow>
          <InspectorRow label={t(($) => $.details.created)}>
            <span className="text-muted-foreground">{timeAgo(squad.created_at)}</span>
          </InspectorRow>
          <InspectorRow label={t(($) => $.details.updated)}>
            <span className="text-muted-foreground">{timeAgo(squad.updated_at)}</span>
          </InspectorRow>
        </div>
      </div>
    </aside>
  );
}

/** 检查器里一行「标签：值」的布局辅助。 */
function InspectorRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <div className="px-2 py-1 text-caption text-muted-foreground">{label}</div>
      <div className="min-w-0 px-2 py-1 text-caption">{children}</div>
    </>
  );
}

// 检查器里点击即改的简介编辑器。对齐 agent-detail-inspector 的
// DescriptionEditor：打开一个带 textarea 的弹窗（给多段简介留足空间）；
// 行内触发器展示当前值（或占位文案），悬停露出 Pencil 图标。
function SquadDescriptionEditor({
  value,
  onSave,
}: {
  value: string;
  onSave: (next: string) => Promise<void>;
}) {
  const { t } = useT("squads");
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="group -mx-1 inline-flex items-start gap-1.5 self-start rounded px-1 text-left text-caption leading-relaxed transition-colors hover:bg-accent/50"
      >
        {value ? (
          <span className="text-muted-foreground">{value}</span>
        ) : (
          <span className="italic text-muted-foreground">{t(($) => $.description_dialog.placeholder_empty)}</span>
        )}
        <Pencil className="mt-0.5 h-3 w-3 shrink-0 text-transparent transition-colors group-hover:text-muted-foreground" />
      </button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          {open && (
            <SquadDescriptionEditorBody
              initialValue={value}
              onSave={onSave}
              onClose={() => setOpen(false)}
            />
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

/** 简介编辑弹窗的实际内容：textarea + 保存/取消，Cmd/Ctrl+Enter 快捷提交。 */
function SquadDescriptionEditorBody({
  initialValue,
  onSave,
  onClose,
}: {
  initialValue: string;
  onSave: (next: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useT("squads");
  const [draft, setDraft] = useState(initialValue);
  const [saving, setSaving] = useState(false);
  const savingRef = useRef(false);
  const dirty = draft !== initialValue;

  const commit = async () => {
    if (savingRef.current) return;
    if (!dirty) { onClose(); return; }
    savingRef.current = true;
    setSaving(true);
    try {
      await onSave(draft);
      onClose();
    } catch {
      // 错误 toast 由父级 mutation 处理
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t(($) => $.description_dialog.title)}</DialogTitle>
      </DialogHeader>
      <textarea
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        placeholder={t(($) => $.description_dialog.responsibility_placeholder)}
        rows={6}
        onKeyDown={(e) => {
          if (e.key === "Escape") { onClose(); return; }
          if (e.defaultPrevented || e.repeat || isImeComposing(e)) return;
          if (shortcutMatchesEvent(getShortcut("send"), e.nativeEvent)) {
            e.preventDefault();
            void commit();
          }
        }}
        className="w-full resize-none rounded-md border bg-transparent px-3 py-2 text-body outline-none focus-visible:border-input"
      />
      <DialogFooter>
        <Button variant="ghost" size="sm" onClick={onClose} disabled={saving}>{t(($) => $.description_dialog.cancel)}</Button>
        <Button size="sm" onClick={() => void commit()} disabled={saving || !dirty}>
          {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : t(($) => $.description_dialog.save)}
        </Button>
      </DialogFooter>
    </>
  );
}

// ---------------------------------------------------------------------------
// SquadOverviewPane——右栏，带两个标签页（成员 | Instructions）。
// 对齐 AgentOverviewPane：Instructions 未保存时切换标签页会先弹 AlertDialog
// 确认（脏数据守卫）。
// ---------------------------------------------------------------------------
type SquadDetailTab = "members" | "instructions";

const squadDetailTabs: { id: SquadDetailTab; icon: typeof FileText }[] = [
  { id: "members", icon: Users },
  { id: "instructions", icon: FileText },
];

/**
 * 右栏主面板：管理「成员 | Instructions」两个标签页的切换，并在切走含未保存
 * 编辑的标签时弹脏数据确认框。
 */
function SquadOverviewPane({
  squad,
  members,
  memberStatusById,
  canManage,
  isLeader,
  isArchived,
  getEntityName,
  onAddMemberClick,
  createAgentHref,
  onSetLeader,
  onRemoveMember,
  onUpdateRole,
  onSaveInstructions,
  setLeaderPending,
}: {
  squad: Squad;
  members: SquadMember[];
  memberStatusById: Map<string, SquadMemberStatus>;
  // 把关 Members 与 Instructions 两个标签页里所有会改数据的控件。为 false
  // 时标签页只读（没有增/删/负责人/角色编辑，也没有保存按钮）。
  // 见 server/internal/handler/squad.go 的 canManageSquad。
  canManage: boolean;
  isLeader: (m: SquadMember) => boolean;
  isArchived: (m: SquadMember) => boolean;
  getEntityName: (type: string, id: string) => string;
  onAddMemberClick: () => void;
  // 可选——只有当前用户能管理该小队（工作区 owner/admin 或创建者）时才传入。
  // 否则隐藏，避免访客看到一个点了也没用的按钮。
  createAgentHref?: string;
  onSetLeader: (agentId: string) => void;
  onRemoveMember: (m: SquadMember) => void;
  onUpdateRole: (m: SquadMember, role: string) => Promise<void>;
  onSaveInstructions: (next: string) => Promise<void>;
  setLeaderPending: boolean;
}) {
  const { t } = useT("squads");
  const [activeTab, setActiveTab] = useState<SquadDetailTab>("members");
  const [activeDirty, setActiveDirty] = useState(false);
  const [pendingTab, setPendingTab] = useState<SquadDetailTab | null>(null);

  const requestTabChange = (next: SquadDetailTab) => {
    if (next === activeTab) return;
    if (activeDirty) { setPendingTab(next); return; }
    setActiveTab(next);
  };

  const commitTabChange = () => {
    if (pendingTab) {
      setActiveTab(pendingTab);
      setActiveDirty(false);
      setPendingTab(null);
    }
  };

  return (
    <div className="flex min-h-[60vh] flex-col overflow-hidden rounded-lg border bg-background md:h-full md:min-h-0">
      <div className="flex shrink-0 items-center gap-0 overflow-x-auto border-b px-2 md:px-4">
        {squadDetailTabs.map((tab) => (
          <button
            key={tab.id}
            type="button"
            onClick={() => requestTabChange(tab.id)}
            className={`flex shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2.5 text-caption font-medium transition-colors ${
              activeTab === tab.id
                ? "border-foreground text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            <tab.icon className="h-3.5 w-3.5" />
            {t(($) => $.detail_tabs[tab.id])}
          </button>
        ))}
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto">
        {activeTab === "members" && (
          <div className="flex h-full flex-col p-4 md:p-6">
            <SquadMembersTab
              members={members}
              memberStatusById={memberStatusById}
              canManage={canManage}
              isLeader={isLeader}
              isArchived={isArchived}
              getEntityName={getEntityName}
              onAddMemberClick={onAddMemberClick}
              createAgentHref={createAgentHref}
              onSetLeader={onSetLeader}
              onRemoveMember={onRemoveMember}
              onUpdateRole={onUpdateRole}
              setLeaderPending={setLeaderPending}
            />
          </div>
        )}
        {activeTab === "instructions" && (
          <div className="flex h-full flex-col p-4 md:p-6">
            <SquadInstructionsTab
              squad={squad}
              canManage={canManage}
              onSave={onSaveInstructions}
              onDirtyChange={setActiveDirty}
            />
          </div>
        )}
      </div>

      {pendingTab !== null && (
        <AlertDialog open onOpenChange={(v) => { if (!v) setPendingTab(null); }}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t(($) => $.discard_changes_dialog.title)}</AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.discard_changes_dialog.description)}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t(($) => $.discard_changes_dialog.keep_editing)}</AlertDialogCancel>
              <AlertDialogAction variant="destructive" onClick={commitTabChange}>
                {t(($) => $.discard_changes_dialog.discard_button)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}

// 五种小队成员状态的视觉配置。对齐 packages/views/agents/presence.ts 里的
// availabilityConfig + workloadConfig——用同一套语义色，让这里的状态点与
// agent 页面一致。未知 / 空状态（人类成员、服务端枚举漂移）渲染成中性灰
// 药丸；这是 CLAUDE.md > API Response Compatibility 里「降级而不是崩溃」的
// 防御手段。
const SQUAD_STATUS_DOT_CLASS: Record<SquadMemberStatusValue, string> = {
  working: "bg-success",
  idle: "bg-muted-foreground/40",
  offline: "bg-muted-foreground/40",
  unstable: "bg-warning",
  archived: "bg-muted-foreground/40",
};

// 成员标签页主体——复用现有的列表 / 角色编辑模式。
function SquadMembersTab({
  members,
  memberStatusById,
  canManage,
  isLeader,
  isArchived,
  getEntityName,
  onAddMemberClick,
  createAgentHref,
  onSetLeader,
  onRemoveMember,
  onUpdateRole,
  setLeaderPending,
}: {
  members: SquadMember[];
  memberStatusById: Map<string, SquadMemberStatus>;
  // 为 false 时，新增/创建/负责人/移除控件与角色编辑全部隐藏；名册保持
  // 可见但只读。
  canManage: boolean;
  isLeader: (m: SquadMember) => boolean;
  isArchived: (m: SquadMember) => boolean;
  getEntityName: (type: string, id: string) => string;
  onAddMemberClick: () => void;
  // 不能管理的访客看不到——见 SquadOverviewPane。
  createAgentHref?: string;
  onSetLeader: (agentId: string) => void;
  onRemoveMember: (m: SquadMember) => void;
  onUpdateRole: (m: SquadMember, role: string) => Promise<void>;
  setLeaderPending: boolean;
}) {
  const { t } = useT("squads");
  const timeAgo = useTimeAgo();
  const p = useWorkspacePaths();
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-body font-medium">{t(($) => $.members_tab.section_title)}</h3>
          <p className="text-caption text-muted-foreground mt-0.5">
            {t(($) => $.members_tab.section_count, { count: members.length })}
          </p>
        </div>
        {canManage && (
          <div className="flex items-center gap-2">
            {createAgentHref && (
              <Button
                size="sm"
                variant="outline"
                render={<AppLink href={createAgentHref} />}
                nativeButton={false}
              >
                <Plus className="size-3.5 mr-1.5" />
                {t(($) => $.members_tab.create_agent_button)}
              </Button>
            )}
            <Button size="sm" variant="outline" onClick={onAddMemberClick}>
              <Plus className="size-3.5 mr-1.5" />
              {t(($) => $.members_tab.add_member_button)}
            </Button>
          </div>
        )}
      </div>

      <div className="space-y-2">
        {members.map((m) => {
          const status = memberStatusById.get(m.member_id);
          const statusValue = status?.status ?? null;
          const dotClass =
            statusValue && statusValue in SQUAD_STATUS_DOT_CLASS
              ? SQUAD_STATUS_DOT_CLASS[statusValue as keyof typeof SQUAD_STATUS_DOT_CLASS]
              : null;
          const statusLabel =
            statusValue === "working" ? t(($) => $.members_tab.status_working)
              : statusValue === "idle" ? t(($) => $.members_tab.status_idle)
              : statusValue === "offline" ? t(($) => $.members_tab.status_offline)
              : statusValue === "unstable" ? t(($) => $.members_tab.status_unstable)
              : statusValue === "archived" ? t(($) => $.members_tab.status_archived)
              : null;
          const activeIssues = status?.active_issues ?? [];
          const primaryIssue = activeIssues[0];
          const extraIssueCount = Math.max(0, activeIssues.length - 1);
          // 只在 agent 非「工作中」时展示 last_active——「工作中」药丸本身
          // 就说明 agent 在线，旁边再放一行「2 秒前活跃」纯属噪音。
          const showLastActive =
            m.member_type === "agent" && statusValue && statusValue !== "working" && status?.last_active_at;
          return (
            <div key={m.id} className="group flex items-start gap-3 rounded-lg border p-3">
              <ActorAvatar
                actorType={m.member_type}
                actorId={m.member_id}
                size="lg"
                showStatusDot
                enableHoverCard={m.member_type === "agent"}
                hoverCardVariant="live"
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-body font-medium">{getEntityName(m.member_type, m.member_id)}</span>
                  <span className="text-caption text-muted-foreground">
                    {t(($) => $.member_type[m.member_type])}
                  </span>
                  {isLeader(m) && (
                    <span className="inline-flex items-center gap-0.5 text-caption bg-amber-100 dark:bg-amber-900/30 text-amber-700 dark:text-amber-400 px-1.5 py-0.5 rounded">
                      <Crown className="size-3" />
                      {t(($) => $.members_tab.leader_chip)}
                    </span>
                  )}
                  {m.member_type === "agent" && statusLabel && (
                    <span className="inline-flex items-center gap-1 text-caption text-muted-foreground">
                      <span className={`h-1.5 w-1.5 rounded-full ${dotClass ?? "bg-muted-foreground/40"}`} />
                      {statusLabel}
                    </span>
                  )}
                </div>
                {canManage ? (
                  <RoleEditor
                    value={m.role ?? ""}
                    onSave={async (next) => { await onUpdateRole(m, next); }}
                  />
                ) : m.role ? (
                  <div className="mt-0.5 text-caption text-muted-foreground">{m.role}</div>
                ) : null}
                {primaryIssue && (
                  <div className="mt-1 flex items-center gap-1 text-caption text-muted-foreground min-w-0">
                    <AppLink
                      href={p.issueDetail(primaryIssue.issue_id)}
                      className="inline-flex items-center gap-1 min-w-0 hover:text-foreground transition-colors"
                    >
                      <span className="font-mono text-micro uppercase shrink-0">{primaryIssue.identifier}</span>
                      <span className="truncate">{primaryIssue.title}</span>
                      {primaryIssue.issue_status === "blocked" && (
                        <span className="shrink-0 inline-flex items-center text-micro uppercase tracking-wide text-warning">
                          {t(($) => $.members_tab.issue_status_blocked)}
                        </span>
                      )}
                    </AppLink>
                    {extraIssueCount > 0 && (
                      <span className="shrink-0">
                        · {t(($) => $.members_tab.active_issue_more, { count: extraIssueCount })}
                      </span>
                    )}
                  </div>
                )}
                {showLastActive && (
                  <div className="mt-0.5 text-caption text-muted-foreground">
                    {t(($) => $.members_tab.last_active_label, {
                      time: timeAgo(status!.last_active_at!),
                    })}
                  </div>
                )}
              </div>
            <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity">
              {m.member_type === "agent" && (
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <AppLink
                        href={p.agentDetail(m.member_id)}
                        className="inline-flex items-center justify-center h-8 w-8 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
                        aria-label={t(($) => $.members_tab.view_agent_tooltip)}
                      >
                        <ArrowUpRight className="size-3.5" />
                      </AppLink>
                    }
                  />
                  <TooltipContent>
                    {t(($) => $.members_tab.view_agent_tooltip)}
                  </TooltipContent>
                </Tooltip>
              )}
              {canManage && m.member_type === "agent" && !isLeader(m) && !isArchived(m) && (
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-muted-foreground hover:text-amber-600 h-8 w-8 p-0"
                        onClick={() => onSetLeader(m.member_id)}
                        disabled={setLeaderPending}
                        aria-label={t(($) => $.members_tab.make_leader_tooltip)}
                      >
                        <Crown className="size-3.5" />
                      </Button>
                    }
                  />
                  <TooltipContent>
                    {t(($) => $.members_tab.make_leader_tooltip)}
                  </TooltipContent>
                </Tooltip>
              )}
              {canManage && !isLeader(m) && (
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-muted-foreground hover:text-destructive h-8 w-8 p-0"
                        onClick={() => onRemoveMember(m)}
                        aria-label={t(($) => $.members_tab.remove_member_tooltip)}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    }
                  />
                  <TooltipContent>
                    {t(($) => $.members_tab.remove_member_tooltip)}
                  </TooltipContent>
                </Tooltip>
              )}
            </div>
          </div>
          );
        })}
      </div>
    </div>
  );
}

// Instructions 标签页主体——对齐 agent 的 InstructionsTab。ContentEditor +
// 保存按钮。小队 leader 领取任务时会读取这里的提示词
// （server/internal/handler/daemon.go）。
function SquadInstructionsTab({
  squad,
  canManage,
  onSave,
  onDirtyChange,
}: {
  squad: Squad;
  canManage: boolean;
  onSave: (instructions: string) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("squads");
  const [value, setValue] = useState(squad.instructions ?? "");
  const [saving, setSaving] = useState(false);
  const isDirty = value !== (squad.instructions ?? "");

  useEffect(() => {
    setValue(squad.instructions ?? "");
  }, [squad.id, squad.instructions]);

  useEffect(() => {
    // 只读访客永远没有未保存的修改需要在切标签时守卫。
    onDirtyChange?.(canManage && isDirty);
  }, [canManage, isDirty, onDirtyChange]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave(value);
    } catch {
      // 错误 toast 由父级处理
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex h-full flex-col gap-4">
      <p className="text-caption text-muted-foreground">
        {t(($) => $.instructions_tab.description)}
      </p>

      {/* 访客不能管理小队时，编辑器外面包一层 pointer-events-none /
          aria-disabled 外壳——ContentEditor 只在挂载时读 `editable`，之后
          无法切换，所以这是把它做成只读的既定方式（见 editor/content-editor.tsx）。 */}
      <div
        aria-disabled={!canManage}
        className={`flex-1 min-h-0 overflow-y-auto rounded-md border bg-background px-4 py-3 transition-colors ${
          canManage ? "focus-within:border-input" : "pointer-events-none"
        }`}
      >
        <ContentEditor
          key={squad.id}
          value={value}
          onUpdate={canManage ? setValue : () => {}}
          placeholder={
            canManage
              ? t(($) => $.instructions_tab.placeholder)
              : ""
          }
          debounceMs={150}
          disableMentions
          className="min-h-full"
        />
      </div>

      {canManage && (
        <div className="flex items-center justify-end gap-3">
          {isDirty && (
            <span className="text-caption text-muted-foreground">{t(($) => $.instructions_tab.unsaved_changes)}</span>
          )}
          <Button size="sm" onClick={handleSave} disabled={!isDirty || saving}>
            {saving ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Save className="h-3.5 w-3.5" />
            )}
            {t(($) => $.instructions_tab.save_button)}
          </Button>
        </div>
      )}
    </div>
  );
}

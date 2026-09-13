// 本文件职责：「创建小队（Squad）」弹窗——填写小队名称、描述、头像，从「我的 Agent /
// 工作区 Agent」里单选一名队长（Leader），并可多选其它 Agent 与工作区成员加入小队，
// 创建成功跳转小队详情页。
"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, UserPlus, X } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentListOptions,
  memberListOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { AGENT_DESCRIPTION_MAX_LENGTH } from "@multica/core/agents";
import { isImeComposing } from "@multica/core/utils";
import type { Agent, MemberWithUser } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { toast } from "sonner";

import { useNavigation } from "../navigation";
import { ActorAvatar } from "../common/actor-avatar";
import { AvatarUploadControl } from "../common/avatar-upload-control";
import { CharCounter } from "../agents/components/char-counter";
import {
  PickerEmpty,
  PickerItem,
  PickerSection,
} from "../issues/components/pickers/property-picker";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import { useT } from "../i18n";

// 一个「额外成员」条目：要么是 agent，要么是工作区成员（member），各带自己的 id 和名字。
type SelectedMember = {
  type: "agent" | "member";
  id: string;
  name: string;
};

// 触发按钮内联展示的成员胶囊上限，超出部分折叠成「+N」。
const CHIP_DISPLAY_LIMIT = 3;

/**
 * 「创建小队」弹窗主组件。
 *
 * 收集小队名称/描述/头像，用 LeaderPicker 单选队长、用 AdditionalMembersPicker
 * 多选成员；提交时先建小队，再逐个添加成员（单条失败只警告不中断），随后刷新
 * 小队相关缓存并跳转到小队详情页。队长必须是一个「在线」（绑定了 runtime）的 agent。
 *
 * @param onClose 关闭弹窗的回调
 */
export function CreateSquadModal({ onClose }: { onClose: () => void }) {
  const { t } = useT("modals");
  const router = useNavigation();
  const wsPaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const currentUser = useAuthStore((s) => s.user);
  const currentUserId = currentUser?.id ?? null;

  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: wsMembers = [] } = useQuery(memberListOptions(wsId));

  const activeAgents = useMemo(
    () => agents.filter((a: Agent) => !a.archived_at && a.runtime_id),
    [agents],
  );

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [avatarUrl, setAvatarUrl] = useState<string | null>(null);
  const [leaderId, setLeaderId] = useState("");
  const [selectedMembers, setSelectedMembers] = useState<SelectedMember[]>([]);
  const [creating, setCreating] = useState(false);

  // 把某 agent 升为队长时，必须真正把它从已选成员里移除，而不是仅仅隐藏掉——
  // 否则之后把队长换成别人时，这个「被隐藏的选中项」会复活，并作为普通成员静默提交。
  const handleLeaderChange = (id: string) => {
    setLeaderId(id);
    if (id) {
      setSelectedMembers((prev) =>
        prev.filter((m) => !(m.type === "agent" && m.id === id)),
      );
    }
  };

  const canSubmit = !!name.trim() && !!leaderId && !creating;

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setCreating(true);
    try {
      const squad = await api.createSquad({
        name: name.trim(),
        description: description.trim() || undefined,
        leader_id: leaderId,
        avatar_url: avatarUrl ?? undefined,
      });
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });

      if (selectedMembers.length > 0) {
        await Promise.allSettled(
          selectedMembers.map(async (m) => {
            try {
              await api.addSquadMember(squad.id, {
                member_type: m.type,
                member_id: m.id,
              });
            } catch (err) {
              toast.warning(
                t(($) => $.create_squad.toast_member_add_failed, {
                  name: m.name,
                  error:
                    err instanceof Error ? err.message : "unknown error",
                }),
              );
            }
          }),
        );
        queryClient.invalidateQueries({
          queryKey: [...workspaceKeys.squads(wsId), squad.id, "members"],
        });
      }

      onClose();
      toast.success(t(($) => $.create_squad.toast_created));
      router.push(wsPaths.squadDetail(squad.id));
    } catch (err) {
      toast.error(
        err instanceof Error
          ? err.message
          : t(($) => $.create_squad.toast_failed),
      );
      setCreating(false);
    }
  };

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent className="p-0 gap-0 flex flex-col overflow-hidden !top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2 !w-full !max-w-2xl !h-[85vh]">
        <DialogHeader className="border-b px-5 py-3 space-y-0">
          <DialogTitle className="text-title-sm font-semibold">
            {t(($) => $.create_squad.title)}
          </DialogTitle>
          <DialogDescription className="mt-1 text-caption">
            {t(($) => $.create_squad.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex-1 overflow-y-auto p-5">
          <div className="space-y-4 min-w-0">
            {/* 身份信息行与 CreateAgentDialog 保持一致，让两个「创建」读起来像同类——
                左边头像、右边名称/描述堆叠。 */}
            <div className="flex items-start gap-4">
              <AvatarUploadControl
                variant="squad"
                value={avatarUrl}
                name={name}
                size={64}
                onUploaded={setAvatarUrl}
                onClear={() => setAvatarUrl(null)}
              />
              <div className="flex-1 min-w-0 space-y-3">
                <div>
                  <Label className="text-caption text-muted-foreground">
                    {t(($) => $.create_squad.name_label)}
                  </Label>
                  <Input
                    autoFocus
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={t(($) => $.create_squad.name_placeholder)}
                    className="mt-1"
                    onKeyDown={(e) => {
                      if (isImeComposing(e)) return;
                      if (e.key === "Enter") void handleSubmit();
                    }}
                  />
                </div>

                <div>
                  <Label className="text-caption text-muted-foreground">
                    {t(($) => $.create_squad.description_label)}
                  </Label>
                  <Input
                    type="text"
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder={t(($) => $.create_squad.description_placeholder)}
                    maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                    className="mt-1"
                  />
                  <div className="mt-1">
                    <CharCounter
                      length={[...description].length}
                      max={AGENT_DESCRIPTION_MAX_LENGTH}
                    />
                  </div>
                </div>
              </div>
            </div>

            <LeaderPicker
              agents={activeAgents}
              currentUserId={currentUserId}
              value={leaderId}
              onChange={handleLeaderChange}
            />

            <AdditionalMembersPicker
              agents={activeAgents}
              members={wsMembers}
              currentUserId={currentUserId}
              leaderId={leaderId}
              value={selectedMembers}
              onChange={setSelectedMembers}
            />
          </div>
        </div>

        {/* 内联 footer——参考 CreateAgentDialog：shadcn 的 DialogFooter 默认带负外边距，
            那是假设 DialogContent 有内边距。我们的内容是 p-0，所以用普通带边框的行即可。 */}
        <div className="flex items-center justify-end gap-2 border-t bg-background px-5 py-3">
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.create_squad.cancel)}
          </Button>
          <Button onClick={() => void handleSubmit()} disabled={!canSubmit}>
            {creating
              ? t(($) => $.create_squad.submitting)
              : t(($) => $.create_squad.submit)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// LeaderPicker —— 单选 agent 的队长选择器，分组为「我的 Agent」在前、「工作区 Agent」
// 在后。工作区里一个 agent 都没有时，渲染一个指向「去创建 agent」的禁用提示条。
// ---------------------------------------------------------------------------
function LeaderPicker({
  agents,
  currentUserId,
  value,
  onChange,
}: {
  agents: Agent[];
  currentUserId: string | null;
  value: string;
  onChange: (id: string) => void;
}) {
  const { t } = useT("modals");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const myAgents = useMemo(
    () => (currentUserId ? agents.filter((a) => a.owner_id === currentUserId) : []),
    [agents, currentUserId],
  );
  const otherAgents = useMemo(
    () =>
      currentUserId
        ? agents.filter((a) => a.owner_id !== currentUserId)
        : agents,
    [agents, currentUserId],
  );

  const q = filter.trim().toLowerCase();
  const matches = (a: Agent) =>
    !q || a.name.toLowerCase().includes(q) || matchesPinyin(a.name, q);
  const filteredMine = myAgents.filter(matches);
  const filteredOthers = otherAgents.filter(matches);

  const selected = agents.find((a) => a.id === value) ?? null;
  const noAgents = agents.length === 0;

  return (
    <div>
      <Label className="text-caption text-muted-foreground">
        {t(($) => $.create_squad.leader_label)}
      </Label>
      <p className="mt-0.5 mb-1.5 text-caption text-muted-foreground">
        {t(($) => $.create_squad.leader_hint)}
      </p>

      {noAgents ? (
        <div className="flex items-center gap-2 rounded-lg border border-dashed bg-muted/30 px-3 py-2.5 text-body text-muted-foreground">
          {t(($) => $.create_squad.no_agents)}
        </div>
      ) : (
        <Popover
          open={open}
          onOpenChange={(v) => {
            setOpen(v);
            if (!v) setFilter("");
          }}
        >
          <PopoverTrigger className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 text-left text-body transition-colors hover:bg-muted">
            {selected ? (
              <ActorAvatar actorType="agent" actorId={selected.id} size="sm" showStatusDot />
            ) : (
              <UserPlus className="h-4 w-4 shrink-0 text-muted-foreground" />
            )}
            <div className="min-w-0 flex-1">
              <div className="truncate font-medium">
                {selected?.name ?? t(($) => $.create_squad.leader_placeholder)}
              </div>
              {selected?.description && (
                <div className="truncate text-caption text-muted-foreground">
                  {selected.description}
                </div>
              )}
            </div>
            <ChevronDown
              className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
                open ? "rotate-180" : ""
              }`}
            />
          </PopoverTrigger>
          <PopoverContent align="start" className="w-[var(--anchor-width)] p-0">
            <div className="border-b px-2 py-1.5">
              <input
                autoFocus
                type="text"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder={t(($) => $.create_squad.picker_search_placeholder)}
                className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
              />
            </div>
            <div className="max-h-72 overflow-y-auto p-1">
              {filteredMine.length > 0 && (
                <PickerSection label={t(($) => $.create_squad.group_my_agents)}>
                  {filteredMine.map((a) => (
                    <PickerItem
                      key={a.id}
                      selected={value === a.id}
                      onClick={() => {
                        onChange(a.id);
                        setOpen(false);
                        setFilter("");
                      }}
                    >
                      <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                      <span className="truncate">{a.name}</span>
                    </PickerItem>
                  ))}
                </PickerSection>
              )}
              {filteredOthers.length > 0 && (
                <PickerSection label={t(($) => $.create_squad.group_workspace_agents)}>
                  {filteredOthers.map((a) => (
                    <PickerItem
                      key={a.id}
                      selected={value === a.id}
                      onClick={() => {
                        onChange(a.id);
                        setOpen(false);
                        setFilter("");
                      }}
                    >
                      <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                      <span className="truncate">{a.name}</span>
                    </PickerItem>
                  ))}
                </PickerSection>
              )}
              {filteredMine.length === 0 && filteredOthers.length === 0 && (
                <PickerEmpty />
              )}
            </div>
          </PopoverContent>
        </Popover>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// AdditionalMembersPicker —— 多选「agent + 工作区成员」的成员选择器。触发按钮内联展示
// 最多 3 个成员胶囊，其余折叠成「+N」。弹层在勾选期间保持打开，方便一次挑选多个而无需
// 反复开关。
// ---------------------------------------------------------------------------
function AdditionalMembersPicker({
  agents,
  members,
  currentUserId,
  leaderId,
  value,
  onChange,
}: {
  agents: Agent[];
  members: MemberWithUser[];
  currentUserId: string | null;
  leaderId: string;
  value: SelectedMember[];
  onChange: (next: SelectedMember[]) => void;
}) {
  const { t } = useT("modals");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const isSelected = (type: "agent" | "member", id: string) =>
    value.some((m) => m.type === type && m.id === id);

  const toggle = (m: SelectedMember) => {
    if (isSelected(m.type, m.id)) {
      onChange(value.filter((x) => !(x.type === m.type && x.id === m.id)));
    } else {
      onChange([...value, m]);
    }
  };

  const remove = (m: SelectedMember) => {
    onChange(value.filter((x) => !(x.type === m.type && x.id === m.id)));
  };

  const myAgents = useMemo(
    () =>
      currentUserId
        ? agents.filter((a) => a.owner_id === currentUserId && a.id !== leaderId)
        : [],
    [agents, currentUserId, leaderId],
  );
  const otherAgents = useMemo(
    () =>
      agents.filter(
        (a) =>
          a.id !== leaderId &&
          (currentUserId ? a.owner_id !== currentUserId : true),
      ),
    [agents, currentUserId, leaderId],
  );

  const q = filter.trim().toLowerCase();
  const agentMatches = (a: Agent) =>
    !q || a.name.toLowerCase().includes(q) || matchesPinyin(a.name, q);
  const memberMatches = (m: MemberWithUser) =>
    !q || m.name.toLowerCase().includes(q) || matchesPinyin(m.name, q);

  const filteredMine = myAgents.filter(agentMatches);
  const filteredOthers = otherAgents.filter(agentMatches);
  const filteredMembers = members.filter(memberMatches);
  const anyResults =
    filteredMine.length + filteredOthers.length + filteredMembers.length > 0;

  return (
    <div>
      <Label className="text-caption text-muted-foreground">
        {t(($) => $.create_squad.members_label)}{" "}
        <span className="text-muted-foreground">
          {t(($) => $.create_squad.members_optional)}
        </span>
      </Label>
      <p className="mt-0.5 mb-1.5 text-caption text-muted-foreground">
        {t(($) => $.create_squad.members_hint)}
      </p>

      <Popover
        open={open}
        onOpenChange={(v) => {
          setOpen(v);
          if (!v) setFilter("");
        }}
      >
        {/* render={<div role="combobox" />}——胶囊内部自带各自的「移除」按钮，所以触发器
            本身不能是 <button>，否则会形成按钮套按钮的非法嵌套。由 Base UI 往这个 div
            里注入点击/键盘/ARIA 逻辑。 */}
        <PopoverTrigger
          render={
            <div
              role="combobox"
              aria-haspopup="listbox"
              aria-expanded={open}
              aria-controls="squad-member-listbox"
              tabIndex={0}
              className="flex w-full min-w-0 cursor-pointer items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 text-left text-body transition-colors hover:bg-muted focus:outline-hidden focus-visible:ring-2 focus-visible:ring-ring"
            >
              {value.length === 0 ? (
                <>
                  <UserPlus className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {t(($) => $.create_squad.members_placeholder)}
                  </span>
                </>
              ) : (
                <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
                  {value.slice(0, CHIP_DISPLAY_LIMIT).map((m) => (
                    <MemberChip
                      key={`${m.type}:${m.id}`}
                      m={m}
                      onRemove={() => remove(m)}
                    />
                  ))}
                  {value.length > CHIP_DISPLAY_LIMIT && (
                    <span className="rounded-md bg-muted px-1.5 py-0.5 text-caption font-medium text-muted-foreground tabular-nums">
                      {t(($) => $.create_squad.members_more_count, {
                        count: value.length - CHIP_DISPLAY_LIMIT,
                      })}
                    </span>
                  )}
                </div>
              )}
              <ChevronDown
                className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
                  open ? "rotate-180" : ""
                }`}
              />
            </div>
          }
        />
        <PopoverContent align="start" className="w-[var(--anchor-width)] p-0">
          <div className="border-b px-2 py-1.5">
            <input
              autoFocus
              type="text"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t(($) => $.create_squad.picker_search_placeholder)}
              className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
            />
          </div>
          <div id="squad-member-listbox" role="listbox" className="max-h-72 overflow-y-auto p-1">
            {filteredMine.length > 0 && (
              <PickerSection label={t(($) => $.create_squad.group_my_agents)}>
                {filteredMine.map((a) => (
                  <PickerItem
                    key={a.id}
                    selected={isSelected("agent", a.id)}
                    onClick={() =>
                      toggle({ type: "agent", id: a.id, name: a.name })
                    }
                  >
                    <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                    <span className="truncate">{a.name}</span>
                  </PickerItem>
                ))}
              </PickerSection>
            )}
            {filteredOthers.length > 0 && (
              <PickerSection label={t(($) => $.create_squad.group_workspace_agents)}>
                {filteredOthers.map((a) => (
                  <PickerItem
                    key={a.id}
                    selected={isSelected("agent", a.id)}
                    onClick={() =>
                      toggle({ type: "agent", id: a.id, name: a.name })
                    }
                  >
                    <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                    <span className="truncate">{a.name}</span>
                  </PickerItem>
                ))}
              </PickerSection>
            )}
            {filteredMembers.length > 0 && (
              <PickerSection label={t(($) => $.create_squad.group_members)}>
                {filteredMembers.map((m) => (
                  <PickerItem
                    key={m.user_id}
                    selected={isSelected("member", m.user_id)}
                    onClick={() =>
                      toggle({ type: "member", id: m.user_id, name: m.name })
                    }
                  >
                    <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                    <span className="truncate">{m.name}</span>
                  </PickerItem>
                ))}
              </PickerSection>
            )}
            {!anyResults && <PickerEmpty />}
          </div>
        </PopoverContent>
      </Popover>
    </div>
  );
}

/**
 * 成员胶囊——展示单个已选成员（头像 + 名字），附带「移除」小按钮。移除按钮
 * preventDefault/stopPropagation 是为了避免误触到外层 Popover 触发器的打开逻辑。
 */
function MemberChip({
  m,
  onRemove,
}: {
  m: SelectedMember;
  onRemove: () => void;
}) {
  const { t } = useT("modals");
  return (
    <span className="inline-flex items-center gap-1 rounded-full border bg-background px-1.5 py-0.5 text-caption">
      <ActorAvatar actorType={m.type} actorId={m.id} size="xs" />
      <span className="max-w-[120px] truncate">{m.name}</span>
      <button
        type="button"
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          onRemove();
        }}
        aria-label={t(($) => $.create_squad.members_remove_aria, { name: m.name })}
        className="rounded-full text-muted-foreground hover:text-foreground"
      >
        <X className="h-3 w-3" />
      </button>
    </span>
  );
}

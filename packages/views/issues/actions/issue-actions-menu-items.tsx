"use client";

// 问题操作菜单项：渲染下拉菜单和右键菜单共用的那一组操作项（状态、优先级、指派人、日期、关系、删除等）。
import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ArrowDown,
  ArrowUp,
  Calendar,
  CalendarClock,
  ExternalLink,
  FolderOpen,
  Link2,
  Network,
  Pin,
  PinOff,
  Plus,
  Trash2,
  Unlink,
  UserMinus,
} from "lucide-react";
import type { Issue } from "@multica/core/types";
import { resolveWorkdirCopyTarget } from "@multica/core/issues";
import { todayDateOnly, addDaysDateOnly } from "@multica/core/issues/date";
import { api } from "@multica/core/api";
import {
  PRIORITY_DISPLAY_ORDER,
  PRIORITY_CONFIG,
} from "@multica/core/issues/config";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useStatusOptions } from "../utils/status-options";
import { issueKeys } from "@multica/core/issues/queries";
import { StatusIcon } from "../components/status-icon";
import { PriorityIcon } from "../components/priority-icon";
import {
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
  DropdownMenuSeparator,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  ContextMenuItem,
  ContextMenuSub,
  ContextMenuSubTrigger,
  ContextMenuSubContent,
  ContextMenuSeparator,
} from "@multica/ui/components/ui/context-menu";
import { copyText } from "@multica/ui/lib/clipboard";
import type { UseIssueActionsResult } from "./use-issue-actions";
import { PluginHookMenuItems, PluginModalMenuItems } from "../../plugins";
import { useT } from "../../i18n";

// Dropdown 和 Context 两种菜单包装暴露的 API 是兼容的（variant、inset、onClick 等）。
// 这里把需要的菜单原语打包进一个对象，让 `IssueActionsMenuItems` 用同一份 JSX 同时渲染两种菜单。
export interface MenuPrimitives {
  Item: typeof DropdownMenuItem;
  Sub: typeof DropdownMenuSub;
  SubTrigger: typeof DropdownMenuSubTrigger;
  SubContent: typeof DropdownMenuSubContent;
  Separator: typeof DropdownMenuSeparator;
}

/** 下拉菜单原语集合，供 IssueActionsMenuItems 复用。 */
export const dropdownPrimitives: MenuPrimitives = {
  Item: DropdownMenuItem,
  Sub: DropdownMenuSub,
  SubTrigger: DropdownMenuSubTrigger,
  SubContent: DropdownMenuSubContent,
  Separator: DropdownMenuSeparator,
};

// 右键菜单原语在 API 上和下拉菜单原语兼容，但 TypeScript 的类型身份不同。
// 在这里统一强转一次，作为两套原语之间的唯一桥接点。
export const contextPrimitives: MenuPrimitives = {
  Item: ContextMenuItem as unknown as typeof DropdownMenuItem,
  Sub: ContextMenuSub as unknown as typeof DropdownMenuSub,
  SubTrigger: ContextMenuSubTrigger as unknown as typeof DropdownMenuSubTrigger,
  SubContent: ContextMenuSubContent as unknown as typeof DropdownMenuSubContent,
  Separator: ContextMenuSeparator as unknown as typeof DropdownMenuSeparator,
};

interface IssueActionsMenuItemsProps {
  issue: Issue;
  actions: UseIssueActionsResult;
  primitives: MenuPrimitives;
  /** 用户点击「指派人」菜单项时调用。父组件应关闭外层菜单并打开共享的 `AssigneePicker` 弹层。
   *  解耦成这样，是让同一个菜单项既能驱动下拉菜单（三点按钮），也能驱动右键菜单。 */
  onOpenAssignee: () => void;
  /** 若设置，删除问题后离开当前页（详情页用它，因为渲染的正是被删除的问题）。
   *  删除弹窗会回到用户来的列表，只有没有站内历史时才回退到该路径。
   *  列表视图不设置它，删除后原地不动。 */
  onDeletedFallbackPath?: string;
}

/**
 * 渲染问题操作菜单的全部菜单项，供下拉菜单和右键菜单共用。
 * @param props.issue - 操作的目标问题。
 * @param props.actions - 由 useIssueActions 返回的操作集合。
 * @param props.primitives - 菜单原语集合（下拉或右键）。
 * @param props.onOpenAssignee - 点击「指派人」时的回调。
 * @param props.onDeletedFallbackPath - 删除后的兜底跳转路径。
 */
export function IssueActionsMenuItems({
  issue,
  actions,
  primitives: P,
  onOpenAssignee,
  onDeletedFallbackPath,
}: IssueActionsMenuItemsProps) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const statusOptions = useStatusOptions(wsId);
  const { categoryOf, colorOf } = useIssueStatuses(wsId);
  const {
    isPinned,
    updateField,
    openInNewTab,
    togglePin,
    copyLink,
    openCreateSubIssue,
    openSetParent,
    removeParent,
    openAddChild,
    openDeleteConfirm,
  } = actions;

  // 提前订阅问题的任务列表，这样等用户点「复制本地工作目录路径」时缓存已经热好了。
  // 该查询只在菜单打开时才发起（Base UI 懒加载菜单内容）——列表视图给每一行都包了
  // IssueActionsContextMenu，在菜单真正打开前不产生任何开销。
  //
  // 该查询的 key 与 ExecutionLogSection 共享，从详情页导航过来是免费的缓存命中。
  const { data: tasks } = useQuery({
    queryKey: issueKeys.tasks(issue.id),
    queryFn: () => api.listTasksByIssue(issue.id),
    staleTime: 30_000,
  });
  const workdirCopyTarget = useMemo(
    () => resolveWorkdirCopyTarget(tasks),
    [tasks],
  );

  // 同步的点击处理器——旧版里 await 的 fetch 会丢掉浏览器短暂的「用户激活」状态，
  // 导致缓存冷的时候 navigator.clipboard.writeText() 在菜单里被拒绝。
  // 现在直接读缓存里的查询结果，在点击的同一个任务里写入剪贴板。
  const handleCopyWorkdirPath = useCallback(() => {
    if (!workdirCopyTarget) {
      toast.error(t(($) => $.detail.workdir_path_unavailable));
      return;
    }
    void copyText(workdirCopyTarget.path).then((ok) => {
      if (!ok) {
        toast.error(t(($) => $.detail.workdir_path_copy_failed));
        return;
      }
      if (workdirCopyTarget.source === "durable_project_directory") {
        toast.success(
          workdirCopyTarget.branchName
            ? t(($) => $.detail.project_directory_path_copied_with_branch, {
                branch: workdirCopyTarget.branchName,
              })
            : t(($) => $.detail.project_directory_path_copied),
        );
        return;
      }
      toast.success(t(($) => $.detail.workdir_path_copied));
    });
  }, [workdirCopyTarget, t]);

  return (
    <>
      {/* 状态 */}
      <P.Sub>
        <P.SubTrigger>
          <StatusIcon
            status={issue.status}
            category={categoryOf(issue.status)}
            color={colorOf(issue.status)}
            className="h-3.5 w-3.5"
          />
          {t(($) => $.actions.status)}
        </P.SubTrigger>
        <P.SubContent>
          {/* 由状态目录驱动，和选择器、过滤器保持一致：所有能改状态的入口必须提供同一套选项，
              否则自定义状态就无法从看板的右键菜单里触达。按规范分类顺序排成一张平铺列表。(MUL-6243) */}
          {statusOptions.map((option) => (
            <P.Item
              key={option.key}
              onClick={() => updateField({ status: option.key })}
            >
              <StatusIcon
                status={option.key}
                category={option.category}
                color={option.color}
                className="h-3.5 w-3.5"
              />
              {option.label}
              {issue.status === option.key && (
                <span className="ml-auto text-caption text-muted-foreground">{"✓"}</span>
              )}
            </P.Item>
          ))}
        </P.SubContent>
      </P.Sub>

      {/* 优先级 */}
      <P.Sub>
        <P.SubTrigger>
          <PriorityIcon priority={issue.priority} />
          {t(($) => $.actions.priority)}
        </P.SubTrigger>
        <P.SubContent>
          {PRIORITY_DISPLAY_ORDER.map((p) => (
            <P.Item key={p} onClick={() => updateField({ priority: p })}>
              <span
                className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-caption font-medium ${PRIORITY_CONFIG[p].badgeBg} ${PRIORITY_CONFIG[p].badgeText}`}
              >
                <PriorityIcon priority={p} className="h-3 w-3" inheritColor />
                {t(($) => $.priority[p])}
              </span>
              {issue.priority === p && (
                <span className="ml-auto text-caption text-muted-foreground">{"✓"}</span>
              )}
            </P.Item>
          ))}
        </P.SubContent>
      </P.Sub>

      {/* 指派人——关闭本菜单并交给共享的 AssigneePicker（成员 + 智能体 + 小队，带搜索和权限校验）。
          让详情侧栏、看板卡片、右键/三点菜单共用同一份指派人交互，保持单一事实来源。 */}
      <P.Item onClick={onOpenAssignee}>
        <UserMinus className="h-3.5 w-3.5" />
        {t(($) => $.actions.assignee)}
      </P.Item>

      {/* 开始日期 */}
      <P.Sub>
        <P.SubTrigger>
          <CalendarClock className="h-3.5 w-3.5" />
          {t(($) => $.actions.start_date)}
        </P.SubTrigger>
        <P.SubContent>
          <P.Item onClick={() => updateField({ start_date: todayDateOnly() })}>
            {t(($) => $.actions.start_today)}
          </P.Item>
          <P.Item onClick={() => updateField({ start_date: addDaysDateOnly(1) })}>
            {t(($) => $.actions.start_tomorrow)}
          </P.Item>
          <P.Item onClick={() => updateField({ start_date: addDaysDateOnly(7) })}>
            {t(($) => $.actions.start_next_week)}
          </P.Item>
          {issue.start_date && (
            <>
              <P.Separator />
              <P.Item onClick={() => updateField({ start_date: null })}>
                {t(($) => $.actions.start_clear)}
              </P.Item>
            </>
          )}
        </P.SubContent>
      </P.Sub>

      {/* 截止日期 */}
      <P.Sub>
        <P.SubTrigger>
          <Calendar className="h-3.5 w-3.5" />
          {t(($) => $.actions.due_date)}
        </P.SubTrigger>
        <P.SubContent>
          <P.Item onClick={() => updateField({ due_date: todayDateOnly() })}>
            {t(($) => $.actions.due_today)}
          </P.Item>
          <P.Item onClick={() => updateField({ due_date: addDaysDateOnly(1) })}>
            {t(($) => $.actions.due_tomorrow)}
          </P.Item>
          <P.Item onClick={() => updateField({ due_date: addDaysDateOnly(7) })}>
            {t(($) => $.actions.due_next_week)}
          </P.Item>
          {issue.due_date && (
            <>
              <P.Separator />
              <P.Item onClick={() => updateField({ due_date: null })}>
                {t(($) => $.actions.due_clear)}
              </P.Item>
            </>
          )}
        </P.SubContent>
      </P.Sub>

      <P.Separator />

      {/* 排在「对问题本身做点什么」分组的最前面：对于不知道修饰键+点击的用户，
          这是唯一能被发现的「在别处打开问题」的入口，所以放在复制类操作之上。 */}
      <P.Item onClick={openInNewTab}>
        <ExternalLink className="h-3.5 w-3.5" />
        {t(($) => $.actions.open_in_new_tab)}
      </P.Item>
      <P.Item onClick={togglePin}>
        {isPinned ? (
          <PinOff className="h-3.5 w-3.5" />
        ) : (
          <Pin className="h-3.5 w-3.5" />
        )}
        {isPinned ? t(($) => $.actions.unpin_from_sidebar) : t(($) => $.actions.pin_to_sidebar)}
      </P.Item>
      <P.Item onClick={copyLink}>
        <Link2 className="h-3.5 w-3.5" />
        {t(($) => $.actions.copy_link)}
      </P.Item>
      <P.Item onClick={handleCopyWorkdirPath}>
        <FolderOpen className="h-3.5 w-3.5" />
        {workdirCopyTarget?.source === "durable_project_directory"
          ? t(($) => $.actions.copy_project_directory_path)
          : t(($) => $.actions.copy_workdir_path)}
      </P.Item>

      <P.Separator />

      {/* 关系类操作放在「关系」子菜单下——用语义明确的标签（不像旧的「更多」），
          第一层就能看出子菜单是干什么的。目前放父子问题链接，以后会加入更多关系类型
          （阻塞、重复、相关）。 */}
      <P.Sub>
        <P.SubTrigger>
          <Network className="h-3.5 w-3.5" />
          {t(($) => $.actions.relations)}
        </P.SubTrigger>
        <P.SubContent>
          <P.Item onClick={openCreateSubIssue}>
            <Plus className="h-3.5 w-3.5" />
            {t(($) => $.actions.create_sub_issue)}
          </P.Item>
          <P.Item onClick={openSetParent}>
            <ArrowUp className="h-3.5 w-3.5" />
            {t(($) => $.actions.set_parent_issue)}
          </P.Item>
          {issue.parent_issue_id && (
            <P.Item onClick={removeParent}>
              <Unlink className="h-3.5 w-3.5" />
              {t(($) => $.actions.remove_parent_issue)}
            </P.Item>
          )}
          <P.Item onClick={openAddChild}>
            <ArrowDown className="h-3.5 w-3.5" />
            {t(($) => $.actions.add_sub_issue)}
          </P.Item>
        </P.SubContent>
      </P.Sub>

      {/* 手动插件钩子。由宿主渲染而不是插件自己渲染，因为触发者决定了身份：一次 `manual`
          调用是以「选中它的人」的身份执行的，所以入口必须放在宿主能证明「有人点过」的地方。
          没有插件声明时这里什么都不渲染。 */}
      <PluginHookMenuItems issueId={issue.id} Item={P.Item} Separator={P.Separator} />
      {/* 插件模态也从这里打开，同理：模态是第三方 UI 接管整个屏幕，所以它是「因为有人选了」才打开，
          绝不因为插件的自发行为而打开。 */}
      <PluginModalMenuItems issueId={issue.id} Item={P.Item} />

      <P.Separator />

      <P.Item
        variant="destructive"
        onClick={() => openDeleteConfirm({ onDeletedFallbackPath })}
      >
        <Trash2 className="h-3.5 w-3.5" />
        {t(($) => $.actions.delete_issue)}
      </P.Item>
    </>
  );
}

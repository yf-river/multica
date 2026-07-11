import { CalendarClock, CalendarDays, ChevronRight, Plus, Tag } from "lucide-react";
import type { Issue, UpdateIssueRequest } from "@multica/core/types";
import { formatDateOnly } from "@multica/core/issues/date";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { PropRow } from "../../common/prop-row";
import { ActorAvatar } from "../../common/actor-avatar";
import { AppLink } from "../../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  AssigneePicker,
  DueDatePicker,
  LabelPicker,
  PriorityIcon,
  PriorityPicker,
  StartDatePicker,
  StatusIcon,
  StatusPicker,
} from ".";
import { ProjectPicker } from "../../projects/components/project-picker";
import { ExecutionLogSection, IssueRunReviewSummaryCard } from "./execution-log-section";
import { PullRequestList } from "./pull-request-list";
import { OPTIONAL_PROP_KEYS, type OptionalPropKey } from "./issue-detail-model";
import type { IssueDetailT } from "./issue-detail-source";

interface IssueDetailSidebarProps {
  issue: Issue;
  issueId: string;
  t: IssueDetailT;
  propertiesOpen: boolean;
  setPropertiesOpen: (open: boolean) => void;
  handleUpdateField: (updates: Partial<UpdateIssueRequest>) => void;
  visibleOptionalProps: ReadonlySet<OptionalPropKey>;
  autoOpenProp: OptionalPropKey | null;
  addPropPopoverOpen: boolean;
  setAddPropPopoverOpen: (open: boolean) => void;
  addOptionalProp: (key: OptionalPropKey) => void;
  parentIssue: Issue | null;
  parentIssueOpen: boolean;
  setParentIssueOpen: (open: boolean) => void;
  prSidebar: boolean;
  pullRequestsOpen: boolean;
  setPullRequestsOpen: (open: boolean) => void;
  detailsOpen: boolean;
  setDetailsOpen: (open: boolean) => void;
  getActorName: (type: string, id: string) => string;
}

function shortDate(date: string | null): string {
  if (!date) return "—";
  return formatDateOnly(date, { month: "short", day: "numeric" }, "zh-CN");
}

export function IssueDetailSidebar({
  issue,
  issueId: id,
  t,
  propertiesOpen,
  setPropertiesOpen,
  handleUpdateField,
  visibleOptionalProps,
  autoOpenProp,
  addPropPopoverOpen,
  setAddPropPopoverOpen,
  addOptionalProp,
  parentIssue,
  parentIssueOpen,
  setParentIssueOpen,
  prSidebar,
  pullRequestsOpen,
  setPullRequestsOpen,
  detailsOpen,
  setDetailsOpen,
  getActorName,
}: IssueDetailSidebarProps) {
  const paths = useWorkspacePaths();
  return (
    <div className="space-y-5">
      {/* Properties */}
      <div>
        <button
          type="button"
          className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors mb-2 hover:bg-accent/70 ${propertiesOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
          onClick={() => setPropertiesOpen(!propertiesOpen)}
        >
          {t(($) => $.detail.section_properties)}
          <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${propertiesOpen ? "rotate-90" : ""}`} />
        </button>
        {propertiesOpen && <div className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 pl-2">
          {/* Core props — always rendered. */}
          <PropRow label={t(($) => $.detail.prop_status)}>
            <StatusPicker status={issue.status} onUpdate={handleUpdateField} align="start" />
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_assignee)}>
            <AssigneePicker assigneeType={issue.assignee_type} assigneeId={issue.assignee_id} onUpdate={handleUpdateField} align="start" />
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_project)}>
            <ProjectPicker
              projectId={issue.project_id}
              onUpdate={handleUpdateField}
            />
          </PropRow>

          {/* Optional props — rendered only when set on the issue OR added
              via "+ Add property" in this session. Row order follows the
              order of `OPTIONAL_PROP_KEYS`. */}
          {visibleOptionalProps.has("priority") && (
            <PropRow label={t(($) => $.detail.prop_priority)}>
              <PriorityPicker
                priority={issue.priority}
                onUpdate={handleUpdateField}
                align="start"
                defaultOpen={autoOpenProp === "priority"}
              />
            </PropRow>
          )}
          {visibleOptionalProps.has("start_date") && (
            <PropRow label={t(($) => $.detail.prop_start_date)}>
              <StartDatePicker
                startDate={issue.start_date}
                onUpdate={handleUpdateField}
                defaultOpen={autoOpenProp === "start_date"}
              />
            </PropRow>
          )}
          {visibleOptionalProps.has("due_date") && (
            <PropRow label={t(($) => $.detail.prop_due_date)}>
              <DueDatePicker
                dueDate={issue.due_date}
                onUpdate={handleUpdateField}
                defaultOpen={autoOpenProp === "due_date"}
              />
            </PropRow>
          )}
          {visibleOptionalProps.has("labels") && (
            <PropRow label={t(($) => $.detail.prop_labels)}>
              <LabelPicker
                issueId={issue.id}
                align="start"
                defaultOpen={autoOpenProp === "labels"}
              />
            </PropRow>
          )}

          {/* "+ Add property" — opens a Popover listing optional fields
              not yet displayed. Hidden once every optional field is on
              screen. Sits inside the same grid as a full-row, with its
              own padding so the visual rhythm follows the rows above. */}
          {OPTIONAL_PROP_KEYS.some((k) => !visibleOptionalProps.has(k)) && (
            <div className="col-span-2 mt-1">
              <Popover open={addPropPopoverOpen} onOpenChange={setAddPropPopoverOpen}>
                <PopoverTrigger
                  className="flex items-center gap-1.5 rounded-md px-2 py-1 -mx-2 text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground transition-colors"
                >
                  <Plus className="h-3 w-3 shrink-0" />
                  <span>{t(($) => $.detail.add_property_action)}</span>
                </PopoverTrigger>
                {/* Item visuals mirror the inspector rows' typography
                    (text-xs, muted icons) and each option leads with the
                    icon the resulting picker uses, so the dropdown reads
                    as a preview of what will show up below. */}
                <PopoverContent align="start" className="w-44 p-1">
                  {OPTIONAL_PROP_KEYS.filter((k) => !visibleOptionalProps.has(k)).map((k) => (
                    <button
                      key={k}
                      type="button"
                      onClick={() => addOptionalProp(k)}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-xs text-foreground/90 transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
                    >
                      {k === "priority" && (
                        <PriorityIcon priority="medium" inheritColor className="text-muted-foreground" />
                      )}
                      {k === "start_date" && (
                        <CalendarClock className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      {k === "due_date" && (
                        <CalendarDays className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      {k === "labels" && (
                        <Tag className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      <span className="truncate">
                        {k === "priority" && t(($) => $.detail.prop_priority)}
                        {k === "start_date" && t(($) => $.detail.prop_start_date)}
                        {k === "due_date" && t(($) => $.detail.prop_due_date)}
                        {k === "labels" && t(($) => $.detail.prop_labels)}
                      </span>
                    </button>
                  ))}
                </PopoverContent>
              </Popover>
            </div>
          )}
        </div>}
      </div>

      {/* Parent issue — standalone section, only when the issue has a
          parent. Setting a parent is reachable via the issue actions menu;
          this card surfaces an existing parent without occupying sidebar
          space for issues that don't have one. */}
      {parentIssue && (
        <div>
          <button
            type="button"
            className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors mb-2 hover:bg-accent/70 ${parentIssueOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
            onClick={() => setParentIssueOpen(!parentIssueOpen)}
          >
            {t(($) => $.detail.section_parent_issue)}
            <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${parentIssueOpen ? "rotate-90" : ""}`} />
          </button>
          {parentIssueOpen && <div className="pl-2">
            <AppLink
              href={paths.issueDetail(parentIssue.id)}
              className="flex items-center gap-1.5 rounded-md px-2 py-1.5 -mx-2 text-xs hover:bg-accent/50 transition-colors group"
            >
              <StatusIcon status={parentIssue.status} className="h-3.5 w-3.5 shrink-0" />
              <span className="text-muted-foreground shrink-0">{parentIssue.identifier}</span>
              <span className="truncate group-hover:text-foreground">{parentIssue.title}</span>
            </AppLink>
          </div>}
        </div>
      )}

      {/* Pull requests — hidden when the workspace disables the PR sidebar
          (or the GitHub master switch is off). Backend data is kept either
          way so re-enabling restores the section instantly. */}
      {prSidebar && (
        <div>
          <button
            type="button"
            className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors mb-2 hover:bg-accent/70 ${pullRequestsOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
            onClick={() => setPullRequestsOpen(!pullRequestsOpen)}
          >
            {t(($) => $.detail.section_pull_requests)}
            <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${pullRequestsOpen ? "rotate-90" : ""}`} />
          </button>
          {pullRequestsOpen && <div className="pl-2"><PullRequestList issueId={id} /></div>}
        </div>
      )}

      {/* Details */}
      <div>
        <button
          type="button"
          className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors mb-2 hover:bg-accent/70 ${detailsOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
          onClick={() => setDetailsOpen(!detailsOpen)}
        >
          {t(($) => $.detail.section_details)}
          <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${detailsOpen ? "rotate-90" : ""}`} />
        </button>
        {detailsOpen && <div className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 pl-2">
          <PropRow label={t(($) => $.detail.prop_created_by)}>
            <ActorAvatar actorType={issue.creator_type} actorId={issue.creator_id} size={18} enableHoverCard />
            <span className="cursor-pointer truncate">{getActorName(issue.creator_type, issue.creator_id)}</span>
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_created)}>
            <span className="text-muted-foreground">{shortDate(issue.created_at)}</span>
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_updated)}>
            <span className="text-muted-foreground">{shortDate(issue.updated_at)}</span>
          </PropRow>
        </div>}
      </div>

      {/* Run review — compact entry point for completed/historical execution
          evidence. Detailed timelines, execution trees, SOP details and token
          breakdowns live on the run review page. */}
      <IssueRunReviewSummaryCard issueId={id} />

      {/* Execution log — active run status only; historical evidence lives in run review. */}
      <ExecutionLogSection issueId={id} />
    </div>
  );
}

import { useCallback } from "react";
import type { Issue, UpdateIssueRequest } from "@multica/core/types";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { useIssueSelectionStore } from "@multica/core/issues/stores/selection-store";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { toast } from "sonner";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { AssigneePicker, StatusIcon, StatusPicker } from ".";

export function SubIssueRow({ child }: { child: Issue }) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const updateIssue = useUpdateIssue();
  const selected = useIssueSelectionStore((s) => s.selectedIds.has(child.id));
  const toggleSelected = useIssueSelectionStore((s) => s.toggle);
  const isDone = child.status === "done" || child.status === "cancelled";

  const handleUpdate = useCallback(
    (updates: Partial<UpdateIssueRequest>) => {
      updateIssue.mutate(
        { id: child.id, ...updates },
        {
          onError: (err) =>
            toast.error(
              err instanceof Error && err.message
                ? err.message
                : t(($) => $.detail.update_failed),
            ),
        },
      );
    },
    [child.id, updateIssue, t],
  );

  // AppLink wraps only the title/identifier area. Pickers and checkbox are
  // siblings, so their clicks never navigate — no stopPropagation acrobatics
  // and no risk of the native checkbox / picker triggers being blocked.
  return (
    <div
      className={cn(
        "flex items-center gap-2.5 px-3 py-2 hover:bg-accent/50 transition-colors group/row",
        selected && "bg-accent/30",
      )}
    >
      <div
        className={cn(
          "flex h-4 w-4 shrink-0 items-center justify-center transition-opacity",
          selected
            ? "opacity-100"
            : "opacity-0 group-hover/row:opacity-100 focus-within:opacity-100",
        )}
      >
        <input
          type="checkbox"
          checked={selected}
          onChange={() => toggleSelected(child.id)}
          aria-label={`Select ${child.identifier}`}
          className="cursor-pointer accent-primary"
        />
      </div>
      <StatusPicker
        status={child.status}
        onUpdate={handleUpdate}
        align="start"
        trigger={
          <StatusIcon
            status={child.status}
            className="h-[15px] w-[15px] shrink-0"
          />
        }
      />
      <AppLink
        href={paths.issueDetail(child.id)}
        className="flex min-w-0 flex-1 items-center gap-2.5"
      >
        <span className="text-[11px] text-muted-foreground tabular-nums font-medium shrink-0">
          {child.identifier}
        </span>
        <span
          className={cn(
            "text-sm truncate flex-1",
            isDone
              ? "text-muted-foreground"
              : "group-hover/row:text-foreground",
          )}
        >
          {child.title}
        </span>
      </AppLink>
      <AssigneePicker
        assigneeType={child.assignee_type}
        assigneeId={child.assignee_id}
        onUpdate={handleUpdate}
        align="end"
        trigger={
          child.assignee_type && child.assignee_id ? (
            <ActorAvatar
              actorType={child.assignee_type}
              actorId={child.assignee_id}
              size={20}
              className="shrink-0"
            />
          ) : (
            <span
              aria-hidden
              className="h-5 w-5 rounded-full border border-dashed border-muted-foreground/30 shrink-0"
            />
          )
        }
      />
    </div>
  );
}

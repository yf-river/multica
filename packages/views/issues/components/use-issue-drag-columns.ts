"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragOverEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import { arrayMove } from "@dnd-kit/sortable";
import type { Issue } from "@multica/core/types";
import type {
  IssueGrouping,
  SortField,
} from "@multica/core/issues/stores/view-store";
import type { BoardColumnGroup } from "./board-column";
import {
  buildColumns,
  computePosition,
  findColumn,
  getMoveUpdates,
  issueMatchesGroup,
  makeKanbanCollision,
  type DragMoveUpdates,
} from "../utils/drag-utils";

interface UseIssueDragColumnsOptions {
  issues: Issue[];
  groups: BoardColumnGroup[];
  grouping: IssueGrouping;
  sortBy: SortField;
  onMoveIssue?: (
    issueId: string,
    updates: DragMoveUpdates,
    onSettled?: () => void,
  ) => void;
}

export function useIssueDragColumns({
  issues,
  groups,
  grouping,
  sortBy,
  onMoveIssue,
}: UseIssueDragColumnsOptions) {
  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);
  const isDraggingRef = useRef(false);
  const isSettlingRef = useRef(false);
  const [settleVersion, setSettleVersion] = useState(0);

  const [columns, setColumns] = useState<Record<string, string[]>>(() =>
    buildColumns(issues, groups, grouping),
  );
  const columnsRef = useRef(columns);
  columnsRef.current = columns;

  useEffect(() => {
    if (!isDraggingRef.current && !isSettlingRef.current) {
      setColumns(buildColumns(issues, groups, grouping));
    }
  }, [issues, groups, grouping, settleVersion]);

  const recentlyMovedRef = useRef(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => {
      recentlyMovedRef.current = false;
    });
    return () => cancelAnimationFrame(id);
  }, [columns]);

  const issueMap = useMemo(() => {
    const map = new Map<string, Issue>();
    for (const issue of issues) map.set(issue.id, issue);
    return map;
  }, [issues]);

  const issueMapRef = useRef(issueMap);
  if (!isDraggingRef.current && !isSettlingRef.current) {
    issueMapRef.current = issueMap;
  }

  const groupIds = useMemo(
    () => new Set(groups.map((group) => group.id)),
    [groups],
  );
  const groupMap = useMemo(
    () => new Map(groups.map((group) => [group.id, group])),
    [groups],
  );
  const collisionDetection = useMemo(
    () => makeKanbanCollision(groupIds),
    [groupIds],
  );

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    }),
  );

  const resetColumns = useCallback(() => {
    setColumns(buildColumns(issues, groups, grouping));
  }, [issues, groups, grouping]);

  const handleDragStart = useCallback((event: DragStartEvent) => {
    isDraggingRef.current = true;
    const issue = issueMapRef.current.get(event.active.id as string) ?? null;
    setActiveIssue(issue);
  }, []);

  const handleDragOver = useCallback(
    (event: DragOverEvent) => {
      const { active, over } = event;
      if (!over || recentlyMovedRef.current) return;

      const activeId = active.id as string;
      const overId = over.id as string;

      setColumns((prev) => {
        const activeCol = findColumn(prev, activeId, groupIds);
        const overCol = findColumn(prev, overId, groupIds);
        if (!activeCol || !overCol || activeCol === overCol) return prev;
        if (sortBy !== "position") return prev;

        recentlyMovedRef.current = true;
        const oldIds = prev[activeCol]!.filter((id) => id !== activeId);
        const newIds = [...prev[overCol]!];
        const overIndex = newIds.indexOf(overId);
        const insertIndex = overIndex >= 0 ? overIndex : newIds.length;
        newIds.splice(insertIndex, 0, activeId);
        return { ...prev, [activeCol]: oldIds, [overCol]: newIds };
      });
    },
    [groupIds, sortBy],
  );

  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      const { active, over } = event;
      isDraggingRef.current = false;
      setActiveIssue(null);

      if (!over || !onMoveIssue) {
        resetColumns();
        return;
      }

      const activeId = active.id as string;
      const overId = over.id as string;

      const cols = columnsRef.current;
      const activeCol = findColumn(cols, activeId, groupIds);
      const overCol = findColumn(cols, overId, groupIds);
      if (!activeCol || !overCol) {
        resetColumns();
        return;
      }

      let finalColumns = cols;
      if (activeCol === overCol && sortBy === "position") {
        const ids = cols[activeCol]!;
        const oldIndex = ids.indexOf(activeId);
        const newIndex = ids.indexOf(overId);
        if (oldIndex !== -1 && newIndex !== -1 && oldIndex !== newIndex) {
          const reordered = arrayMove(ids, oldIndex, newIndex);
          finalColumns = { ...cols, [activeCol]: reordered };
          setColumns(finalColumns);
        }
      }

      const finalCol =
        sortBy === "position"
          ? findColumn(finalColumns, activeId, groupIds)
          : overCol;
      if (!finalCol) {
        resetColumns();
        return;
      }
      const finalGroup = groupMap.get(finalCol);
      if (!finalGroup) {
        resetColumns();
        return;
      }

      const map = issueMapRef.current;

      if (sortBy !== "position") {
        const currentIssue = map.get(activeId);
        if (!currentIssue || issueMatchesGroup(currentIssue, finalGroup)) {
          resetColumns();
          return;
        }
        isSettlingRef.current = true;
        onMoveIssue(activeId, getMoveUpdates(finalGroup, currentIssue.position), () => {
          isSettlingRef.current = false;
          setSettleVersion((v) => v + 1);
        });
        return;
      }

      const finalIds = finalColumns[finalCol]!;
      const newPosition = computePosition(finalIds, activeId, map);
      const currentIssue = map.get(activeId);

      if (
        currentIssue &&
        issueMatchesGroup(currentIssue, finalGroup) &&
        currentIssue.position === newPosition
      ) {
        return;
      }

      isSettlingRef.current = true;
      onMoveIssue(activeId, getMoveUpdates(finalGroup, newPosition), () => {
        isSettlingRef.current = false;
      });
    },
    [groupIds, groupMap, onMoveIssue, resetColumns, sortBy],
  );

  return {
    activeIssue,
    collisionDetection,
    columns,
    handleDragEnd,
    handleDragOver,
    handleDragStart,
    isDraggingRef,
    issueMapRef,
    sensors,
  };
}

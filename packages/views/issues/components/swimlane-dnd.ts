import {
  closestCenter,
  pointerWithin,
  type CollisionDetection,
} from "@dnd-kit/core";
import type { IssueStatus } from "@multica/core/types";
import type { SwimlaneGrouping } from "@multica/core/issues/stores/view-store";

export const NONE_LANE_ID = "none";
export const ORPHAN_LANE_ID = "__orphans__";

const LANE_ID_PREFIX = "lane:";

type SwimLaneCells = Record<string, Record<string, string[]>>;
type SwimLaneCellRef = { laneKey: string; status: string };

export function makeSwimLaneCollision(cellIds: Set<string>): CollisionDetection {
  return (args) => {
    const activeId = args.active.id as string;
    const isLaneDrag = activeId.startsWith(LANE_ID_PREFIX);
    const pointer = pointerWithin(args);

    if (pointer.length > 0) {
      const matchingTargets = pointer.filter((collision) =>
        isLaneDrag
          ? String(collision.id).startsWith(LANE_ID_PREFIX)
          : !String(collision.id).startsWith(LANE_ID_PREFIX),
      );
      if (matchingTargets.length > 0) {
        const cards = matchingTargets.filter((collision) => !cellIds.has(String(collision.id)));
        return cards.length > 0 ? cards : matchingTargets;
      }
    }

    return closestCenter(args).filter((collision) =>
      isLaneDrag
        ? String(collision.id).startsWith(LANE_ID_PREFIX)
        : !String(collision.id).startsWith(LANE_ID_PREFIX),
    );
  };
}

function parseCellId(id: string): { laneKey: string; status: string } | null {
  if (!id.startsWith("swim:")) return null;
  const rest = id.slice(5);
  const lastColon = rest.lastIndexOf(":");
  if (lastColon === -1) return null;
  return {
    laneKey: rest.slice(0, lastColon),
    status: rest.slice(lastColon + 1),
  };
}

export function findCellIn(
  data: SwimLaneCells,
  cellIds: Set<string>,
  id: string,
): SwimLaneCellRef | null {
  if (cellIds.has(id)) return parseCellId(id);
  for (const [laneKey, statusMap] of Object.entries(data)) {
    for (const [status, ids] of Object.entries(statusMap)) {
      if (ids.includes(id)) return { laneKey, status };
    }
  }
  return null;
}

export function moveIssueBetweenCells(
  data: SwimLaneCells,
  source: SwimLaneCellRef,
  target: SwimLaneCellRef,
  issueId: string,
  overId: string,
): SwimLaneCells {
  const sourceRow = data[source.laneKey] ?? {};
  const targetRow = source.laneKey === target.laneKey
    ? sourceRow
    : data[target.laneKey] ?? {};
  const sourceIds = (sourceRow[source.status] ?? []).filter((id) => id !== issueId);
  const targetIds = (targetRow[target.status] ?? []).filter((id) => id !== issueId);
  const overIndex = targetIds.indexOf(overId);
  targetIds.splice(overIndex >= 0 ? overIndex : targetIds.length, 0, issueId);

  if (source.laneKey === target.laneKey) {
    return {
      ...data,
      [source.laneKey]: {
        ...sourceRow,
        [source.status]: sourceIds,
        [target.status]: targetIds,
      },
    };
  }
  return {
    ...data,
    [source.laneKey]: { ...sourceRow, [source.status]: sourceIds },
    [target.laneKey]: { ...targetRow, [target.status]: targetIds },
  };
}

export function cellId(laneKey: string, status: IssueStatus): string {
  return `swim:${laneKey}:${status}`;
}

export function laneIdFor(grouping: SwimlaneGrouping, rawId: string): string {
  return `${LANE_ID_PREFIX}${grouping}:${rawId}`;
}

export function parseLaneId(id: string): { grouping: string; rawId: string } | null {
  if (!id.startsWith(LANE_ID_PREFIX)) return null;
  const rest = id.slice(LANE_ID_PREFIX.length);
  const firstColon = rest.indexOf(":");
  if (firstColon === -1) return null;
  return {
    grouping: rest.slice(0, firstColon),
    rawId: rest.slice(firstColon + 1),
  };
}

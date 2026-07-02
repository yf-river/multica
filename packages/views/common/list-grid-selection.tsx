"use client";

import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { ListGridCell } from "@multica/ui/components/ui/list-grid";

export function ListGridCheckboxCell({
  checked,
  onToggle,
}: {
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <ListGridCell className="justify-center px-0">
      <button
        type="button"
        aria-pressed={checked}
        onClick={(e) => {
          e.stopPropagation();
          onToggle();
        }}
        className={`-m-1.5 flex items-center p-1.5 ${
          checked ? "" : "opacity-0 transition-opacity group-hover/row:opacity-100"
        }`}
      >
        <Checkbox
          checked={checked}
          tabIndex={-1}
          className="pointer-events-none"
        />
      </button>
    </ListGridCell>
  );
}

export function ListGridSelectAllHeaderCell({
  allSelected,
  someSelected,
  onToggleAll,
}: {
  allSelected: boolean;
  someSelected: boolean;
  onToggleAll: () => void;
}) {
  const anySelected = allSelected || someSelected;
  return (
    <div className="flex items-center justify-center">
      <button
        type="button"
        aria-pressed={allSelected}
        onClick={onToggleAll}
        className={`-m-1.5 flex items-center p-1.5 ${
          anySelected ? "" : "opacity-0 transition-opacity group-hover/header:opacity-100"
        }`}
      >
        <Checkbox
          checked={allSelected}
          indeterminate={someSelected && !allSelected}
          tabIndex={-1}
          className="pointer-events-none"
        />
      </button>
    </div>
  );
}

"use client";

import type { ReactNode } from "react";
import { ArrowDown, ArrowUp, ChevronDown, Filter, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";

type SortDirection = "asc" | "desc";

export function ToolbarCountBadge({ count }: { count: number }) {
  return (
    <span className="ml-auto pl-3 text-xs text-muted-foreground">{count}</span>
  );
}

export function ToolbarResultCount({
  active,
  title,
  visibleCount,
  totalCount,
}: {
  active: boolean;
  title: string;
  visibleCount: number;
  totalCount: number;
}) {
  if (!active) return null;

  return (
    <span
      title={title}
      className="hidden shrink-0 text-xs tabular-nums text-muted-foreground md:inline"
    >
      {visibleCount} / {totalCount}
    </span>
  );
}

export function ToolbarFrame({
  left,
  children,
}: {
  left: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex h-12 shrink-0 items-center justify-between gap-2 px-5">
      <div className="flex min-w-0 items-center gap-2">{left}</div>
      <div className="flex shrink-0 items-center gap-1">{children}</div>
    </div>
  );
}

function ToolbarFilterButton({
  hasActiveFilters,
  activeCount,
  filterLabel,
  activeLabel,
  clearLabel,
  onClearFilters,
}: {
  hasActiveFilters: boolean;
  activeCount: number;
  filterLabel: ReactNode;
  activeLabel: ReactNode;
  clearLabel: string;
  onClearFilters: () => void;
}) {
  return (
    <Button
      variant={hasActiveFilters ? "default" : "outline"}
      size="sm"
      className={
        hasActiveFilters
          ? "h-8 w-8 gap-1 bg-brand px-0 text-white hover:bg-brand/90 md:w-auto md:px-2.5"
          : "h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5"
      }
    >
      <Filter className="size-3.5" />
      {hasActiveFilters ? (
        <>
          <span className="hidden md:inline">{activeLabel}</span>
          <span className="tabular-nums md:hidden">{activeCount}</span>
        </>
      ) : (
        <span className="hidden md:inline">{filterLabel}</span>
      )}
      {hasActiveFilters && (
        <span
          role="button"
          tabIndex={-1}
          aria-label={clearLabel}
          className="-mr-1 ml-0.5 hidden rounded-sm p-0.5 hover:bg-white/20 md:inline-flex"
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onClearFilters();
          }}
          onPointerDown={(e) => e.stopPropagation()}
        >
          <X className="size-3" />
        </span>
      )}
    </Button>
  );
}

export function ToolbarFilterDropdown({
  hasActiveFilters,
  activeCount,
  activeLabel,
  filterLabel,
  clearLabel,
  onClearFilters,
  children,
}: {
  hasActiveFilters: boolean;
  activeCount: number;
  activeLabel: ReactNode;
  filterLabel: ReactNode;
  clearLabel: string;
  onClearFilters: () => void;
  children: ReactNode;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <ToolbarFilterButton
            hasActiveFilters={hasActiveFilters}
            activeCount={activeCount}
            activeLabel={activeLabel}
            filterLabel={filterLabel}
            clearLabel={clearLabel}
            onClearFilters={onClearFilters}
          />
        }
      />
      <DropdownMenuContent align="end" className="w-auto">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function ToolbarFilterSubmenu({
  label,
  selectedCount,
  contentClassName = "w-auto min-w-44",
  children,
}: {
  label: ReactNode;
  selectedCount: number;
  contentClassName?: string;
  children: ReactNode;
}) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <span className="flex-1">{label}</span>
        {selectedCount > 0 && (
          <span className="text-xs font-medium text-primary">
            {selectedCount}
          </span>
        )}
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className={contentClassName}>
        {children}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}

export function ToolbarScopeSelector<TScope extends string>({
  scopes,
  scope,
  scopeCounts,
  scopeLabels,
  onScopeChange,
}: {
  scopes: readonly TScope[];
  scope: TScope;
  scopeCounts: Record<TScope, number>;
  scopeLabels: Record<TScope, string>;
  onScopeChange: (scope: TScope) => void;
}) {
  return (
    <>
      <div className="hidden shrink-0 items-center gap-1 md:flex">
        {scopes.map((s) => (
          <Button
            key={s}
            variant="outline"
            size="sm"
            className={
              scope === s
                ? "gap-1.5 bg-accent text-accent-foreground hover:bg-accent/80"
                : "gap-1.5 text-muted-foreground"
            }
            onClick={() => onScopeChange(s)}
          >
            {scopeLabels[s]}
            <span className="tabular-nums text-xs text-muted-foreground/70">
              {scopeCounts[s]}
            </span>
          </Button>
        ))}
      </div>

      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="outline"
              size="sm"
              className="shrink-0 gap-1 text-muted-foreground md:hidden"
            >
              <span className="truncate">{scopeLabels[scope]}</span>
              <ChevronDown className="size-3 text-muted-foreground" />
            </Button>
          }
        />
        <DropdownMenuContent align="start" className="w-auto">
          <DropdownMenuRadioGroup
            value={scope}
            onValueChange={(value) => onScopeChange(value as TScope)}
          >
            {scopes.map((s) => (
              <DropdownMenuRadioItem key={s} value={s}>
                {scopeLabels[s]}
                <span className="ml-2 tabular-nums text-xs text-muted-foreground/70">
                  {scopeCounts[s]}
                </span>
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  );
}

export function ToolbarDisplaySettings<
  TSortField extends string,
  TColumnKey extends string,
>({
  sortField,
  sortDirection,
  sortFields,
  sortLabels,
  onSortFieldChange,
  onSortDirectionChange,
  columnKeys,
  columnLabels,
  hiddenColumns,
  onToggleColumn,
  displayLabel,
  sortByLabel,
  directionAscLabel,
  directionDescLabel,
  columnsLabel,
}: {
  sortField: TSortField;
  sortDirection: SortDirection;
  sortFields: readonly TSortField[];
  sortLabels: Record<TSortField, string>;
  onSortFieldChange: (field: TSortField) => void;
  onSortDirectionChange: (direction: SortDirection) => void;
  columnKeys: readonly TColumnKey[];
  columnLabels: Record<TColumnKey, string>;
  hiddenColumns: readonly TColumnKey[];
  onToggleColumn: (key: TColumnKey) => void;
  displayLabel: string;
  sortByLabel: string;
  directionAscLabel: string;
  directionDescLabel: string;
  columnsLabel: string;
}) {
  const sortLabel = sortLabels[sortField];

  return (
    <Popover>
      <Tooltip>
        <PopoverTrigger
          render={
            <TooltipTrigger
              render={
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5"
                >
                  {sortDirection === "asc" ? (
                    <ArrowUp className="size-3.5" />
                  ) : (
                    <ArrowDown className="size-3.5" />
                  )}
                  <span className="hidden md:inline">{sortLabel}</span>
                </Button>
              }
            />
          }
        />
        <TooltipContent side="bottom">{displayLabel}</TooltipContent>
      </Tooltip>
      <PopoverContent align="end" className="w-64 p-0">
        <div className="border-b px-3 py-2.5">
          <span className="text-xs font-medium text-muted-foreground">
            {sortByLabel}
          </span>
          <div className="mt-2 flex items-center gap-1.5">
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="outline"
                    size="sm"
                    className="flex-1 justify-between text-xs"
                  >
                    {sortLabel}
                    <ChevronDown className="size-3 text-muted-foreground" />
                  </Button>
                }
              />
              <DropdownMenuContent align="start" className="w-auto">
                <DropdownMenuRadioGroup
                  value={sortField}
                  onValueChange={(value) =>
                    onSortFieldChange(value as TSortField)
                  }
                >
                  {sortFields.map((field) => (
                    <DropdownMenuRadioItem key={field} value={field}>
                      {sortLabels[field]}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuContent>
            </DropdownMenu>
            <Button
              variant="outline"
              size="icon-sm"
              onClick={() =>
                onSortDirectionChange(sortDirection === "asc" ? "desc" : "asc")
              }
              title={
                sortDirection === "asc" ? directionAscLabel : directionDescLabel
              }
            >
              {sortDirection === "asc" ? (
                <ArrowUp className="size-3.5" />
              ) : (
                <ArrowDown className="size-3.5" />
              )}
            </Button>
          </div>
        </div>

        <div className="px-3 py-2.5">
          <span className="text-xs font-medium text-muted-foreground">
            {columnsLabel}
          </span>
          <div className="mt-2 space-y-2">
            {columnKeys.map((key) => (
              <label
                key={key}
                className="flex cursor-pointer items-center justify-between"
              >
                <span className="text-sm">{columnLabels[key]}</span>
                <Switch
                  size="sm"
                  checked={!hiddenColumns.includes(key)}
                  onCheckedChange={() => onToggleColumn(key)}
                />
              </label>
            ))}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

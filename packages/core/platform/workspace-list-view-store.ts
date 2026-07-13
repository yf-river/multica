"use client";

import { create, type StateCreator } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "./storage";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "./workspace-storage";

type SortDirection = "asc" | "desc";

interface WorkspaceListViewData {
  sortField: string;
  sortDirection: SortDirection;
  hiddenColumns: string[];
  filters: object;
}

interface WorkspaceListViewActions<Data extends WorkspaceListViewData> {
  toggleSort: (field: Data["sortField"]) => void;
  setSortField: (field: Data["sortField"]) => void;
  setSortDirection: (direction: SortDirection) => void;
  toggleColumn: (key: Data["hiddenColumns"][number]) => void;
  toggleFilter: (
    key: Extract<keyof Data["filters"], string>,
    value: string,
  ) => void;
  clearFilters: () => void;
}

export type WorkspaceListViewStoreState<
  Data extends WorkspaceListViewData,
  ExtraActions extends object = Record<never, never>,
> = Data & WorkspaceListViewActions<Data> & ExtraActions;

type StoreSetter<State> = Parameters<StateCreator<State>>[0];

interface WorkspaceListViewStoreConfig<
  Data extends WorkspaceListViewData,
  ExtraActions extends object,
> {
  name: string;
  defaults: Data;
  sortDirections: Record<Data["sortField"], SortDirection>;
  version?: number;
  createExtraActions?: (
    set: StoreSetter<WorkspaceListViewStoreState<Data, ExtraActions>>,
  ) => ExtraActions;
  afterFilterToggle?: (
    state: WorkspaceListViewStoreState<Data, ExtraActions>,
  ) => Partial<Data>;
}

export function createWorkspaceListViewStore<
  Data extends WorkspaceListViewData,
  ExtraActions extends object = Record<never, never>,
>({
  name,
  defaults,
  sortDirections,
  version = 0,
  createExtraActions,
  afterFilterToggle,
}: WorkspaceListViewStoreConfig<Data, ExtraActions>) {
  type State = WorkspaceListViewStoreState<Data, ExtraActions>;
  const persistedKeys = Object.keys(defaults) as Array<keyof Data>;

  const store = create<State>()(
    persist(
      (set) => {
        const update = set as (
          partial: Partial<State> | ((state: State) => Partial<State>),
        ) => void;
        return {
          ...defaults,
          toggleSort: (field) =>
            update(
              (state) =>
                (state.sortField === field
                  ? {
                      sortDirection:
                        state.sortDirection === "asc" ? "desc" : "asc",
                    }
                  : {
                      sortField: field,
                      sortDirection: sortDirections[field],
                    }) as Partial<State>,
            ),
          setSortField: (field) =>
            update(
              (state) =>
                (state.sortField === field
                  ? {}
                  : {
                      sortField: field,
                      sortDirection: sortDirections[field],
                    }) as Partial<State>,
            ),
          setSortDirection: (sortDirection) =>
            update({ sortDirection } as Partial<State>),
          toggleColumn: (key) =>
            update(
              (state) =>
                ({
                  hiddenColumns: state.hiddenColumns.includes(key)
                    ? state.hiddenColumns.filter((column) => column !== key)
                    : [...state.hiddenColumns, key],
                }) as Partial<State>,
            ),
          toggleFilter: (key, value) =>
            update((state) => {
              const filters = state.filters as Record<string, string[]>;
              const values = filters[key]!;
              const next = values.includes(value)
                ? values.filter((candidate) => candidate !== value)
                : [...values, value];
              return {
                ...afterFilterToggle?.(state),
                filters: { ...filters, [key]: next },
              } as Partial<State>;
            }),
          clearFilters: () =>
            set({ filters: defaults.filters } as Partial<State>),
          ...(createExtraActions?.(set) ?? {}),
        } as State;
      },
      {
        name,
        storage: createJSONStorage(() =>
          createWorkspaceAwareStorage(defaultStorage),
        ),
        version,
        partialize: (state) => {
          const persisted: Partial<State> = {};
          for (const key of persistedKeys) {
            (persisted as Record<string, unknown>)[key as string] = state[key];
          }
          return persisted;
        },
        merge: (persisted, current) => {
          if (!persisted) return { ...current, ...defaults };
          const saved = persisted as Partial<State>;
          return {
            ...current,
            ...saved,
            filters: {
              ...defaults.filters,
              ...(saved.filters ?? {}),
            },
          } as State;
        },
      },
    ),
  );

  registerWorkspacePersistStore(store);
  return store;
}

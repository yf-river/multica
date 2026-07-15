"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import type { RuntimeModel } from "@multica/core/types";

export function useRuntimeModelPickerState({
  runtimeId,
  runtimeOnline,
}: {
  runtimeId: string | null;
  runtimeOnline: boolean;
}) {
  const [search, setSearch] = useState("");
  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );
  const supported = modelsQuery.data?.supported ?? true;
  const models = useMemo(
    () => modelsQuery.data?.models ?? [],
    [modelsQuery.data],
  );

  const trimmedSearch = search.trim();
  const filteredModels = useMemo(() => {
    const needle = trimmedSearch.toLowerCase();
    if (!needle) return models;
    return models.filter(
      (model) =>
        model.id.toLowerCase().includes(needle) ||
        model.label.toLowerCase().includes(needle),
    );
  }, [models, trimmedSearch]);
  const exactMatch = hasExactModelMatch(models, trimmedSearch);
  const canCreate = trimmedSearch.length > 0 && !exactMatch;

  return {
    canCreate,
    filteredModels,
    models,
    modelsQuery,
    search,
    setSearch,
    supported,
    trimmedSearch,
  };
}

function hasExactModelMatch(models: RuntimeModel[], value: string): boolean {
  return models.some((model) => model.id === value || model.label === value);
}

import { createSafeId } from "../utils";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import type { MealCommand } from "./schema";
export const mealKeys = { scope: (wsId: string, userId: string) => ["meals", wsId, userId] as const };
export function useMeals(wsId: string) {
 const userId = useAuthStore((s) => s.user?.id) || "";
 const key = mealKeys.scope(wsId, userId);
 const client = useQueryClient();
 const query = useQuery({ queryKey: key, queryFn: () => api.meals(), enabled: !!wsId && !!userId, refetchInterval: 5000, refetchOnWindowFocus: "always" });
 const mutation = useMutation({ mutationFn: (command: MealCommand) => api.mealAction({ ...command, revision: command.revision ?? query.data?.revision ?? 0, requestId: command.requestId || createSafeId() }), onSuccess: (result) => { client.setQueryData(key, result); }, onError: () => { void client.invalidateQueries({ queryKey: key }); } });
 return { query, mutation, key: `${userId}:${wsId}` };
}

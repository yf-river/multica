import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import { api } from "../api";
export function useCookingList(wsId: string, section: string, query: string) {
  const userId = useAuthStore((s) => s.user?.id);
  return useQuery({
    queryKey: ["cooking", userId, wsId, section, query],
    queryFn: () => api.cookingList(section, query),
    enabled: !!wsId,
    refetchOnWindowFocus: "always",
  });
}
export function useCookingDetail(wsId: string, kind: string, id: string) {
  const userId = useAuthStore((s) => s.user?.id);
  return useQuery({
    queryKey: ["cooking", userId, wsId, "detail", kind, id],
    queryFn: () => api.cookingDetail(kind, id),
    enabled: !!wsId && !!id,
    refetchOnWindowFocus: "always",
  });
}
export function useCookingCategories(wsId: string) {
  const userId = useAuthStore((s) => s.user?.id);
  return useQuery({
    queryKey: ["cooking", userId, wsId, "categories"],
    queryFn: () => api.cookingCategories(),
    enabled: !!wsId,
    staleTime: 300000,
  });
}
export function useCookingFavorite() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, favorite }: { id: string; favorite: boolean }) =>
      api.cookingFavorite(id, favorite),
    onSuccess: () => client.invalidateQueries({ queryKey: ["cooking"] }),
  });
}

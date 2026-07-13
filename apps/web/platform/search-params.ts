export type RouteSearchParams = Record<
  string,
  string | string[] | undefined
>;

export function searchParamsSuffix(searchParams: RouteSearchParams): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(searchParams)) {
    if (Array.isArray(value)) {
      for (const item of value) search.append(key, item);
    } else if (value !== undefined) {
      search.set(key, value);
    }
  }
  const query = search.toString();
  return query ? `?${query}` : "";
}

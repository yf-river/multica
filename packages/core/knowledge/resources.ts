export interface KnowledgeEntry {
  uri: string;
  title: string;
  abstract?: string;
  score?: number;
  isDirectory: boolean;
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

export function knowledgeEntries(
  raw: unknown,
  directory?: string,
): KnowledgeEntry[] {
  const outer = record(raw);
  const result = outer?.result ?? raw;
  const inner = record(result);
  const rows = Array.isArray(result)
    ? result
    : Array.isArray(inner?.entries)
      ? inner.entries
      : ["resources", "memories", "skills"].flatMap((key) =>
          Array.isArray(inner?.[key]) ? (inner[key] as unknown[]) : [],
        );
  const seen = new Set<string>();
  return rows.flatMap((item) => {
    const row = record(item);
    if (!row) return [];
    const name = typeof row.name === "string" ? row.name : "";
    const uri =
      typeof row.uri === "string"
        ? row.uri
        : directory &&
            name &&
            !name.includes("/") &&
            name !== "." &&
            name !== ".."
          ? `${directory.replace(/\/$/, "")}/${encodeURIComponent(name)}`
          : "";
    if (!uri || seen.has(uri)) return [];
    seen.add(uri);
    return [
      {
        uri,
        title:
          typeof row.title === "string"
            ? row.title
            : name || uri.split("/").pop() || uri,
        abstract:
          typeof row.abstract === "string"
            ? row.abstract
            : typeof row.summary === "string"
              ? row.summary
              : undefined,
        score: typeof row.score === "number" ? row.score : undefined,
        isDirectory:
          row.isDir === true || row.is_dir === true || row.type === "directory",
      },
    ];
  });
}

---
name: multica-knowledge
description: "Use when a task may depend on workspace documentation, decisions, or prior knowledge: search the workspace knowledge base before answering from memory, then cite the viking:// URI you used."
user-invocable: false
allowed-tools: Bash(multica knowledge *)
---

# Using the workspace knowledge base

The workspace knowledge base is an OpenViking namespace scoped to this
workspace. Multica proxies it, so the CLI never needs an OpenViking URL or key.

## Search before assuming

```bash
multica knowledge search "<specific query>" --limit 5
```

- Query with terms that would appear in the source (product names, error
  strings, API names), not with the whole user sentence.
- If results look irrelevant, refine once with synonyms; do not keep retrying
  the same query.
- Results include `uri`, `title`, `abstract` and `score`. Use `score` only to
  rank candidates; verify the content before relying on it.

## Read only what you need

```bash
multica knowledge list [--uri <directory>]
multica knowledge read <viking://.../file.md> --offset 0 --limit 200
```

- Default `read` returns the whole file; for long documents page with
  `--offset`/`--limit` rather than dumping everything into context.
- Prefer the smallest passage that answers the question.

## Cite and qualify

- When an answer depends on knowledge-base content, include
  `source: <viking://...>` so the reader can open it.
- Knowledge can be stale or describe an older environment. If it conflicts
  with the current repository, live API response, or a newer task comment, say
  so and prefer the live evidence.
- If nothing relevant is found, say that the workspace knowledge base has no
  match; do not invent a source.

## Scope

`multica knowledge` always searches this workspace's namespace
(`viking://resources/workspaces/<workspace-id>`). Do not pass URIs from another
workspace; the server rejects them.

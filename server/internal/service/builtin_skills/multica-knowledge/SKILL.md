---
name: multica-knowledge
description: "Use when a task may depend on shared documentation, decisions, or prior knowledge: search the knowledge base before answering from memory, then cite the viking:// URI you used."
user-invocable: false
allowed-tools: Bash(multica knowledge *)
---

# Using OpenViking knowledge

Shared resources live in `viking://resources` in the default OpenViking account.
Multica automatically recalls the current Agent user's memories before execution.
The CLI can read shared resources, shared Skills, and this Agent user's own space.
Credentials remain with the platform.

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

Default searches cover `viking://resources`. Use `--path` with a directory URI
when a narrower search is useful. Shared Skills live in `viking://agent/skills`.
Use the OpenViking Studio entry in Multica for resource curation.

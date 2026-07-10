# Skill-importing source map

Evidence for the current multica-skill-importing contract. Paths are relative
to the repository root. Re-run the symbol searches below after code moves;
symbols, not old line numbers, are authoritative.

## Verification

    rg -n "func .*ImportSkill|func detectImportSource|func parseGitHubURL" server/internal/handler/skill_import*.go
    rg -n "func runSkillImport" server/cmd/multica/cmd_skill.go
    go test ./internal/service -run "TestSkillImportingSkill|TestBuiltinSkillsConformToTemplate"

## HTTP import boundary

| Contract | Current source |
|---|---|
| POST /api/skills/import route | server/cmd/server/router.go:970 |
| Request and response data types | server/internal/handler/skill.go:36-114,548-563 |
| Import handler | server/internal/handler/skill_import_github.go:904 |
| on_conflict validation | server/internal/handler/skill_import.go:15; handler use at skill_import_github.go:922 |
| Source detection and URL normalization | server/internal/handler/skill_import.go:221 |
| GitHub tree/blob parsing | server/internal/handler/skill_import_github.go:472 |
| Provenance stored in config.origin | server/internal/handler/skill_import_github.go:959-967 |
| Create skill and files transaction | server/internal/handler/skill_create.go:29-77 |
| Import create wrapper | server/internal/handler/skill_import_github.go:796 |

The handler currently distinguishes callers that send on_conflict from callers
that omit it at skill_import_github.go:926-1011. Current CLI calls always send
the field. Any removal of the omitted-field response requires an external
contract decision and must update this map and its tests together.

## Conflict behavior

| Strategy | Current source |
|---|---|
| Shared conflict dispatcher | server/internal/handler/skill_import_github.go:835 |
| fail -> 409 conflict | server/internal/handler/skill_import_github.go:893-900 |
| skip -> existing skill unchanged | server/internal/handler/skill_import_github.go:838-844 |
| overwrite -> creator gate and transactional replacement | server/internal/handler/skill_import_github.go:845-874; server/internal/handler/skill_create.go:133 |
| rename -> bounded suffixed create | server/internal/handler/skill_import_github.go:806-820,875-891 |
| Unique-race resolution | server/internal/handler/skill_import_github.go:987-1003 |

## CLI

| Contract | Current source |
|---|---|
| skill import command and flags | server/cmd/multica/cmd_skill.go:60-64,142-144 |
| runSkillImport | server/cmd/multica/cmd_skill.go:400 |
| Request POST and structured error/result handling | server/cmd/multica/cmd_skill.go:416-485 |

## Agent skill assignment

| Contract | Current source |
|---|---|
| PUT agent skills replaces all | server/internal/handler/skill_import_github.go:1128; route router.go:946 |
| POST agent skills/add is additive | server/internal/handler/skill_import_github.go:1183; route router.go:947 |
| CLI list/set/add | server/cmd/multica/cmd_agent.go:740-838 |

## Reserved SKILL.md

| Contract | Current source |
|---|---|
| Reserved path definition | server/internal/skill/reserved.go:8-27 |
| Create/import silently drops a supporting SKILL.md | server/internal/handler/skill_create.go:50-54 |
| Replace-files update skips it | server/internal/handler/skill.go:418-500 |
| Dedicated file upsert rejects it | server/internal/handler/skill_import_github.go:1036-1062 |

The primary content writer owns SKILL.md; supporting files cannot claim the
same path.

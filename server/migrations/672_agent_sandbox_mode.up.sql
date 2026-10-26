-- Per-agent filesystem sandbox stance for the providers that can enforce it.
--
-- Values deliberately mirror the runtime vocabulary (DSH and Codex both speak
-- read-only / workspace-write / danger-full-access) so the value the user picks
-- is the value the runtime receives, with no cross-provider translation table.
--
-- The default is danger-full-access because Multica does not sandbox the
-- filesystem for agents (docs/content/docs/security-model.zh.mdx); Codex runs
-- with danger-full-access and Claude Code with bypassPermissions. The DSH path
-- used to inherit DSH's own workspace-write default instead, which hid the
-- per-task TMPDIR behind an ephemeral /tmp and made the Go build cache
-- read-only — a task could not run its own tests. Making the stance explicit
-- on the agent both restores the documented default and lets the settings page
-- expose (and narrow) it.
ALTER TABLE agent
    ADD COLUMN sandbox_mode TEXT NOT NULL DEFAULT 'danger-full-access'
    CONSTRAINT agent_sandbox_mode_check
    CHECK (sandbox_mode IN ('read-only', 'workspace-write', 'danger-full-access'));

# Changelog

本文件记录 multica 仓库的阶段性变更。每个版本对应一次提交批次，按时间倒序排列。

## 2026-07-25 — 前端死代码彻底清理（第二轮）

在移除 apps/desktop 与 onboarding UI 层的基础上，二次扫描并清理 apps/ 与 packages/ 中的所有死代码。

### 删除

- **孤儿目录与文件**：
  - `packages/core/dashboard/`（整目录，DashboardPage 零引用）
  - `packages/core/markdown/`（整目录，零消费方）
  - `packages/core/onboarding/` 的 `store.ts`、`step-order.ts`、`needs-backfill.ts`(+test)（函数零消费方）
  - `packages/core/agents/use-workspace-presence-prefetch.ts`（旧消费方已删）
  - `packages/ui/components/common/mention-hover-card.tsx`（零 import）
  - `packages/core/api/client.ts` 中 4 个 `getDashboard*` API 方法
- **14 个零引用 UI 原子组件**：button-group、empty、breadcrumb、field、direction、drawer、kbd、carousel、spinner、pagination、combobox、aspect-ratio、data-table-column-header、item
- **apps/web fumadocs-mdx 整套**：`fumadocs-core`/`fumadocs-mdx` 依赖、`createMDX()` 包装、`source.config.ts`、`package.json` 脚本调用（apps/web 已无 .mdx 内容）
- **3 个死依赖**：`embla-carousel-react`、`vaul`、`date-fns`（对应组件已删，全仓零引用）
- **5 个死类型**：`OnboardingStep`、`Source`、`Role`、`UseCase`、`QuestionnaireAnswers`（仅留 `OnboardingCompletionPath`）
- **onboarding barrel**：`packages/core/onboarding/index.ts` + `packages/core/package.json` 的 `./onboarding` exports
- **206 个 i18n dead key**：分布在 14 个 json 文件，全是代码零引用的翻译 key
- **连带清理**：4 个 dashboard 类型定义、4 个 dashboard schema、schemas.test.ts 的 dashboard 测试用例、knip.json 中的死依赖 ignore 条目

### 修改

- `packages/core/analytics/index.ts`：`detectClientType()` 简化为恒返回 `"web"`，`ClientType` 简化为 `"web"`
- `packages/views/platform/open-external.ts`：简化为直接 `window.open`，移除 desktop 分支
- `packages/core/onboarding/types.ts`：仅保留 `OnboardingCompletionPath` 类型

### 验证

- `pnpm typecheck` 5 包通过
- `pnpm test` 7 包通过（1189 测试）
- `pnpm lint` 通过
- `pnpm knip` 干净（无未使用文件/导出/依赖）

### 影响

累计 117 文件变更，+75/-8877 行。仓库中每一行代码都有人用、每一个导出都有人 import、每一个 i18n key 都有界面显示、每一个依赖都被代码引用。

---

## 2026-07-24 — 移除 desktop 应用与残留依赖

### 删除

- `apps/desktop/` 目录（119 文件，4.6M）与硬依赖（pnpm-workspace、turbo.json、knip.json、CI workflow、font-fallback 测试）
- onboarding UI 层（`packages/views/onboarding/`，保留 `templates/` 子目录）
- `packages/core/analytics/download.ts` 与 re-export
- platform 防御分支：`local-directory.ts`、`use-local-daemon-status.ts`、`local-directory-hint.tsx`、`use-desktop-unread-badge.ts`、`use-immersive-mode.ts`
- server 端 `onboarding_shim.go`、`LaunchedBy` 字段、`PlatformDesktop` 常量
- apps/web login page 的 `isDesktopHandoff` 分支与对应测试
- i18n dead key：`auth.json` 的 `desktop_handoff`、`onboarding.json` 死段、`settings.json` 的 desktop 块、`runtimes.json` 的 `managed_by_desktop`
- 文档站 `desktop-app.zh.mdx` 与各 mdx 的 desktop 引用
- `docs/analytics.md` desktop 段、`docs/design.md` desktop 路径引用

### 验证

- `pnpm typecheck`/`test`、`go build`/`test` 全通过
- 218 文件变更，+1539/-24010 行

---

## 2026-07-24 — 根目录文档职责收敛与中文化

### 变更

- README/AGENTS/CLAUDE/CONTRIBUTING/CLI_*/SELF_HOSTING* 职责收敛与中文化
- AGENTS 精简为入口指针，CLAUDE 修正仓库路径与验证策略表述
- `goal.md` 归档至 `docs/archive/goal-test/`
- Makefile/package.json 输出与配置注释中文化
- ignore 文件、Dockerfile、Compose、.goreleaser.yml、playwright.config.ts、pnpm-workspace.yaml 注释中文化

---

## 2026-07-24 — 统一 in_review 中文翻译

### 变更

- `in_review` 中文翻译从「审核中」统一为「验收中」
- 涉及 `issues.json`、`search-command.test.tsx`、`issue-detail.test.tsx`

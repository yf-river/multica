---
name: prod-data-cleanup
description: Clean goal-test production-stable test data through public APIs with dry-run-first evidence and explicit apply confirmation. Use when goal-test production data needs to be reset for testing.
---

# goal-test 生产环境清测试数据

## 适用场景

这个 skill 只用于 `/data/ida/goal-test` 的生产稳定测试环境清数据，目的是清掉反复验收、E2E、curl、训练闭环、演示验证产生的临时测试数据，让后续测试回到可控状态。

它不是客户真实生产数据清理流程，也不是数据库运维 runbook。

有两种模式：

- `prune`：保守清理，只删除脚本识别出的测试数据。
- `full-reset`：从头测试前使用，备份后清空业务数据，只保留最小登录壳。

## 强约束

- 默认只做 dry-run。没有用户在当前任务里明确说可以执行 `APPLY=1`，不要真正删除或归档数据。
- 只打 goal-test 生产稳定环境：后端 `http://127.0.0.1:18760`，前端 `http://9.134.129.162:13680`，生产环境文件 `.run/env/goal-test-prod.env`。
- 优先通过公开 API 清理。不要直接 `DELETE`、`TRUNCATE` 或手改数据库，除非用户明确要求数据库级清理并接受风险。
- 用户说“完全清除”“整体清理”“从头测试”时，按 `full-reset` 处理，但必须先备份数据库。
- dry-run 计划里只要出现 canonical seed 或稳定演示资产，就停止，不要 apply。
- 不要把 integration/dev 清理证据当作 production 清理证据。

## 保留对象

清理前必须确认计划不会删除这些长期对象：

- 项目：`usercenter`、`gateway`、`ida-deployment`
- 智能体：`pm`、`01`、`02`、`03`、`04`、`05`
- 小队：`pm`
- 稳定提示词、稳定业务训练资产、当前用户要求保留的演示数据

如果需要把生产环境压回 SOP canonical 形态，只能在用户明确要求后使用 `CANONICAL_SOP_ONLY=1`。

## 标准流程

### prune 模式

1. 确认当前仓库和生产环境状态：

   ```bash
   git status --short
   node scripts/goal-test-environments.mjs verify prod
   ```

2. 先做 dry-run，不会清数据：

   ```bash
   make goal-test-prune-prod-data KEEP=5
   ```

3. 审查产物：

   ```bash
   jq '.target_environment, .mode, .backend_url, .summary, [.categories[] | {id, planned_count, planned}]' artifacts/acceptance/prod-data-prune-latest.json
   ```

   继续前必须确认：

   - `target_environment` 是 `prod`
   - `mode` 是 `dry-run`
   - `backend_url` 指向生产后端
   - `planned` 里没有保留对象
   - `failed_count` 是 `0`

4. 只有用户明确确认后才能 apply：

   ```bash
   APPLY=1 KEEP=5 make goal-test-prune-prod-data
   ```

5. apply 后做验证并记录证据：

   ```bash
   node scripts/goal-test-environments.mjs verify prod
   node scripts/goal-test-environments.mjs verify-logs prod
   jq '.mode, .summary' artifacts/acceptance/prod-data-prune-latest.json
   ```

### full-reset 模式

1. 确认生产环境，并备份数据库：

   ```bash
   node scripts/goal-test-environments.mjs verify prod
   mkdir -p artifacts/backups
   ```

   使用 PostgreSQL 客户端或 `postgres:16-alpine` 容器执行 `pg_dump --format=custom --no-owner --no-acl`，备份 `multica_goal_test_680`，并用 `pg_restore --list` 和 `sha256sum -c` 校验。

2. 清空业务数据，只保留最小登录壳：

   - 保留：`schema_migrations`、`user`、`workspace`、`member`，且最终只保留 `goal-test-daemon` 用户、`goal-test-daemon` workspace 和 owner member。
   - 清空：project、issue、agent、squad、prompt library、training assets/runs、inbox、trace、usage、runtime profile、external credential profile 等业务表。
   - 若登录后系统自动补回 starter Agent/小队，设置 `user.starter_content_state='imported'` 后再次清空 `agent`、`squad`、`agent_runtime` 相关业务记录。

3. 验证：

   ```bash
   node scripts/goal-test-environments.mjs verify prod
   node scripts/goal-test-environments.mjs verify-logs prod
   ```

   通过生产 API 登录 `goal-test-daemon` 后确认：projects/issues/agents/squads/prompts/assets/runs/inbox 全部为 `0`，workspace/user/member 均为 `1`。

## 停止条件

遇到以下任一情况必须停止并向用户说明：

- 生产环境 verify 失败
- dry-run 产物缺失或不是 `prod`
- 计划清理对象包含保留对象
- 清理范围无法用公开 API 解释清楚
- full-reset 前数据库备份没有完成或校验失败
- apply 后 `failed_count` 大于 `0`
- 用户只要求新增 skill，没有明确授权实际清理

## 交付说明

汇报时说明是否执行了 apply。只新增或修改 skill 时，要明确说“没有清理生产数据”。真正执行清理时，给出命令、产物路径、planned/applied/failed 数量和 post-verify 结果。

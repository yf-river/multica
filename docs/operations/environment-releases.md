# 单套生产环境发布

现有 prod 是唯一常驻实例。地址、进程、版本从 `.run/deployments/goal-test-prod.json` 读取，私有配置真源为 `.run/env/prod-services.json`。数据库、账号、密钥、上传文件、Daemon profile 和会话属于生产，发布不复制其他环境的数据，不初始化账号，不修改已安装版本。历史路径和 `prod` Cookie 标识无需迁移。

## 日常操作

```bash
pnpm release build
pnpm release request <release-id>
pnpm release request-status <request-id>
pnpm release status
pnpm release verify
pnpm release verify-logs
pnpm verify:release
pnpm release rollback
pnpm release backup
```

Makefile 提供对应的 `release-build`、`release-request RELEASE=<id>`、`release-status REQUEST=<id>`、`release-verify`、`release-logs`、`release-rollback` 和 `release-backup`。

普通开发先完成相关检查；生产发布、提交和推送按用户授权执行。单机实例发布与公开 CLI 发版独立，不要求 main 合并、公开 tag 或 Homebrew 发布。int 操作明确报错，不创建环境、不转发生产。

## 构建、切换与自更新

- 构建收集已跟踪修改及未忽略文件，需先检查快照范围。源码快照放在 `.run/build/<id>`，包放在 `.run/artifacts/<id>`；版本同时记录 Git SHA 和源码摘要，包内文件有校验摘要。构建期间服务继续使用安装目录。
- `request` 持久化到 `.run/publish-requests/<id>.json`，通过 systemd 启动独立的发布进程，执行代码复制到 `.run/publisher/`。请求立即返回，Multica 内的任务应随后正常结束，不同步轮询等自己发布完成。
- 请求状态为 queued、running、succeeded、failed 或 rolled_back；结果跨应用重启保存。相同版本重复请求复用原请求，不并行重启。失败后显式使用 `request <id> --retry`；失去最终结果的外部进程由状态检查标为失败，不声称成功。
- 外部发布进程使用发布锁，暂停新认领，等待已有任务与在途认领全部结束，再切换 Web、Server 和 Daemon。默认等待 900 秒，可用 `--timeout <seconds>` 指定；超时恢复接单，任务不被取消。排队任务在更新后继续处理。
- 任务外可以执行 `pnpm release deploy <id>`，同步完成切换及浏览器验收；任务内只允许 request。已运行同版本不重启，但仍验收。
- 启动失败尝试恢复旧程序与运行配置。启动成功而浏览器验收失败时，发布进程切回发布前版本并再次验收；恢复失败会明确报错。回滚依赖生产当前/上一版本及对应不可变包，不能删除这些包。
- 安装目录为 `.run/releases/prod/<id>`。现有配置、上传、状态、任务目录和 Daemon profile 沿用部署记录，不随发布替换。新版本不会改动孤儿任务恢复、重试和工作目录接续规则。

## 数据库与备份

发布命令不执行数据库迁移。迁移须核对真实台账差异、向前/向后兼容性并先备份，不能重放未经审核的历史迁移。程序回滚不撤销数据库变更。

`backup` 将生产数据库导出为 `.run/backups/prod-<UTC>.dump`，权限 0600，使用本机 pg_dump 或按实际端口唯一识别的 PostgreSQL 容器。备份和凭证不提交 Git。重要文件同时备份部署记录所指向的上传目录；数据库备份不包含原始文件。

恢复时暂停生产认领并等任务结束，停止应用，确认目标服务器和数据库，使用与备份兼容的 pg_restore 将备份恢复到明确指定的空恢复数据库，核对内容后再进行经授权的数据切换；不要直接对运行中的生产执行 `pg_restore --clean`。数据库、上传原件和应用版本应相互匹配。恢复属于独立的高风险操作，不由日常发布自动执行。

## 验收与临时测试

`verify` 检查实际进程归属、配置、API 身份及 Daemon 绑定；`verify-logs` 检查本次发布日志的致命错误。浏览器验收使用现有 profile 身份，或者明确提供 `MULTICA_TEST_ACCOUNT`、`MULTICA_TEST_PASSWORD`，检查刷新、主要入口、已有聊天与报告原件、CSRF 和 390px 侧栏；缺少既有聊天、Mika 或报告的场景单独报告证据缺口，不制造用户数据来冒充覆盖。

独立测试环境须明确授权并在启动命令上设置 `MULTICA_LOCAL_DEV=1`，不写入环境文件。数据库故障实验另外要求 `MULTICA_TEST_ENV_FILE` 和 `MULTICA_TEST_DEPLOYMENT_PATH`，拒绝生产数据库。临时环境不是发布前置条件，结束后只清理本次创建的明确资源。

通用 Playwright 造数测试使用明确授权的临时环境，不通过测试登录重写生产姓名或创建测试账号；生产日常浏览器验收使用 `pnpm verify:release`。性能工具从生产部署记录读取地址、版本和现有身份，故障实验须显式隔离。

## int 清理

一次性迁移工具 `node scripts/retire-integration.mjs` 先核对资源并输出精确清单，`--execute` 在生产验收已有证据后执行。它等待 int 任务结束，验证进程归属，删除 int 专属数据库、配置、上传、状态、干净任务目录、安装目录及自动启动服务；共享数据库角色、生产当前/上一版本包及共享构建快照保留。任务目录存在未提交工作或归属不清则拒绝清理。该工具没有初始化或重建 int 的能力，完成后不会成为发布依赖。

## 受影响入口对账

| 真源 | 消费者与职责 |
| --- | --- |
| `scripts/release-environments.py` | Node 操作入口、pnpm release、Makefile release-*；构建、发布请求、切换、回滚、备份、状态及日志 |
| `scripts/lib/goal-test-audit-env.mjs` | 浏览器认证辅助、菜单性能测量与回归、工作台点击审计；读取现有生产记录，无 int 默认值 |
| `scripts/verify-release-browser.mjs` | 外部发布进程及人工验收入口；真实浏览器、已有身份、主要页面与窄屏 |
| 开发启动保护与 E2E 环境入口 | Makefile、Web dev、dev.sh、通用 TestApiClient；明确选择临时环境，防止测试登录修改生产资料 |
| 故障与专项工具 | 生活数据故障测试、四象限浏览器测试、烹饪限定迁移；生产归属或显式隔离及审核 |
| 仓库与开发者说明 | AGENTS、CLAUDE、README、CONTRIBUTING、开发者中文文档及生活模块运维说明；单实例交付与公开发版分离 |
| int 一次性清理 | 实际数据库、路径、进程及 systemd 开机入口；共享资源不删，完成后无日常消费者 |

性能报告中旧测量的联调版本、隔离实验和样本说明属于历史证据，不能改成生产实测；新测量工具使用当前部署记录。

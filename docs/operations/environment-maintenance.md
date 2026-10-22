# 固定生产、联调环境维护

本页提供环境、存储、备份与事故留证的维护入口。连接真源是固定控制仓库 `/data/ida/multica/.run/env/{prod,int}-services.json`，运行状态同时核对对应后端进程。根 `CLAUDE.md` 的“环境身份与生产数据保护”规定授权边界；发布步骤见 [环境发布](environment-releases.md)。

## 当前两套环境

以下为 2026-10-22 恢复后的现场核验记录，不是运行配置。变更后更新本表；任何数据库操作仍需重新核对配置和真实进程，不能只照抄表格或使用任务工作树的 `.env.worktree`。

| 项目 | 生产 prod | 固定联调 int |
| --- | --- | --- |
| 用户入口 | http://9.134.129.162:13680/gongzuo/issues | http://9.134.129.162:13682/ai-studio/issues |
| Web / API 端口 | 13680 / 18760 | 13682 / 18762 |
| PostgreSQL 地址 | localhost:55434 | localhost:55434 |
| 数据库 | `multica_goal_test_680` | `multica_goal_test_int` |
| 私有服务配置 | `.run/env/prod-services.json` | `.run/env/int-services.json` |
| 部署记录 | `.run/deployments/goal-test-prod.json` | `.run/deployments/goal-test-int.json` |
| 上传目录 | `.run/uploads/goal-test-prod` | `.run/uploads/goal-test-int` |
| Agent 工作目录 | `/data/cbs/worktree/prod` | `/data/cbs/worktree/int` |
| Daemon profile | `/root/.multica/profiles/goal-test-prod/config.json` | `/root/.multica/profiles/goal-test-int/config.json` |

相对路径基于 `/data/ida/multica`。程序 PID、提交、模型账号、Token 和密钥不在本页维护；从当前部署记录、私有配置和进程现场读取，不输出完整环境变量或含密码的 DATABASE_URL。仅两套固定环境在提供产品服务，其他库的存在不代表新增产品环境。

## 数据库实例与存储

| 实例 | 状态与用途 | 维护边界 |
| --- | --- | --- |
| `multica-postgres-live`，55434 | 正式集群，共用实例内的生产和联调相互独立 | 停容器会影响两套环境；核对业务和活动任务后按发布规则维护 |
| `multica-life-complete-pg17`，55432 | 旧集群；20 个非系统库已授权归档、验证并删除，现仅有系统库 | 容器保留，不作为产品数据库使用 |
| `multica-postgres-1`，55433 | 原事故现场，PAUSED，重启策略 no | 禁止随意解冻、启动写入或删除卷 |
| `multica-recovered-db-20261022` | 阶段恢复集群，已停止，重启策略 no | 留存恢复检查点，不与正式集群同时启动 |
| `multica-recovery-20261022` | 较早恢复容器，已停止 | 可能与阶段容器共享存储，清理前核对 Mounts，不能按容器名推断卷独立 |

正式 PGDATA：`/data/ida/multica-state/postgres`。Compose：`/data/ida/operations/multica-postgres/compose.yaml`。该 Compose 用于现有已初始化集群，不能直接当作空卷的完整初始化方案。

### 当前数据库与历史归档

2026-10-22 16:30 清理后核验：正式集群仅保留生产 `multica_goal_test_680`、联调 `multica_goal_test_int` 和 `postgres`、`template0`、`template1` 系统库。旧 55432 集群仅保留三个系统库。

用户已明确授权将正式集群中的 `multica_backup_20261021_1627`、`multica_recovery_validate`、`recovery_forensics`、`multica`，以及旧集群全部 20 个非系统库备份到独立盘，并在验证后删除。

24 个库均使用一致快照导出，在独立实例完成实际还原及全部业务 schema 表清单、行数核对；删除前核对当前配置、运行进程、原库 OID 与无客户端连接，没有使用强制断开。归档位于 `/data/cbs/multica-database-archives/20261022-retired-databases/`，完整名单、SHA256、逐库删除及验证结果见其 `README.md`、`backup-complete.json`、`verify-complete.json`、`deletion-complete.json`。

恢复副本转为归档保存，原事故冻结容器、阶段恢复容器、原始磁盘及内存取证材料继续保留。上述清理没有删除它们。暂停或停止集群未为盘点而启动，其数据不属于当前两套环境在线库清单。

## 只读核对入口

- `pnpm release status`、`pnpm release verify`：查看固定环境当前部署与健康结果。健康成功不能代替数据库内容和业务页面验收。
- 查看固定 `.run/env/` 配置中的连接时，仅提取 host、port、database；与部署记录中的运行 server PID 对应进程的真实连接核对，不泄露密码。
- `docker ps -a` 及 `docker inspect <已核实容器>`：检查状态、端口、挂载与重启策略；禁止为盘点启动或解冻事故容器。
- 在已确认的在线实例执行只读查询，列出 `pg_database`、`pg_stat_activity`，区分库所有者角色、当前客户端和真实业务消费者。库的 owner 同为 multica 无法证明用途相同。

维护前确认环境、活动任务、用户授权范围及备份。生产结构变更先经过固定 int 和发布流程；调查默认只读，禁止以修复测试或迁移报错为由手工改生产库表、ledger 或批量业务记录。

## 发布窗口与验收槽位的人工收尾

两项占用都优先自动恢复，下面的入口只在自动恢复失败时使用，且都需要工作区管理员身份。

- 发布窗口：`POST /api/delivery-queue/window`，`{"id":"<窗口 id>","action":"abandon"}`。窗口记为 `finished`，未确认停止的暂停任务记为 `skipped` 并保留原因，认领随即恢复；任务本身保持 `deferred`，需要人工重新派发。超过 2 小时未更新的窗口不再阻塞认领，新发布 `begin` 时会自动收尾并说明原因。
- 验收槽位：`POST /api/delivery-queue/slots/configure`，`{"name":"verify-1","action":"reset","reason":"<原因>"}`。隔离态可立即收回，其他状态要等 2 分钟租约过期；收回写入 `verification_event` 的 `reset` 记录。

两处都只改占用归属，不删除交付记录和任务，也不重放已完成的部署。

## 备份与恢复

本次额外部署的服务器备份机制位于 `/data/ida/operations/multica-backup/`，不是 Multica 上游产品内置功能。线上文件是运行入口，文档目录中的迁移包是归档副本；修改机制时同步归档。

| 项目 | 当前配置 |
| --- | --- |
| 目标 | 固定 prod/int 两个库及各自上传文件、必要私有配置 |
| 调度 | `multica-backup.timer` 每小时；`multica-backup-verify.timer` 每日 04:30 |
| 保留 | 24 个小时快照、7 个每日快照 |
| 位置 | `/var/backups/multica`，与正式数据库分处不同磁盘，仍为同机 |
| 验证 | 实际恢复到本次随机新建的库，核对全部 public 业务表数量与引用文件哈希，只清理本次演练库 |
| 状态入口 | `/var/backups/multica/latest.json`、`last-restore-check.json`、systemd journal |

手动生成备份用 `systemctl start multica-backup.service`，手动演练用 `systemctl start multica-backup-verify.service`；两者共用互斥锁，避免并发。日常详见 `/data/ida/operations/multica-backup/README.md`。

旧显式发布备份位于项目 `.run/backups/`，当前未配置定期清理；新机制不清理它们。备份只覆盖所列内容，Agent 模型登录、SSH 配置、代码和未提交的任务工作目录还需单独保全。当前没有异地备份或连续 WAL 时间点恢复；小时备份正常成功时仍可能损失最近约一小时新增数据。

事故证据及完整总结：`/data/ida/recovery-multica-20261022/RESTORE_STATUS.md`、`/data/ida/docs/tapd/20261022-multica数据恢复/`。备份脚本、四个 systemd 单元、Compose 与迁移说明归档在后者的 `备份机制迁移包/`，不含业务数据和密钥。

## 清理决策与能力边界

2026-10-22 指定 24 个库的归档清理已完成，此授权不扩展到后续新建数据库或原事故现场。未知用途的库先核对消费者，准备可还原归档，再取得针对准确实例和库名的明确删除授权。

文档约束不等于数据库权限隔离或技术防误删机制。本次没有变更角色权限、实现危险命令拦截或配置新的清库定时任务。后续清理不能通过恢复备份、盘点或普通开发的授权推导获得。

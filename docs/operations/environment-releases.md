# 联调验收与生产自动晋级

`int` 是小队代码交付的固定联调环境，`prod` 是真实生产环境。两套环境使用独立数据库、账号、Cookie、上传目录、Daemon profile、工作目录和端口。小队任务只能提交发布请求；外部 publisher 等待任务自然完成后，先部署 int，完成完整证据验收，再自动把同一个不可变 release 提升到 prod。

## 日常流程

```bash
pnpm release build
pnpm release request <release-id>
pnpm release request-status <release-id>
pnpm release verify
pnpm release verify-logs --env int
pnpm release verify-logs --env prod
```

发布状态会持久保存。`request` 立即返回，任务不能同步等待自身发布。publisher 的顺序固定为：等待任务自然完成、部署 int、浏览器/API/WebSocket/真实任务验收、写入完整 int 证据、部署 prod、生产验收、观察窗口。int 失败不会触碰 prod；prod 启动或验收失败会切回上一份已验收包。

## 生产晋级证据

晋级必须同时具备同一 `release_id` 和 `source_digest` 的：任务自然完成记录、部署身份与进程归属、API 和 WebSocket、认证与 CSRF、主要页面和 390px 页面、真实 Agent 任务终态、工作目录与产物、零致命日志、数据库隔离和浏览器证据。健康接口 200、代码修改完成或发布请求已提交都不能单独触发晋级。

证据写入 `.run/evidence/int-release/<release-id>/`，publisher 校验后写入 `promotion.json`。证据缺字段、对应错误版本、任务取消/超时、环境串线或浏览器失败请求都会停止流程。

## int 数据与重置

int 使用固定测试账号、工作区、Agent、Squad 和任务种子。需要恢复干净状态时运行：

```bash
pnpm release reset --env int
```

reset 只允许操作 int 数据库，执行迁移、写入固定种子并重新验证环境身份；不得复制生产用户数据或上传原件。验收创建的临时记录由验收脚本清理。

## 故障恢复

发布器使用环境锁、Daemon drain、不可变安装目录和上一版本包。启动失败、浏览器验收失败或观察窗口失败时自动回滚并保存证据。独立 systemd watchdog 定期检查 prod 进程、健康接口和 release 身份，发现发布后崩溃时直接暂停认领并执行回滚，不依赖 Multica API 或任务入口。

数据库迁移发布前自动备份并先在 int 执行。程序回滚不撤销数据库结构和已经写入的数据；迁移失败或数据影响需要根据保存的备份和台账人工补救。

## 关键入口

| 真源 | 职责 |
| --- | --- |
| `scripts/release-environments.py` | 构建、双环境部署、发布请求、证据门禁、回滚、备份 |
| `scripts/verify-integration-release.mjs` | 校验 int 任务完成、浏览器证据、日志和晋级资格 |
| `scripts/verify-release-browser.mjs` | 按 `GOAL_TEST_ENV=int|prod` 验收真实浏览器 |
| `scripts/release-watchdog.mjs` | 独立检查 prod 并自动回滚 |
| `ops/multica-release-watchdog.*` | watchdog 的 systemd service/timer 模板 |

缺少 int 配置时停止并报告阻塞，不把 prod 当联调环境，也不创建第三套预览环境。

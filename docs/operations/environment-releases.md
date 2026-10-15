# 固定联调与生产更新

开发任务在独立 worktree 修改代码，在固定 int 验证功能，再将同一份运行产物应用到 prod。Git 保存源码历史；两个环境各自保留当前运行文件，更新期间临时多留一份，验证成功后删除旧文件。不维护发布包仓库，也不强制 MR 审批。

## 开发入口

候选必须是干净 Git 提交。提交、推送、MR 及部署按用户授权执行；明确要求先 review 或仅本地修改时停在对应边界。SOP 使用平台准备的任务分支，不额外建立分支。

```bash
# 在固定发布控制目录调用，源码来自任务 worktree。
pnpm --dir /data/ida/multica release int --source /absolute/task/worktree
# 上一步返回候选编号。开发 Agent 在 int 验证本次功能后记录结果。
pnpm --dir /data/ida/multica release accept <candidate> --evidence /absolute/acceptance.json
pnpm --dir /data/ida/multica release request <candidate>
pnpm --dir /data/ida/multica release request-status <candidate>
```

验收文件保存在任务源码目录之外，避免使已提交源码变脏。格式如下，内容必须来自实际功能验证，脚本只检查记录完整性，不能判断 Agent 的陈述是否真实：

```json
{
  "candidate": "上一步返回的候选编号",
  "summary": "本次功能的验证结论",
  "checks": [
    {"name": "本次修改涉及的用户操作", "status": "passed", "evidence": "实际操作、结果与证据位置"}
  ]
}
```

基础浏览器/API/WebSocket检查由部署入口执行，本次功能验证由开发 Agent 完成。证据、int 部署身份或候选源码发生变化时，不能晋级原候选。需要长时间修复或等用户决定时执行 `pnpm release release <candidate>` 释放 int，修复后重新排队。

## 并发与源码

多个任务并行开发，一次只有一个候选占用 int，从构建到生产更新期间都不能被其他任务覆盖。取得使用权后才编译。占用时立即返回，其他 Agent 完成独立工作后结束当前运行，之后重试；不能在运行内同步等待，否则会与生产更新等待运行结束形成循环等待。`--timeout`（默认 900 秒）用于等待运行退出。发布进程异常退出或自动恢复失败（`recovery_required`）时保留占用，先检查日志及运行身份，再恢复并释放；不能让后续更新覆盖恢复资料。

生产成功提交记录在 `.run/published.json`。下一候选必须包含该提交，冲突由开发 Agent 解决，不由发布脚本自动处理。首次接管旧部署时，现有生产的提交和内容摘要必须一致；若原生产包含未提交代码，需要先核对并保存源码基线，不能仅凭提交号跳过检查。保存了与当前生产内容完全一致的干净 checkout 后，通过 `pnpm release baseline --source <checkout>` 登记首次基线；命令核对内容摘要，不支持强制跳过。

仅配置远端仓库不等于启用了本地 worktree。项目资源选择 `worktree` 时平台创建 `feature/<agent>/<issue-or-chat>`；通用任务目录位于源码树之外。发布器不迁移任务目录。

## 运行文件与自更新

`.run/runtime/int/` 和 `.run/runtime/prod/` 各有 a、b 两个位置。一个在运行，另一个只用于准备更新或临时恢复；成功后删除旧位置。首次迁移遗留的旧目录由单独授权的历史清理处理。

Go 和 Web 按各自源码输入摘要复用；未知根目录输入变化会触发两组构建。int 使用生产运行模式，prod 同步已验证的程序，不重新编译。复用组件的内置版本仍表示它自己的构建提交；整体部署以部署记录中的提交和摘要为准。

int 中的任务不能同步更新自身。由生产开发 Agent 调用 int，验证后提交生产请求并结束当前运行。外部 systemd 进程等待来源运行完成，暂停生产领取新任务，等待已有运行结束后更新；超时恢复接单，不强杀任务。结果写入请求记录，并追加到原运行的“查看过程”中。请求提交不是交付成功；用户确认任务后再标记完成。

启动或部署检查失败恢复临时旧程序；成功后删除旧副本，后续恢复使用 Git 重新构建。watchdog 只报告健康异常，不再从历史发布包自动回滚。数据库、上传文件和任务工作目录独立于运行目录，程序恢复不会撤销数据库变更。迁移兼容性和必要备份由具体任务处理；显式备份入口为 `pnpm release backup`。

## 清理与排查

生产成功记录持久化后清理旧运行文件和对应任务 worktree。目录必须无运行进程引用、仍指向该次已发布提交、没有未保存成果；非构建类忽略文件也会阻止删除。普通主 checkout、其他任务目录及分支保留。Daemon 已正常回收 worktree 时视为清理完成。

清理失败不回滚生产，记录 `cleanup_error`。重试只清理本次任务成果，不触碰后来发布的恢复目录；任务提交仍在已发布历史中时也可以回收。执行 `pnpm release cleanup <candidate>` 重试。当前缓存按目录限制 1 GiB，超出后在构建结束时删除；Web/API 日志超过 64 MiB 时，在服务重新启动前保留末尾 16 MiB。共享 pnpm/Go 缓存不自动清理，以免影响其他项目。

请求与日志位于 `.run/publish-requests/`，当前候选位于 `.run/candidate.json`。`pnpm release status` 查看记录，`pnpm release verify` 检查真实环境身份，`pnpm release verify-logs --env int|prod` 检查本轮致命日志。恢复入口 `pnpm release restore --env int|prod` 仅在临时旧目录仍存在时有效，必须从被恢复运行环境之外执行。

平台提示来源可通过运行环境根目录的 `runtime-instructions.json` 排查：包含提示指纹、技能名称及父目录规则候选路径，不保存凭据或提示正文，也不证明所有候选文件实际被读取。任务触发消息见 `server/internal/daemon/prompt.go`，平台运行规则见 `server/internal/daemon/execenv/runtime_config_sections.go`。

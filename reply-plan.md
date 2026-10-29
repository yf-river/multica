已把已准备的修复接入本任务分支，并完成代码审查与相关测试。下面是本次方案，按此执行验证、合并和生产发布。

## 一、代码接入与审查

任务分支 `feature/ida-developer/gong-105`，基于 `1cbb3fe35d39e6b6a971dd57690ef5cfce92ba42`（已包含已发布基线 `413dffa94329d5953b509a6cfba9a1070feb5270`）。

1. 以 cherry-pick 接入 `ca23ce947743306c6fb56706a1547cac2d8e330c`（Clear idle verification slots after baseline sync），生成 `dddf56036`，内容 5 个文件、+119/-9，与源提交一致。
2. 逐处核对实际变更：
   - `verification-seed.py` 新增 `clear_idle_baseline()`：只接受外部维护身份（拒绝任务内 `MULTICA_TASK_ID`）；先 `begin_maintenance` 停接单；在候选锁内校验槽位安装身份、实际数据库连接与专属实例一致；停机路径先 `check_stopped_slot()`（无残留进程、目录未被占用、数据库无消费者、三个监听端口未被占用），在途路径 `check_process_ownership` + `check_live_environment` + `drain`；确认无未完成任务后停 Daemon、停 web/server、清上传目录、TRUNCATE 业务表（保留 `schema_migrations`）；最后 `record_slot_seed(..., verified=False)`，下一次领取前才导入种子。
   - `check_stopped_slot()`：槽位进程全部意外退出时，按“进程 → 目录占用 → 数据库连接 → 监听端口”顺序核实后才允许从种子重启，避免误判停机就重置。
   - `restore()`：清理后的槽位走原有归属核对；进程全停的槽位改走 `check_stopped_slot()` 核实，运行中槽位维持原核对与排空。
   - `issue-delivery-worker.py`：`reconcile_environment_baseline()` 用 `completed_slots` 逐槽推进（取代原“种子一致即跳过”），中断在 `baseline_cleaning` 的槽位先补完清理再谈恢复，支持中断续接与重入。
   - `docs/operations/environment-releases.md`：把“空闲槽位更新程序后清空业务数据和上传目录、下次领取前再导入种子”写入流程真源。
3. 我没有发现必须阻断的问题；以下几点在验收中一并盯：清理后的槽位 `record['pids']` 已置空、`cleaned` 阶段重入只核对不重复清库；`cleanup_*` 旧代次键在下一任务 `installed` 时由发布流程重写，不影响本次判断；`check_stopped_slot` 的端口探测覆盖 backend/frontend/health 三个端口。

## 二、验证

1. 单元测试（已跑）：`scripts/verification-seed.test.py` 36 passed / 2 skipped；`scripts/delivery-batch.test.py` 48 passed；另跑 `issue-delivery-worker.test.py` 41、`verification-pool.test.py` 28、`release-environments.test.py` 49 全通过。
2. 真实验收：固定 `int` 集成 + 一个空闲 `verify` 槽位，覆盖——生产基线同步后空闲槽位被清空且种子标记为待导入；槽位进程全部退出时按新核实顺序安全恢复；槽位仍有未完成任务/数据库消费者/端口被占用时保留现场并拒绝重置。
3. 生产发布：int 通过后合入 `personal-studio-upstream` 并推送，由外部发布器应用同一份程序，发布后做真实浏览器验收。

## 三、交付方式

按正式交付接口登记（plan / confirm-plan / submit / accept / approve），固定提交 `dddf56036`、基线 `413dffa94329d5953b509a6cfba9a1070feb5270` 与本任务分支，提交实际检查证据。用户已授权的既定范围内验证、合并和生产发布不再重复请示；出现范围变化或真实阻塞会停下来说明。

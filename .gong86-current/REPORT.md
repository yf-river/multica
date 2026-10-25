本轮 verify-1 实际功能验收未通过。

交付：01a171b7-60b1-788d-99f3-1d47e1c7f7ea，revision 3。
候选：96b302c78548dcf5bdba997fa1bc04972c3a1549-1792909468815044667。
源码：96b302c78548dcf5bdba997fa1bc04972c3a1549。
实际 release_id：96b302c78548dcf5bdba997fa1bc04972c3a1549-1792909417807488520，与候选映射一致。
环境：http://9.134.129.162:33653；verify-1；种子 seed-20261025-agent-control-v4，SHA256 5fcd7c40b1e78cf96ff8290d25718d6a9e463abbcc15c20a25859ba5371b6090。

已完成：

1. 通过正式 recover-owner 接续过期归属，初始化脚本 preflight 通过；核对候选、部署、租约、种子和实际数据库身份。本机没有 psql，使用现有 postgres:17-alpine 镜像执行只读活动任务检查，未创建数据库或重置种子。
2. 受控 0600 凭据按测试工作区注入；正式设置接口绑定测试账号自己的 Codex gpt-6.1-sol、low，开启 investigation 和监督；保留原始设置及服务配置。
3. 初始化脚本错误引用 server/internal/service/builtin_skills/delivery-supervision/SKILL.md，实际固定候选为 skills/delivery-supervision/SKILL.md。未改动候选，通过正式接口从实际目录导入绑定。技能 SHA256 aa7e5011089ff451e065acd4a6b68ab17c2878264c67f052f129f5a1a7b56558。只证明绑定成功，不能证明监督模型实际加载成功。
4. 测试任务 DEV-5 / 01a171bf-385d-742e-8693-22dcabaac3f9，真实执行 01a171bf-3863-72d1-b1d1-ee0d1263d932 完成，回答 17+25=42。Codex session 01a171bf-3a1a-74b1-ae1c-7b07ec3cf0c0。所属 Daemon 形成 local_snapshots=1 的材料 2fcf9aad-64e4-4a96-8930-16f30b576931，runtime=codex，AgentLens not_applicable，外部HTTP请求0；保留 run级用量和可见事件截断 gaps。
5. 浏览器使用种子测试账号登录，访问监督页、刷新和390px视口；API有限边界检查：无认证400、无权限工作区404、不存在证据 deleted=true。这些不能替代完整分页、跨任务权限和恢复验收。

确定失败：手动请求监督 DEV-5 后，批次 01a171bf-c256-7e4c-af89-ace7db645d80，执行 01a171bf-c25b-739a-a93e-e99565ee3a50 于14:29:15进入 failed，原始错误 decode supervision input: invalid character 'C' after top-level value。固定候选 daemon.go:7256 给 prompt 追加 Current runtime paths 文本；7510 又将整个 prompt 当 JSON Unmarshal。真实监督模型调用前失败，未产生有效建议或覆盖结果；不能宣称实际加载技能、分页和效率判定通过。

CodeBuddy 样本 DEV-6 / 01a171bf-91e3-7148-ac4b-59d09115501d 的 Agent 创建请求显式选 CodeBuddy，但正式测试策略自动将保存结果改绑到 Codex gpt-6.1-sol，执行日志亦为 provider=codex。此样本不能计作 CodeBuddy 强关联、逐请求对账或混合runtime证据；本轮未改共享测试模型策略。

尚未通过：真实 CodeBuddy 及混合runtime、监督模型和效率建议、材料连续分页及生命周期恢复矩阵、最终固定 int 组合验收。未用API成功或历史单测冒充业务通过。

清理：正式停止测试监督后，初始化脚本 --cleanup 成功，恢复原服务配置和监督设置（enabled=false），移除短期服务凭据及本地临时来源文件。保留测试任务、技能和证据用于追溯；未删除业务数据，未合主分支或发布生产。

证据文件：setup.json、runs.json、sample-subject.json、codex-evidence.json、codebuddy-agent.json、failure.log、browser-final.log；浏览器原始响应和截图位于相邻 .gong86-acceptance 目录。本报告随任务评论上传。

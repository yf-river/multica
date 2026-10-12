# 身体数据接口与执行入口

- `server/internal/handler/health.go`：本人作用域、私有原件、指标历史和版本恢复。
- `server/internal/handler/health_import.go`：报告任务登记、任务凭据验证、结果校验与入库。
- `server/internal/health/`：指标标准化、逐页文字/OCR 与来源证据。
- `server/cmd/multica/cmd_health.go`：运行时通过当前身份调用健康接口，提交前按 health.Result 校验 JSON 类型；本人可用 `health report apply <id> --revision N` 应用既有提取。
- `server/internal/health/data.go`：Result、Observation 和 PageResult 是结果字段及类型的权威契约。
- `/api/health/overview`：当前指标、有效历史、基础资料及引用身高体重来源的 computed_bmi。
- `server/internal/health/bmi.go`：当前 BMI 的单位校验、计算和来源关联。
- `/api/health/reports/{id}/apply`：本人应用既有提取，保存覆盖前版本，无模型调用。
- `/api/health/reports/{id}/prepare`、`pages`、`images/{page}`：分页读取证据。
- `/api/health/reports/{id}/submit`：一次原子入库，报告轮次和数据代次共同拒绝过期提交。

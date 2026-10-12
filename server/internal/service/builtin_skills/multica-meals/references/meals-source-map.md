# 饮食数据与接口

真源：server/internal/meals/data.go、generate.go；server/internal/handler/meals.go；server/cmd/multica/cmd_meals.go。

接口 GET /api/meals 返回 state、revision、health_revision、task_statuses。饮食记录按本人和工作区隔离；Agent 只能使用本人发起、当前运行中的 task 读取及提交建议。POST /api/meals/actions 的 Agent 写操作仅允许 draft_save；正式确认仅允许本人。

propose 文件是 draft 对象；不要把完整接口响应作为草稿提交：

```json
{
  "week": "2026-10-12",
  "preferences": {"goal":"本人沟通确定的目标", "activity":"本人描述的活动", "restrictions":"本人描述的忌口", "people":1, "prep_time":"约定的时间", "notes":"已有食材和外食餐次"},
  "health_revision": 0,
  "rationale": "按本人确认的偏好安排；明确每份食材用量，未知数量留空。",
  "entries": [{
    "id": "",
    "date": "2026-10-12",
    "slot": "lunch",
    "name": "用户选定的菜品",
    "portions": 1,
    "mode": "home",
    "recipe_id": "读取到的现有菜谱ID，手工餐食留空",
    "batch_id": "",
    "foods": [{"name": "食材原名", "quantity": null, "unit": "", "basis": "", "origin": "assistant", "original": "菜谱食材原文"}],
    "note": "本人建议食用量及分装说明，不能包含医学数值"
  }],
  "prep_steps": "按已确认菜品组织步骤，并引用对应菜谱。"
}
```

slot=breakfast/lunch/dinner/snack；mode=home/out/delivery；portions 为正整数。health_revision 使用本次实际读取值，不能使用模板值。id 为空表示新增餐食，已有餐食编辑沿用 ID。日期须属于草稿 week，week 为周一。实际数量必须来自菜谱明确值、用户填写或清楚标明的助理建议。

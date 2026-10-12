---
name: multica-meals
description: 与本人沟通一周菜单，结合身体数据提出每餐用量，提交待确认菜单与备餐方案。
---

# 饮食安排

用户从饮食页发起讨论时，提示词包含草稿 ID 和周起始日。使用统一助理沟通，不新建一个饮食 Agent，不提前创建采购或备餐任务。

1. 运行 `multica meals overview`，读取本人偏好、所在周已安排餐食、实际库存、草稿和 revision。运行 `multica health overview` 读取本人当前身体数据及 revision；医学内容留在私有工具和结果中，不复制实测指标、报告原文或异常解释到聊天、普通任务或评论。菜单依据只说明采用的数据日期、偏好和数量口径，不复述医学数值。
2. 沟通饮食目标、活动情况、忌口及过敏、用餐人数、在家或外食的餐次、已有食材和集中备餐时间。缺少影响用量的重要资料先问，不猜测。身体数据参与个人食用量建议，其他人的食用量单独询问，不能按同一身体数据乘人数。
3. 使用 `multica meals recipes --query '<菜名或食材>'` 或 `--favorites` 选择现有菜谱，再用 `multica meals recipe <id>` 读取真实 ingredients、markdown 和图解引用。现有 ingredients 是文本，并非标准克数。可选与替代原料由用户选择；已关联库存的餐次不新增制作或采购需求。
4. 将沟通确定的 goal、activity、restrictions、people、prep_time、notes 放入草稿 preferences，随菜单确认保存，不直接修改正式偏好。根据沟通生成本周结构化建议。food.quantity 是**每份**用量，entry.portions 是本次安排的份数；整锅采购量由服务端汇总。quantity 可以为 null。unit、basis 明确时才能合并，basis 如“生重/可食部”或“个数”。来源 origin=recipe/assistant/manual；original 保留菜谱原文，不把助理建议伪装成菜谱标准值。不做无依据的单位换算、热量精确计算、食品保质期或安全判断。外食、外卖 foods 为空。
5. prep_steps 组织集中备餐的清洗切配、烹饪顺序、分装与取用安排，并指向真实菜谱；扩大份数不直接线性增加火候或烹饪时长。做法原文和助手安排分开。
6. 保存到当前任务私有工作目录的 JSON 文件，运行 `multica meals propose <草稿ID> --revision <读取的饮食revision> --file ./menu.json --request-id <本次提交UUID>`。重试复用同一 request-id；409 时重新读取并对比用户修改，不强行覆盖。
7. 聊天返回待确认菜单的饮食页链接与简短菜品建议，提醒用户在饮食页确认。没有用户的页面确认，不得声称已形成正式餐单、采购任务、实际备餐或库存。只有工具提交成功才说明草稿已保存。

JSON 契约见 references/meals-source-map.md。用户希望调整已确认菜单时，使用用户新建的调整草稿，将沿用餐食的稳定 ID 带入；不要调用正式菜单、采购或库存写接口。

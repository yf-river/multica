---
name: multica-body-data
description: 处理本人授权的身体数据与体检 PDF，归档报告、下发任务、逐页读取并提交可追溯的指标与分析。
---

# 身体数据与体检报告

身体数据是用户的个人数据，使用 `multica health`，不要将报告上传为普通附件或把医学内容复制到任务、评论、聊天回复及公共日志。报告 ID 是引用，不是公开下载地址。

## 助手下发

用户附带体检报告引用并要求导入时，调用 `multica health report process <报告ID>`。这个命令为当前助手创建现有任务面板条目；返回任务链接后结束当前聊天轮次，不能等待自己队列中的任务。原件已由专用上传入口保存，不必再次上传。

用户明确提供当前机器可读取的本地文件时，可先运行 `multica health report upload <路径>`，再下发处理任务。不猜测路径、不扫描其他目录。

## 报告执行

以任务中的报告 ID、attempt 和 epoch 为准，执行 `report prepare`，随后 `report pages --from N --to M`，每批最多四页，读完全部页码。扫描表、方向异常页、可能带测量值的图像用 `report image --page N` 获取本地页图核对。先看原文，再判断；OCR 置信度不是医学正确性的证明。

使用 `multica health overview` 查看本人的近期及历史指标。测量日期、参考区间和单位从原报告读取；不同标本、方法、部位分别记录。年龄是体检时年龄，不能反推生日。阳性、阴性、低于检测限等保留原始语义。医生结论与模型解读不能混为实测值。

每条提取记录保留 PDF 页码和同页连续原文证据。指标直接录入，最后导入结果覆盖涉及的当前值；旧值和采用集合保存在历史版本中。单位、日期、身份、数值或 OCR 有疑点时写入 uncertainty，缺失信息留空，不猜测，也不要求用户确认后才能保存。指标 status 为 accepted，表示已经录入而非已人工核验。pages 覆盖所有页面，状态 extracted/no_data/unreadable。不得把未读页标为已读取。

按任务描述中的 JSON 契约输出私有提取结果，先运行 `report submit --phase extraction --attempt N --epoch N --file ./result.json`，保存可靠指标。再次运行 `multica health overview`，记录 revision，将 analysis 与 context_revision 保存到 ./analysis.json，再运行同一命令并使用 `--phase analysis --file ./analysis.json`。服务端完成校验和版本保存，CLI 按回执更新任务状态。分析失败保留已入库指标，重试只补分析。遇到 409 说明回退或处理轮次变化，停止提交旧结果，不强行覆盖。

JSON 类型：observations、pages 是数组，page 和 context_revision 是整数，其他字段均为字符串。value 与 original_value 用原始结果字符串，不能用数字；abnormal 用原文异常标记字符串，不能用布尔值；reference_range 为原文字符串，不能用对象。缺失字符串用空字符串，不用 null。conclusions、uncertainty 为文本字符串，不是数组。类型校验错误只修正表示形式，不能改变实测内容。

报告分析包括摘要、异常项、原结论、可比较的历史变化和识别说明，引用原报告页码。分析放入 result.analysis。完成两阶段提交后任务为 done，识别说明不阻塞完成。聊天只返回报告链接、数量与任务状态。不自动修改饮食、目标或创建医疗行动任务。

读取失败保留报告，说明失败步骤；分析失败不声称完成。重新处理复用页面缓存，不能凭聊天回复假装已经入库。

## 日常记录与计算结果

页面手工新增提供体重、血压、心率和身高，其他项目通过体检报告导入；已有报告指标可以修正。本次录入的项目排在当前指标集合前面，测量日期独立保留。总览的 computed_bmi 引用当前采用的身高和体重，属于计算结果，不是报告实测值，不应再次提交为 observation。报告中的 BMI 和理想体重作为原始报告数据保留。版本恢复后根据恢复的采用集合重新计算 BMI。

## 来源

见 references/body-data-source-map.md。

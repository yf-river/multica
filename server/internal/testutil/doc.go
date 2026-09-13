// Package testutil 保存服务端 DB 类测试搭建场景所需的夹具：插入一行夹具数据、测试
// 结束再删掉它的行构造器，以及跑一个 handler 并对其输出做断言的 HTTP 辅助。
//
// 它存在的原因：共享行为改动的成本曾随测试文件数量线性增长。在本包出现之前，仅
// internal/handler 就有约 1000 段手写 `INSERT INTO ... RETURNING id`、约 1150 段
// `DELETE FROM` 清理、约 1200 份 recorder/call/status/decode 四件套拷贝，每一份都
// 是对同一契约的独立陈述。改一次 insert 的列集合或失败消息的形状，就意味着改每一份拷贝。
//
// 两条规则让情况不再恶化：
//
//   - 新的 DB 测试通过 Fixture 建行、通过 Call 驱动 handler。不要在新测试里手写
//     INSERT + t.Cleanup 对，或 recorder/status/decode 四件套。
//   - 这里的辅助保持"无行为"：它们插行、跑 handler、报告不一致，绝不在测试的
//     名义下断言产品规则。一个"知道正确响应长什么样"的辅助，等于把断言从本该做
//     断言的测试里搬走了。
//
// 本包 import "testing"，只能被 _test 文件引用。从生产代码 import 它会把 testing
// flag 集链接进二进制。
package testutil

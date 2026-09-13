// Package featureflag 是 multica 后端的框架级功能开关（feature flag）库。
//
// 它实现了 Martin Fowler 描述的规范分层——Toggle Point / Toggle Router /
// Toggle Configuration：
//
//	业务代码 -> Service.IsEnabled(ctx, key, default)    // Toggle Point
//	            Service                                  // Toggle Router
//	            Provider (Static/Env/Chain/custom)       // Toggle Configuration
//
// 设计目标：
//
//   - 业务代码从不直接跟 provider 打交道，总是问 Service。这让"决策点"与"决策逻辑"
//     解耦，同一处 Toggle Point 今天可以由 YAML 文件支撑、明天由数据库、之后再换成
//     A/B 路由器，调用方零改动。
//   - 常开安全：缺失 provider、缺失 key 或规则配错都不能让调用方崩溃。所有公开入口
//     在这种情况下返回调用方给的默认值，并记录 Reason 使失败可观测。
//   - 确定性百分比发布：同一 (key, identifier) 对永远落入同一桶，用户不会在多次请求
//     间反复进出某个实验。
//
// 装配：
//
// 在 multica server 内部构造 Service 的标准方式是 featureflag.NewServiceFromEnv：
// 它读取 MULTICA_FEATURE_FLAGS_FILE 中的 YAML 规则集，并在其上叠加 EnvProvider，
// 让单个开关可通过 FF_<KEY> 环境变量在运行时被覆盖。核心类型只依赖标准库；
// YAML 加载器引入的 gopkg.in/yaml.v3 已是 server 级依赖。
//
// 公开的 Service API 见 server/pkg/featureflag/service.go，端到端用法见包测试。
package featureflag

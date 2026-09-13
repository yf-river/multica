package featureflag

// 本文件职责：确定性百分比分桶的实现——把 (开关 key, 标识符) 用 FNV-1a 哈希
// 成 [0, 100) 的桶号，供百分比发布规则判断「这个用户这次算不算进实验组」。

import "hash/fnv"

// bucketFor 用 FNV-1a 为 (key, identifier) 对算出一个 [0, 100) 里的确定性
// 桶号。同一对参数永远得到同一个桶——这正是稳定百分比发布所依赖的契约：
// 一个用户不能在不同请求间反复进出某个实验。
//
// 选 FNV-1a 而不是加密哈希，是因为它快、零依赖，对「100 以内的分桶」分布
// 也足够均匀。这个哈希不涉及安全，别拿它做除分桶外的任何用途。
func bucketFor(key, identifier string) int {
	h := fnv.New32a()
	// 两个组成部分之间写一个分隔符，避免 "key||identifier" 式的撞车——
	// 否则 ("ab", "c") 与 ("a", "bc") 会哈希成同一个值。
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(identifier))
	return int(h.Sum32() % 100)
}

// inPercent 判断 (key, identifier) 是否落进前 percent 个桶。
// percent 为 0 时对所有人关闭该规则；100 时对所有人开启。
// [0, 100] 之外的值会被钳制处理。
func inPercent(key, identifier string, percent int) bool {
	switch {
	case percent <= 0:
		return false
	case percent >= 100:
		return true
	}
	return bucketFor(key, identifier) < percent
}

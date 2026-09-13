package publicapiv1

// 本文件职责：承载并暴露 canonical 的 v1 契约文档（openapi.yaml）。
// 它被 go:embed 编译进二进制，运行时通过 OpenAPI() 取出一份隔离拷贝。

import (
	"bytes"
	_ "embed"
)

// openAPISpec 是随本包一起编译进二进制的 v1 OpenAPI 契约文档内容。
// 测试用它与 Operation 台账互相钉死，防止路由与文档漂移。
//
//go:embed openapi.yaml
var openAPISpec []byte

// OpenAPI 返回 canonical v1 契约文档的一份隔离拷贝。
func OpenAPI() []byte {
	return bytes.Clone(openAPISpec)
}

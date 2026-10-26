# Daemon 内置 DSH bridge

`bundle/` 是从 `packages/dsh-runtime` 生成的交付制品，供 Go embed 随独立 daemon 分发。
修改 bridge 后运行 `pnpm --filter @multica-ai/dsh-runtime bundle:daemon`，将生成的 tarball
和校验清单一起提交；CI 重建并核对字节。不要手工修改制品，不放入凭据或 session。

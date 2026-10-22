#!/usr/bin/env bash
# 构建版本串的唯一来源：`make build` / `make multica` 与固定发布入口
# （scripts/release-environments.py）都从这里取，保证本地自建和固定环境构建
# 上报同一个形态。
#
# 为什么不能省这一步：daemon 上报的 CLI 版本串是它与前端/服务端之间的能力信号
# （交接说明 0.3.28、快速创建 0.2.21/0.4.3、聊天项目上下文 0.4.10 等门槛都读它）。
# 两个解析器只认 `vX.Y.Z` 与 git-describe 的 `vX.Y.Z-N-g<sha>`：裸提交哈希会被判成
# "解析不了"，于是新构建的运行时被当成旧版本，交接说明输入框被置灰、快速创建被 422
# 拒绝。所以这里宁可合成一个 `v<apps/web 版本>-0-g<sha>`，也不用裸 sha。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# 有 tag 时直接用 git describe 的形态（正式发布是裸 semver，tag 之后又提交过则是
# vX.Y.Z-N-gSHA，可能带 -dirty）。
description="$(git -C "$root" describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || true)"
if printf '%s' "$description" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+'; then
	printf '%s\n' "$description"
	exit 0
fi

# 没有 tag 的回退：用 apps/web 的版本号补出主版本段，并标出"超出一个 tag 都没打到"。
base="$(node -e 'console.log(require(process.argv[1]).version)' "$root/apps/web/package.json" 2>/dev/null || printf '0.0.0')"
dirty=""
git -C "$root" diff --quiet --ignore-submodules HEAD -- 2>/dev/null || dirty="-dirty"
printf 'v%s-0-g%s%s\n' "$base" "$(git -C "$root" rev-parse --short HEAD 2>/dev/null || printf 'unknown')" "$dirty"

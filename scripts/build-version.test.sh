#!/usr/bin/env bash
# scripts/build-version.sh 的回归测试。
#
# 这个脚本的输出不是给人看的：daemon 把它当 cli_version 上报，前端/服务端拿它和
# 交接说明、快速创建、聊天项目上下文的版本门槛比较。两个解析器只认 `vX.Y.Z` 和
# git-describe 的 `vX.Y.Z-N-g<sha>`，裸提交哈希会被判成"解析不了"。所以这里锁住的
# 是**形态**：每个用例都要求输出能被这两个解析器读出来，而不只是"看起来像版本号"。
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fail() {
	echo "$1" >&2
	exit 1
}

# 解析器接受的最小形态：裸 semver，或 `vX.Y.Z-N-g<sha>`（可带 -dirty）。
PARSABLE='^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9]+-g[0-9a-fA-F]+)?(-dirty)?$'

new_repo() {
	local dir="$TMP/$1"
	mkdir -p "$dir/apps/web" "$dir/scripts"
	printf '{"name":"web","version":"1.2.3"}\n' >"$dir/apps/web/package.json"
	cp "$SRC_DIR/scripts/build-version.sh" "$dir/scripts/build-version.sh"
	git -C "$dir" init -q
	git -C "$dir" config user.email test@example.com
	git -C "$dir" config user.name test
	printf 'base\n' >"$dir/file.txt"
	git -C "$dir" add -A
	git -C "$dir" commit -qm base
	printf '%s' "$dir"
}

require_shape() {
	local label=$1 version=$2
	[[ $version =~ $PARSABLE ]] ||
		fail "$label: 版本串无法被版本门槛解析: '$version'"
}

# 仓库里没有 tag（这个项目 2026-10 起的控制仓库就是这种状态）：必须合成出主版本段。
repo="$(new_repo untagged)"
out="$(bash "$repo/scripts/build-version.sh")"
short="$(git -C "$repo" rev-parse --short HEAD)"
[ "$out" = "v1.2.3-0-g$short" ] ||
	fail "无 tag: 期望 'v1.2.3-0-g$short'，实际 '$out'"
require_shape "无 tag" "$out"

# 打了 tag 且 HEAD 就是 tag：直接用裸 semver。
repo="$(new_repo tagged)"
git -C "$repo" tag v1.2.3
out="$(bash "$repo/scripts/build-version.sh")"
[ "$out" = "v1.2.3" ] || fail "tag 处: 期望 'v1.2.3'，实际 '$out'"
require_shape "tag 处" "$out"

# tag 之后又提交过：git-describe 形态，解析器按 dev 构建放行。
repo="$(new_repo ahead)"
git -C "$repo" tag v1.2.3
printf 'more\n' >>"$repo/file.txt"
git -C "$repo" commit -qam more
short="$(git -C "$repo" rev-parse --short HEAD)"
out="$(bash "$repo/scripts/build-version.sh")"
[ "$out" = "v1.2.3-1-g$short" ] ||
	fail "tag 之后有提交: 期望 'v1.2.3-1-g$short'，实际 '$out'"
require_shape "tag 之后有提交" "$out"

# 工作区有未提交改动：带 -dirty，形态仍然可解析（本地自建常处于这个状态）。
repo="$(new_repo dirty)"
printf 'changed\n' >>"$repo/file.txt"
out="$(bash "$repo/scripts/build-version.sh")"
[[ $out == *-dirty ]] || fail "脏工作区: 期望以 -dirty 结尾，实际 '$out'"
require_shape "脏工作区" "$out"

echo "✓ build-version.sh 在无 tag / tag / tag 之后 / 脏工作区下都输出可解析的版本串"

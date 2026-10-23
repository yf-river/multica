这是 diff 采集功能的验收探针任务，请严格按下面步骤执行，不要做其他改动。

1. 在当前工作目录依次执行：

```bash
mkdir -p probe/sub
printf 'hello a\nhello b\n' > probe/a.txt
printf 'nested\n' > probe/sub/untracked.txt
printf 'probe/ignored.txt\n' > .probe-ignore
printf 'ignored-content\n' > probe/ignored.txt
printf 'SECRET_KEY=sk-abcdefghijklmnopqrstuvwxyz0123456789\nDB_PASSWORD=hunter2\n' > .env
head -c 3000000 /dev/zero | tr '\0' 'x' > probe/big.txt
chmod 000 probe/sub 2>/dev/null || true
```

2. 执行完成后，用一句话回复“探针文件已生成”，不要提交、不要清理任何文件。

注意：最后一步把 probe/sub 设为不可读是故意的，用来验证采集侧对读不到路径的处理，不要恢复它的权限。

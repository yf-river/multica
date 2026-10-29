//go:build windows

package openviking

// Windows 的目录句柄不支持 os.File.Sync；文件本体在原子替换前已 FlushFileBuffers。
func syncDir(string) error { return nil }

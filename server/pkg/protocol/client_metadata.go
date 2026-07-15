package protocol

func NormalizeGOOS(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows", "linux":
		return goos
	default:
		return goos
	}
}

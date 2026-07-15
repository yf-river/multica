package protocol

func NormalizeGOOS(goos string) string {
	if goos == "darwin" {
		return "macos"
	}
	return goos
}

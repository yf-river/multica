package agent

import "testing"

func TestHandoffSupported(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"at minimum", MinHandoffCLIVersion, true},
		{"above minimum", "0.4.0", true},
		{"below minimum", "0.3.26", false},
		{"far below", "0.2.21", false},
		{"empty (unreported)", "", false},
		{"unparsable", "garbage", false},
		{"dev git-describe build", "v0.3.0-5-gabc1234", true},
		// 固定环境的源码构建在仓库没有 tag 时合成 `v<apps/web 版本>-0-g<sha>`
		// （scripts/build-version.sh）——这是 daemon 实际上报的形态，必须放行。
		{"source build without tags", "v0.4.37-0-g9283f784db88", true},
		{"bare commit hash (not a version)", "9283f784db88", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HandoffSupported(tc.version); got != tc.want {
				t.Fatalf("HandoffSupported(%q) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}

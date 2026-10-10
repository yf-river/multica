package main

import (
	"strings"
	"testing"
)

func TestHealthResultJSONTypes(t *testing.T) {
	if err := validateHealthResultJSON([]byte(`{"observations":[{"value":"1.2","abnormal":"偏高"}],"conclusions":"原报告结论","uncertainty":"","pages":[{"page":1,"status":"extracted","note":""}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"observations":[{"value":1.2}]}`,
		`{"observations":[{"abnormal":true}]}`,
		`{"conclusions":["私有医学内容"]}`,
		`{"uncertainty":[]}`,
	} {
		err := validateHealthResultJSON([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "字符串") || strings.Contains(err.Error(), "私有医学内容") {
			t.Fatalf("类型错误须提供格式提示且不能回显医学内容：%v", err)
		}
	}
}

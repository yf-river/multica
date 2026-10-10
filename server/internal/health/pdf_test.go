package health

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 真实报告只通过显式路径启用，默认测试不读取用户文件。
func TestPrepareRealReport(t *testing.T) {
	path := os.Getenv("MULTICA_HEALTH_REPORT_FIXTURE")
	if path == "" {
		t.Skip("需要显式提供真实报告路径")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	count, err := PDFInfo(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "pages")
	p, err := Prepare(ctx, path, dir, count)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Pages) != count {
		t.Fatalf("page coverage %d/%d", len(p.Pages), count)
	}
	texts, ocr := 0, 0
	for i, page := range p.Pages {
		if page.Page != i+1 {
			t.Fatal("page order lost")
		}
		texts += len([]rune(page.Text))
		ocr += len([]rune(page.OCR))
	}
	if count == 21 && (p.Pages[18].OCR == "" || p.Pages[19].OCR == "") {
		t.Fatal("scanned lab or ECG page missing")
	}
	second, err := Prepare(ctx, path, dir, count)
	if err != nil || len(second.Pages) != count {
		t.Fatal("preparation cache failed", err)
	}
	t.Logf("PASS pages=%d text_chars=%d ocr_chars=%d cached=true", count, texts, ocr)
}

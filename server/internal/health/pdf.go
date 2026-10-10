package health

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const PreparationVersion = "poppler-tesseract-v1"

type Word struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	Left       int     `json:"left"`
	Top        int     `json:"top"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
}
type PDFPage struct {
	Page        int    `json:"page"`
	Text        string `json:"text"`
	OCR         string `json:"ocr"`
	Words       []Word `json:"words"`
	NeedsReview bool   `json:"needs_review"`
	Rotation    int    `json:"rotation"`
	HasImage    bool   `json:"has_image"`
	Layout      string `json:"layout"`
}
type Preparation struct {
	Version string    `json:"version"`
	Pages   []PDFPage `json:"pages"`
}

var prepareLocks sync.Map

// runPDFTool 限制命令执行时间和输出大小，错误中不返回报告内容。
func runPDFTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, name, args...)
	out := &limitedOutput{limit: 32 << 20}
	cmd.Stdout = out
	cmd.Stderr = &limitedOutput{limit: 4096}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s 执行失败，请检查 PDF 或读取工具", name)
	}
	return out.Bytes(), nil
}

type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("工具输出超过限制")
	}
	return b.Buffer.Write(p)
}

func PDFInfo(ctx context.Context, path string) (int, error) {
	out, err := runPDFTool(ctx, "pdfinfo", path)
	if err != nil {
		return 0, err
	}
	pages := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Encrypted:") && strings.Contains(line, "yes") {
			return 0, fmt.Errorf("请先解密 PDF 后再上传")
		}
		if strings.HasPrefix(line, "Pages:") {
			pages, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
		}
	}
	if pages < 1 {
		return 0, fmt.Errorf("PDF 没有可读取的页面")
	}
	return pages, nil
}

func Prepare(ctx context.Context, pdf, dir string, pageCount int) (Preparation, error) {
	lock, _ := prepareLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	index := filepath.Join(dir, "index.json")
	if data, err := os.ReadFile(index); err == nil {
		var p Preparation
		if json.Unmarshal(data, &p) == nil && p.Version == PreparationVersion && len(p.Pages) == pageCount {
			return p, nil
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Preparation{}, err
	}
	for _, tool := range []string{"pdftotext", "pdftoppm", "tesseract"} {
		if _, err := exec.LookPath(tool); err != nil {
			return Preparation{}, fmt.Errorf("缺少报告读取工具 %s", tool)
		}
	}
	out, err := runPDFTool(ctx, "pdftotext", "-layout", "-enc", "UTF-8", pdf, "-")
	if err != nil {
		return Preparation{}, err
	}
	texts := strings.Split(string(out), "\f")
	images := imagePages(ctx, pdf)
	result := Preparation{Version: PreparationVersion, Pages: []PDFPage{}}
	for i := 1; i <= pageCount; i++ {
		if err := ctx.Err(); err != nil {
			return Preparation{}, err
		}
		cached := filepath.Join(dir, fmt.Sprintf("page-%d.json", i))
		var p PDFPage
		if data, e := os.ReadFile(cached); e == nil && json.Unmarshal(data, &p) == nil && p.Page == i {
			result.Pages = append(result.Pages, p)
			continue
		}
		text := ""
		if i <= len(texts) {
			text = strings.TrimSpace(texts[i-1])
		}
		p = PDFPage{Page: i, Text: text, HasImage: images[i], Words: []Word{}}
		// 坐标文字用于从数值回到表格所在区域；原始 XML 不包含文件路径。
		if layout, e := runPDFTool(ctx, "pdftotext", "-f", strconv.Itoa(i), "-l", strconv.Itoa(i), "-bbox-layout", pdf, "-"); e == nil {
			p.Layout = string(layout)
		}
		if len([]rune(text)) < 200 || images[i] {
			img := filepath.Join(dir, fmt.Sprintf("page-%d", i))
			if _, e := runPDFTool(ctx, "pdftoppm", "-f", strconv.Itoa(i), "-l", strconv.Itoa(i), "-singlefile", "-r", "200", "-scale-to", "2400", "-png", pdf, img); e != nil {
				return Preparation{}, e
			}
			raw, words, e := ocr(ctx, img+".png")
			if e != nil {
				return Preparation{}, e
			}
			// 扫描页方向识别失败时比较四个方向的文字识别质量，保留采用的方向。
			if len([]rune(text)) < 200 {
				best := ocrScore(words)
				for _, rotation := range []int{90, 180, 270} {
					path := img + fmt.Sprintf("-rot%d.png", rotation)
					if e := rotateImage(img+".png", path, rotation); e != nil {
						return Preparation{}, e
					}
					candidate, cw, e := ocr(ctx, path)
					if e != nil {
						return Preparation{}, e
					}
					if score := ocrScore(cw); score > best {
						best = score
						raw = candidate
						words = cw
						p.Rotation = rotation
					}
				}
			}
			p.OCR = raw
			p.Words = words
			p.NeedsReview = strings.TrimSpace(raw) == ""
		}
		if err := writePrivateJSON(cached, p); err != nil {
			return Preparation{}, err
		}
		result.Pages = append(result.Pages, p)
	}
	if err := writePrivateJSON(index, result); err != nil {
		return Preparation{}, err
	}
	return result, nil
}

func imagePages(ctx context.Context, pdf string) map[int]bool {
	result := map[int]bool{}
	data, err := runPDFTool(ctx, "pdfimages", "-list", pdf)
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 14 || f[2] != "image" {
			continue
		}
		page, _ := strconv.Atoi(f[0])
		w, _ := strconv.ParseFloat(f[3], 64)
		h, _ := strconv.ParseFloat(f[4], 64)
		x, _ := strconv.ParseFloat(f[12], 64)
		y, _ := strconv.ParseFloat(f[13], 64)
		if x > 0 && y > 0 && w*h/(x*y) > 25 {
			result[page] = true
		}
	}
	return result
}
func ocr(ctx context.Context, path string) (string, []Word, error) {
	data, err := runPDFTool(ctx, "tesseract", path, "stdout", "-l", "chi_sim+eng", "--psm", "3", "tsv")
	if err != nil {
		return "", nil, err
	}
	words := []Word{}
	var text strings.Builder
	lastLine := ""
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.SplitN(line, "\t", 12)
		if len(f) != 12 || f[0] != "5" || strings.TrimSpace(f[11]) == "" {
			continue
		}
		key := strings.Join(f[1:5], "/")
		if key != lastLine {
			text.WriteByte('\n')
			lastLine = key
		} else {
			text.WriteByte(' ')
		}
		text.WriteString(f[11])
		conf, _ := strconv.ParseFloat(f[10], 64)
		left, _ := strconv.Atoi(f[6])
		top, _ := strconv.Atoi(f[7])
		width, _ := strconv.Atoi(f[8])
		height, _ := strconv.Atoi(f[9])
		words = append(words, Word{f[11], conf, left, top, width, height})
	}
	return strings.TrimSpace(text.String()), words, nil
}
func ocrScore(words []Word) float64 {
	score := 0.
	for _, w := range words {
		if w.Confidence < 65 {
			continue
		}
		letters := 0
		for _, r := range w.Text {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				letters++
			}
		}
		score += float64(letters) * w.Confidence
	}
	return score
}
func rotateImage(src, dest string, angle int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	im, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	b := im.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w, h
	if angle != 180 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch angle {
			case 90:
				out.Set(h-1-y, x, im.At(x+b.Min.X, y+b.Min.Y))
			case 180:
				out.Set(w-1-x, h-1-y, im.At(x+b.Min.X, y+b.Min.Y))
			case 270:
				out.Set(y, w-1-x, im.At(x+b.Min.X, y+b.Min.Y))
			}
		}
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, out)
}
func writePrivateJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func EvidenceMatches(page PDFPage, o Observation) bool {
	clean := func(s string) string { return strings.Join(strings.Fields(s), "") }
	evidence := clean(o.Evidence)
	if evidence == "" || !valueOccurs(evidence, clean(o.OriginalValue)) || (o.OriginalName != "" && !strings.Contains(evidence, clean(o.OriginalName))) {
		return false
	}
	return strings.Contains(clean(page.Text), evidence) || strings.Contains(clean(page.OCR), evidence)
}

// OCRValueReliable 只判断结果字符的识别质量，不能用整页平均分掩盖模糊数值。
func OCRValueReliable(page PDFPage, value string) bool {
	for _, w := range page.Words {
		if valueOccurs(w.Text, value) && w.Confidence >= 90 {
			return true
		}
	}
	return false
}

// 让坐标 XML 在测试中可检查，而不依赖外部 PDF 工具。
func LayoutValid(raw string) bool { return xml.Unmarshal([]byte(raw), new(any)) == nil }

func valueOccurs(text, value string) bool {
	if number.MatchString(value) {
		pattern := regexp.MustCompile(`(^|[^0-9.])` + regexp.QuoteMeta(value) + `($|[^0-9.])`)
		return pattern.MatchString(text)
	}
	return strings.Contains(text, value)
}

// 本文件实现落地页「联系销售（Contact Sales）」表单的公开提交端点（POST /api/contact-sales）：
// 校验公司邮箱、公司规模/用途白名单、各自由文本长度，做「同邮箱每小时限次 + 路由层按 IP
// 限流」两道防垃圾措施后落库并上报埋点。文件末尾是若干入参清洗辅助函数。
package handler

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 落地页「Contact Sales」表单的公开、无认证端点。垃圾提交靠三道防线缓解：
//   - 公司邮箱校验（handler 层挡掉免费邮箱服务商）。
//   - 每邮箱每小时限次，让一次合法提交不能被同地址重放成洪泛。
//   - 路由层按 IP 限流（RATE_LIMIT_CONTACT_SALES）。
//
// 自由文本字段在进库前都限长，请求体本身也设上限——攻击者没法把 MB 级垃圾 POST 进
// 无 JSONB 保护的 TEXT 列。
const (
	contactSalesMaxFirstName   = 80
	contactSalesMaxLastName    = 80
	contactSalesMaxEmail       = 254
	contactSalesMaxCompanyName = 200
	contactSalesMaxGoals       = 2000
	contactSalesHourlyEmailCap = 3
	contactSalesBodyLimit      = 16 * 1024
)

// contactSalesAllowedCompanySize 是前端下拉框会提交的闭值枚举。保持与
// `apps/web/features/landing/i18n/types.ts` 同步。
var contactSalesAllowedCompanySize = []string{
	"1-10",
	"11-50",
	"51-200",
	"201-500",
	"501-1000",
	"1000+",
}

// contactSalesAllowedUseCase 镜像「你打算怎么用/怎么和 Multica 协作？」下拉框的取值。
var contactSalesAllowedUseCase = []string{
	"evaluate",
	"adopt_team",
	"self_host",
	"integrate",
	"partner",
	"other",
}

// freeEmailDomains 是我们前置拒绝的个人邮箱域名。前端会展示同样文案的警告，但服务端
// 强制才是权威——手搓的 curl 请求不该能绕过它。
var freeEmailDomains = map[string]struct{}{
	"gmail.com":      {},
	"googlemail.com": {},
	"outlook.com":    {},
	"hotmail.com":    {},
	"live.com":       {},
	"msn.com":        {},
	"yahoo.com":      {},
	"yahoo.co.uk":    {},
	"yahoo.co.jp":    {},
	"ymail.com":      {},
	"icloud.com":     {},
	"me.com":         {},
	"mac.com":        {},
	"aol.com":        {},
	"protonmail.com": {},
	"proton.me":      {},
	"pm.me":          {},
	"gmx.com":        {},
	"gmx.de":         {},
	"mail.com":       {},
	"zoho.com":       {},
	"yandex.com":     {},
	"yandex.ru":      {},
	"qq.com":         {},
	"163.com":        {},
	"126.com":        {},
	"sina.com":       {},
	"foxmail.com":    {},
}

// CreateContactSalesRequest 是「联系销售」表单提交的请求体。
type CreateContactSalesRequest struct {
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	BusinessEmail string `json:"business_email"`
	CompanyName   string `json:"company_name"`
	CompanySize   string `json:"company_size"`
	CountryRegion string `json:"country_region"`
	UseCase       string `json:"use_case"`
	Goals         string `json:"goals"`
	// Source 标识表单是从哪里打开的。前端枚举 {page, onboarding, agents_page}；埋点标签
	// `multica_contact_sales_submitted_total{source=...}` 经 metrics.NormalizeContactSalesSource
	// 白名单读取，其余一律归为 "other"。空值退回 "page"，保证不发该字段的老客户端不会让
	// 这条指标凭空消失。
	Source          string `json:"source"`
	ConsentOutreach bool   `json:"consent_outreach"`
	ConsentUpdates  bool   `json:"consent_updates"`
}

// ContactSalesResponse 是提交成功后的回执：库里新行的 id 与创建时间。
type ContactSalesResponse struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

// CreateContactSales 是公开的 POST /api/contact-sales 端点。
func (h *Handler) CreateContactSales(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, contactSalesBodyLimit)
	var req CreateContactSalesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	firstName, ok := requireTrimmedField(w, req.FirstName, "first_name", contactSalesMaxFirstName)
	if !ok {
		return
	}
	lastName, ok := requireTrimmedField(w, req.LastName, "last_name", contactSalesMaxLastName)
	if !ok {
		return
	}
	companyName, ok := requireTrimmedField(w, req.CompanyName, "company_name", contactSalesMaxCompanyName)
	if !ok {
		return
	}

	email, ok := canonicalBusinessEmail(req.BusinessEmail)
	if !ok {
		writeError(w, http.StatusBadRequest, "business_email is invalid")
		return
	}
	if len(email) > contactSalesMaxEmail {
		writeError(w, http.StatusBadRequest, "business_email is too long")
		return
	}
	if !isBusinessEmailDomain(email) {
		writeError(w, http.StatusBadRequest, "please use a business email address")
		return
	}

	companySize := strings.TrimSpace(req.CompanySize)
	if !slices.Contains(contactSalesAllowedCompanySize, companySize) {
		writeError(w, http.StatusBadRequest, "company_size is invalid")
		return
	}

	countryRegion := strings.TrimSpace(req.CountryRegion)
	if countryRegion == "" {
		writeError(w, http.StatusBadRequest, "country_region is required")
		return
	}
	// 给下拉框值一个宽松的长度上限——前端提交什么我们都收（国家列表很大），但封顶以免
	// 攻击者灌爆该列。
	if len(countryRegion) > 80 {
		writeError(w, http.StatusBadRequest, "country_region is too long")
		return
	}

	useCase := strings.TrimSpace(req.UseCase)
	if !slices.Contains(contactSalesAllowedUseCase, useCase) {
		writeError(w, http.StatusBadRequest, "use_case is invalid")
		return
	}

	goals := strings.TrimSpace(req.Goals)
	if len(goals) > contactSalesMaxGoals {
		writeError(w, http.StatusBadRequest, "goals is too long")
		return
	}

	// 每邮箱每小时限次：防止同一公司邮箱在通过一次校验后被当成重复提交通道。基于数据库，
	// 跨副本有效、重启不丢。
	count, err := h.Queries.CountRecentContactSalesByEmail(r.Context(), email)
	if err != nil {
		slog.Warn("count recent contact sales failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to submit inquiry")
		return
	}
	if count >= contactSalesHourlyEmailCap {
		writeError(w, http.StatusTooManyRequests, "too many recent inquiries from this email")
		return
	}

	inquiry, err := h.Queries.CreateContactSalesInquiry(r.Context(), db.CreateContactSalesInquiryParams{
		FirstName:       firstName,
		LastName:        lastName,
		BusinessEmail:   email,
		CompanyName:     companyName,
		CompanySize:     companySize,
		CountryRegion:   countryRegion,
		UseCase:         useCase,
		Goals:           goals,
		ConsentOutreach: req.ConsentOutreach,
		ConsentUpdates:  req.ConsentUpdates,
		UserAgent:       truncateString(r.UserAgent(), 512),
		SubmitterIp:     submitterIP(r),
	})
	if err != nil {
		slog.Warn("create contact sales failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to submit inquiry")
		return
	}

	inquiryID := uuidToString(inquiry.ID)
	slog.Info("contact sales submitted",
		append(logger.RequestAttrs(r),
			"inquiry_id", inquiryID,
			"company_size", companySize,
			"country_region", countryRegion,
			"use_case", useCase,
		)...)

	formSource := strings.TrimSpace(req.Source)
	if formSource == "" {
		formSource = "page"
	}

	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.ContactSalesSubmitted(
		inquiryID,
		companySize,
		countryRegion,
		useCase,
		formSource,
		goals != "",
	))

	writeJSON(w, http.StatusCreated, ContactSalesResponse{
		ID:        inquiryID,
		CreatedAt: timestampToString(inquiry.CreatedAt),
	})
}

// requireTrimmedField 去首尾空白后校验必填与长度上限；不合法时写好 400 并返回 ok=false。
func requireTrimmedField(w http.ResponseWriter, raw, field string, max int) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		writeError(w, http.StatusBadRequest, field+" is required")
		return "", false
	}
	if len(v) > max {
		writeError(w, http.StatusBadRequest, field+" is too long")
		return "", false
	}
	return v, true
}

// truncateString 把超长字符串截到 max 字节（用于 User-Agent 等不重要的审计字段）。
func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// canonicalBusinessEmail 用 net/mail 解析用户原始输入，返回规范的「local@domain」形态
// （小写、无显示名、无首尾空白）。这是 isBusinessEmailDomain 唯一安全的入参——直接检查原始
// 字符串会让 `Ada <ada@gmail.com>` 溜过免费邮箱黑名单：按 RFC 5322 解析后域名是 gmail.com，
// 而原始串里 "@" 之后是 `gmail.com>`。
func canonicalBusinessEmail(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", false
	}
	email := strings.ToLower(strings.TrimSpace(addr.Address))
	// mail.ParseAddress 也接受 `<ada@example.com>` 这类带尖括号与注释的写法；addr.Address
	// 会剥掉两者。这里要求规范的 local@domain 形态且 @ 两侧都非空。
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return "", false
	}
	return email, true
}

// isBusinessEmailDomain 判断邮箱的域名是否在免费邮箱黑名单里。
func isBusinessEmailDomain(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	if _, blocked := freeEmailDomains[domain]; blocked {
		return false
	}
	return true
}

// submitterIP 抓取原始连接 IP 以便事后追溯滥用。这里刻意忽略 X-Forwarded-For——路由层的
// 限流中间件已经核过可信代理头；审计记录要的是真实 TCP 对端，这在「未强制剥 XFF」的部署上
// 是更保守的信号。
func submitterIP(r *http.Request) *netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	return &addr
}

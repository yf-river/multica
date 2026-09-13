// email.go —— EmailService：站内邮件发送（邀请邮件）。
// 支持两条通道：Resend HTTP API（配 RESEND_API_KEY）或直接 SMTP 中继（配
// SMTP_HOST 等），并兼容多种中继差异（STARTTLS/隐式 TLS、PLAIN/LOGIN 认证、
// 8BITMIME 探测回退 quoted-printable、EHLO 名称等）。含发送逻辑与标题净化逻辑。
package service

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/resend/resend-go/v2"
)

// maxSubjectFieldRunes 限制能进邮件 Subject 的用户可控文本（工作区名、邀请人名字）
// 的长度，防止攻击者把一整段钓鱼话术塞进工作区名、再借我们的域名发出去。
const maxSubjectFieldRunes = 60

// EmailService 封装两种邮件后端的选择与各自参数。根据环境变量决定走 SMTP 还是
// Resend；客户端为 nil 且没有 SMTP 时（开发环境）只打印不发。
type EmailService struct {
	client          *resend.Client
	fromEmail       string
	smtpHost        string
	smtpPort        string
	smtpUsername    string
	smtpPassword    string
	smtpTLSInsecure bool
	smtpTLSImplicit bool
	smtpEHLOName    string
}

// smtpAuthClient 是 net/smtp.Client 上“做认证 + 查扩展”的最小接口，用于测试替身。
type smtpAuthClient interface {
	Auth(smtp.Auth) error
	Extension(string) (bool, string)
}

// smtpClientAdapter 把 *smtp.Client 适配成 smtpAuthClient。
type smtpClientAdapter struct {
	client *smtp.Client
}

func (a smtpClientAdapter) Auth(auth smtp.Auth) error {
	return a.client.Auth(auth)
}

func (a smtpClientAdapter) Extension(name string) (bool, string) {
	return a.client.Extension(name)
}

func isLocalhost(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}

// loginAuth 实现老式 LOGIN 认证（AUTH LOGIN）。部分中继只认它，不认 PLAIN；
// Start 里还会校验链路已加密（或对方是 localhost），避免明文把口令送出去。
type loginAuth struct {
	username string
	password string
	host     string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !isLocalhost(server.Name) {
		return "", nil, fmt.Errorf("unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, fmt.Errorf("wrong host name: %q does not match expected %q", server.Name, a.host)
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}

	// 服务端提示词可能带 base64，先还原成明文再匹配关键词，两种都认。
	raw := strings.TrimSpace(string(fromServer))
	challenge := strings.ToLower(raw)
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		challenge = strings.ToLower(strings.TrimSpace(string(decoded)))
	}

	switch {
	case strings.Contains(challenge, "username") || strings.Contains(challenge, "user name"):
		return []byte(a.username), nil
	case strings.Contains(challenge, "password"):
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("unexpected LOGIN challenge %q", raw)
	}
}

// smtpAuthWithFallback 先试 PLAIN；只有当中继明确表示“不认识这种认证类型”时才回退
// 到 LOGIN（返回 fallbackToLogin=true 让调用方决定是否重连重试）。
func smtpAuthWithFallback(c smtpAuthClient, host, username, password string) (bool, error) {
	plainErr := c.Auth(smtp.PlainAuth("", username, password, host))
	if plainErr == nil {
		return false, nil
	}

	msg := strings.ToLower(plainErr.Error())
	if !strings.Contains(msg, "unrecognized authentication type") && !strings.Contains(msg, "504 5.7.4") {
		return false, plainErr
	}

	ok, authLine := c.Extension("AUTH")
	if !ok || !strings.Contains(strings.ToUpper(authLine), "LOGIN") {
		return false, plainErr
	}
	return true, plainErr
}

// resolveFromEmail 按优先级决定发件人地址：走 SMTP 时优先 SMTP_FROM_EMAIL 再退到
// RESEND_FROM_EMAIL；没配 SMTP 则用 RESEND_FROM_EMAIL，最后兜底 noreply@multica.ai。
func resolveFromEmail(smtpHost string) string {
	resendFrom := strings.TrimSpace(os.Getenv("RESEND_FROM_EMAIL"))
	if smtpHost == "" {
		if resendFrom != "" {
			return resendFrom
		}
		return "noreply@multica.ai"
	}
	if smtpFrom := strings.TrimSpace(os.Getenv("SMTP_FROM_EMAIL")); smtpFrom != "" {
		return smtpFrom
	}
	return resendFrom
}

// openSMTPClient 建立一条可用的 SMTP 连接：按配置走隐式 TLS（SMTPS）或明文直连，
// 再做 STARTTLS 升级、设置 30 秒整体读写截止时间，并按需发送自定义 EHLO 名。
func (s *EmailService) openSMTPClient() (*smtp.Client, error) {
	addr := net.JoinHostPort(s.smtpHost, s.smtpPort)

	tlsCfg := &tls.Config{
		ServerName:         s.smtpHost,
		InsecureSkipVerify: s.smtpTLSInsecure, //nolint:gosec // opt-in via SMTP_TLS_INSECURE=true
	}

	var conn net.Conn
	var err error
	if s.smtpTLSImplicit {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	if err = conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp set deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, s.smtpHost)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp client: %w", err)
	}

	if s.smtpEHLOName != "" {
		if err = c.Hello(s.smtpEHLOName); err != nil {
			c.Close()
			return nil, fmt.Errorf("smtp EHLO %s: %w", s.smtpEHLOName, err)
		}
	}

	if !s.smtpTLSImplicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err = c.StartTLS(tlsCfg); err != nil {
				c.Close()
				return nil, fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}

	return c, nil
}

// NewEmailService 从环境变量组装 EmailService。核心决策：SMTP_HOST 非空走 SMTP，
// 否则有 RESEND_API_KEY 走 Resend，两者都没有时进入“开发模式打印”路径。
func NewEmailService() *EmailService {
	apiKey := os.Getenv("RESEND_API_KEY")
	smtpHost := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	smtpPort := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	if smtpPort == "" {
		smtpPort = "25"
	}
	smtpUsername := os.Getenv("SMTP_USERNAME")
	smtpPassword := os.Getenv("SMTP_PASSWORD")
	smtpTLSInsecure := os.Getenv("SMTP_TLS_INSECURE") == "true"
	from := resolveFromEmail(smtpHost)

	// EHLO/HELO 名称只在 SMTP 中继发送路径上有意义。net/smtp 默认发 "localhost"，
	// 严格的中继（如 smtp-relay.gmail.com）会拒绝来自公网的这种问候。SMTP_EHLO_NAME
	// 没配时退到机器主机名。只在 SMTP 模式下解析，让 Resend/DEV 路径永远不碰
	// os.Hostname()、也不会打印它的失败日志。
	var smtpEHLOName string
	if smtpHost != "" {
		smtpEHLOName = strings.TrimSpace(os.Getenv("SMTP_EHLO_NAME"))
		if smtpEHLOName == "" {
			hostname, hostErr := os.Hostname()
			if hostErr != nil {
				// 空名称会让 sendSMTP 跳过 Hello()，退回 net/smtp 惰性的 "localhost"
				// ——严格中继会拒收。把它打印出来，让运维知道要显式设 SMTP_EHLO_NAME。
				fmt.Printf("EmailService: os.Hostname() failed (%v); SMTP EHLO falls back to \"localhost\" — set SMTP_EHLO_NAME for strict relays\n", hostErr)
			}
			smtpEHLOName = hostname
		}
	}

	// SMTP_TLS=implicit 要求连上后立刻做一次 TLS 握手（SMTPS）。阿里企业邮箱这类
	// 只开放 465 SSL、不广播 STARTTLS 的服务商需要它。默认（空或 "starttls"）保持
	// 原先 STARTTLS 升级的行为。
	smtpTLSMode := strings.ToLower(strings.TrimSpace(os.Getenv("SMTP_TLS")))
	smtpTLSImplicit := smtpTLSMode == "implicit" || smtpTLSMode == "smtps" || smtpTLSMode == "ssl"
	if smtpTLSMode == "" && smtpPort == "465" {
		smtpTLSImplicit = true
	}
	if smtpTLSMode != "" && !smtpTLSImplicit && smtpTLSMode != "starttls" {
		fmt.Printf("EmailService: SMTP_TLS=%q not recognized, falling back to starttls\n", smtpTLSMode)
	}

	var client *resend.Client
	if apiKey != "" {
		client = resend.NewClient(apiKey)
	}

	switch {
	case smtpHost != "":
		tlsLabel := "starttls"
		if smtpTLSImplicit {
			tlsLabel = "implicit-tls"
		}
		fmt.Printf("EmailService: SMTP relay %s:%s (%s) from=%s\n", smtpHost, smtpPort, tlsLabel, from)
	case client != nil:
		fmt.Printf("EmailService: Resend API from=%s\n", from)
	}

	return &EmailService{
		client:          client,
		fromEmail:       from,
		smtpHost:        smtpHost,
		smtpPort:        smtpPort,
		smtpUsername:    smtpUsername,
		smtpPassword:    smtpPassword,
		smtpTLSInsecure: smtpTLSInsecure,
		smtpTLSImplicit: smtpTLSImplicit,
		smtpEHLOName:    smtpEHLOName,
	}
}

// sendSMTP 经一台 SMTP 服务器投递 HTML 邮件。
// 支持免认证中继（SMTP_USERNAME 为空）和认证 SMTP。
// 服务器广播 STARTTLS 时自动升级加密。
// 自签名或私有 CA 证书可设 SMTP_TLS_INSECURE=true。
func (s *EmailService) sendSMTP(to, subject, htmlBody string) error {
	if strings.TrimSpace(s.fromEmail) == "" {
		return fmt.Errorf("SMTP_FROM_EMAIL or RESEND_FROM_EMAIL is required when SMTP_HOST is set")
	}

	c, err := s.openSMTPClient()
	if err != nil {
		return err
	}
	defer c.Close()

	if s.smtpUsername != "" {
		// PLAIN 认证可能被某些中继拒绝，此时若对方支持 LOGIN 就重连改走 LOGIN。
		fallbackToLogin, authErr := smtpAuthWithFallback(smtpClientAdapter{client: c}, s.smtpHost, s.smtpUsername, s.smtpPassword)
		if authErr != nil {
			if !fallbackToLogin {
				return fmt.Errorf("smtp auth: %w", authErr)
			}

			c.Close()
			c, err = s.openSMTPClient()
			if err != nil {
				return fmt.Errorf("smtp auth: plain auth failed (%v); login reconnect failed: %w", authErr, err)
			}
			defer c.Close()

			if err = c.Auth(&loginAuth{username: s.smtpUsername, password: s.smtpPassword, host: s.smtpHost}); err != nil {
				return fmt.Errorf("smtp auth: plain auth failed (%v); login auth fallback failed: %w", authErr, err)
			}
		}
	}

	// 在（可能的）STARTTLS 之后才探测 8BITMIME，确保扩展列表是最新的。
	// 不广播 8BITMIME 的中继改用 quoted-printable 编码正文——对非 ASCII 的工作区/
	// 邀请人名字穿越严格或老旧 SMTP 节点更安全。
	has8Bit, _ := c.Extension("8BITMIME")
	encodedSubject := mime.QEncoding.Encode("utf-8", subject)
	msgID := fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), s.smtpHost)

	var bodyBytes []byte
	var cte string
	if has8Bit {
		bodyBytes = []byte(htmlBody)
		cte = "8bit"
	} else {
		var buf strings.Builder
		qpw := quotedprintable.NewWriter(&buf)
		_, _ = qpw.Write([]byte(htmlBody))
		_ = qpw.Close()
		bodyBytes = []byte(buf.String())
		cte = "quoted-printable"
	}

	if err = c.Mail(s.fromEmail); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err = c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO <%s>: %w", to, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	headers := "From: " + s.fromEmail + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + encodedSubject + "\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: " + msgID + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: " + cte + "\r\n" +
		"\r\n"
	if _, err = fmt.Fprintf(w, "%s%s", headers, bodyBytes); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("smtp end data: %w", err)
	}
	return c.Quit()
}

// SendInvitationEmail 发送一封“XX 邀请你加入工作区”的邮件。按配置分流：SMTP 模式
// 走 sendSMTP；Resend 模式走 Resend API；两者都没有则打印一行开发提示并当成功。
func (s *EmailService) SendInvitationEmail(to, inviterName, workspaceName, invitationID string) error {
	appURL := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	if appURL == "" {
		appURL = "https://multica.ai"
	}
	inviteURL := fmt.Sprintf("%s/invite/%s", appURL, invitationID)

	if s.smtpHost != "" {
		params := buildInvitationParams(s.fromEmail, to, inviterName, workspaceName, inviteURL)
		return s.sendSMTP(to, params.Subject, params.Html)
	}
	if s.client == nil {
		fmt.Printf("[DEV] Invitation email to %s: %s invited you to %s — %s\n", to, inviterName, workspaceName, inviteURL)
		return nil
	}
	params := buildInvitationParams(s.fromEmail, to, inviterName, workspaceName, inviteURL)
	_, err := s.client.Emails.Send(params)
	return err
}

// buildInvitationParams 组装一封邀请邮件的请求体。从 SendInvitationEmail 里拆出来，
// 是为了让净化行为可以不 mock Resend SDK、也不起 SMTP 服务就做单元测试。
func buildInvitationParams(from, to, inviterName, workspaceName, inviteURL string) *resend.SendEmailRequest {
	safeWorkspace := html.EscapeString(workspaceName)
	safeInviter := html.EscapeString(inviterName)
	subjectInviter := sanitizeSubjectField(inviterName)
	subjectWorkspace := sanitizeSubjectField(workspaceName)

	return &resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Subject: fmt.Sprintf("%s invited you to %s on Multica", subjectInviter, subjectWorkspace),
		Html: fmt.Sprintf(
			`<div style="font-family: sans-serif; max-width: 480px; margin: 0 auto;">
				<h2>You're invited to join %s</h2>
				<p><strong>%s</strong> invited you to collaborate in the <strong>%s</strong> workspace on Multica.</p>
				<p style="margin: 24px 0;">
					<a href="%s" style="display: inline-block; padding: 12px 24px; background: #000; color: #fff; text-decoration: none; border-radius: 6px; font-weight: 500;">Accept invitation</a>
				</p>
				<p style="color: #666; font-size: 14px;">You'll need to log in to accept or decline the invitation.</p>
			</div>`, safeWorkspace, safeInviter, safeWorkspace, inviteURL),
	}
}

// sanitizeSubjectField 把用户可控文本净化后放进邮件 Subject。Subject 不做 HTML
// 渲染，直接做 HTML 转义会把字面实体（如 &lt;script&gt;）漏进收件箱。做法是去掉
// 控制字符（即使 Resend 也过滤 CR/LF，这里仍纵深防御类似“头注入”的滥用）并限制
// 长度，让攻击者没法把一整句钓鱼主题塞进工作区名。
func sanitizeSubjectField(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := b.String()
	if utf8.RuneCountInString(cleaned) <= maxSubjectFieldRunes {
		return cleaned
	}
	runes := []rune(cleaned)
	return string(runes[:maxSubjectFieldRunes-1]) + "…"
}

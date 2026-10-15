package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"sync"
)

// Message is an outgoing email.
type Message struct {
	To      string
	Subject string
	Text    string
	// Link is the actionable URL inside Text (kept separate so dev mailers can surface it).
	Link string
}

// Mailer sends transactional email (magic links, invitations). Implementations: LogMailer (dev),
// SMTPMailer, and later SES/Postmark adapters.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// LogMailer "sends" mail by logging it. The recipient is masked; the link IS logged because that
// is the point in local dev — never use LogMailer in production.
type LogMailer struct{ Logger *slog.Logger }

// Send implements Mailer.
func (l LogMailer) Send(_ context.Context, m Message) error {
	lg := l.Logger
	if lg == nil {
		lg = slog.Default()
	}
	lg.Info("dev mailer: email not sent (log mailer)", "to", MaskEmail(m.To), "subject", m.Subject, "link", m.Link)
	return nil
}

// MemoryMailer records messages (tests).
type MemoryMailer struct {
	mu   sync.Mutex
	Sent []Message
}

// Send implements Mailer.
func (m *MemoryMailer) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sent = append(m.Sent, msg)
	return nil
}

// Last returns the most recent message.
func (m *MemoryMailer) Last() (Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Sent) == 0 {
		return Message{}, false
	}
	return m.Sent[len(m.Sent)-1], true
}

// SMTPMailer sends plain-text mail through an SMTP relay (STARTTLS negotiated by net/smtp).
type SMTPMailer struct {
	Addr     string // host:port
	From     string
	Username string
	Password string
}

// Send implements Mailer.
func (s SMTPMailer) Send(_ context.Context, m Message) error {
	if strings.ContainsAny(m.To+m.Subject, "\r\n") {
		return fmt.Errorf("auth: invalid mail header")
	}
	host, _, _ := strings.Cut(s.Addr, ":")
	var a smtp.Auth
	if s.Username != "" {
		a = smtp.PlainAuth("", s.Username, s.Password, host)
	}
	body := "From: " + s.From + "\r\nTo: " + m.To + "\r\nSubject: " + m.Subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + m.Text
	return smtp.SendMail(s.Addr, a, s.From, []string{m.To}, []byte(body))
}

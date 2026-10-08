// Package mailer sends HTML e-mails over SMTP (plain, STARTTLS or implicit
// TLS), with timeouts so a dead mail server never blocks the monitor.
package mailer

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config of the SMTP connection.
type Config struct {
	Host     string
	Port     int
	Security string // "starttls" | "tls" | "none"
	Username string
	Password string
	From     string
	Timeout  time.Duration
	// InsecureSkipVerify disables certificate checks (internal CAs).
	InsecureSkipVerify bool
}

// Message to send.
type Message struct {
	To      []string
	Subject string
	HTML    string
	Text    string
}

// plainAuth is PLAIN auth that also works without TLS (the standard
// library refuses that; internal relays sometimes need it).
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a plainAuth) Next([]byte, bool) ([]byte, error) { return nil, nil }

// loginAuth implements AUTH LOGIN (Exchange).
type loginAuth struct{ user, pass string }

func (a loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a loginAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(from))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("váratlan SMTP kérdés: %q", from)
}

// Send delivers a message.
func Send(cfg Config, msg Message) error {
	if cfg.Host == "" {
		return errors.New("nincs megadva SMTP szerver")
	}
	if cfg.From == "" {
		return errors.New("nincs megadva feladó cím")
	}
	var to []string
	for _, t := range msg.To {
		if t = strings.TrimSpace(t); t != "" {
			to = append(to, t)
		}
	}
	if len(to) == 0 {
		return errors.New("nincs címzett")
	}
	if cfg.Port == 0 {
		cfg.Port = 25
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in for internal CAs
	d := &net.Dialer{Timeout: cfg.Timeout}
	var conn net.Conn
	var err error
	if cfg.Security == "tls" {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("kapcsolódás a(z) %s SMTP szerverhez: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout * 3))
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP: %w", err)
	}
	defer c.Close()
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	if err := c.Hello(host); err != nil {
		return fmt.Errorf("SMTP HELO: %w", err)
	}
	if cfg.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("az SMTP szerver nem támogatja a STARTTLS-t – válassza a „nincs” titkosítást vagy a TLS-t")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if cfg.Username != "" {
		ok, mechs := c.Extension("AUTH")
		if !ok {
			return errors.New("az SMTP szerver nem kér/támogat hitelesítést – hagyja üresen a felhasználónevet")
		}
		var auth smtp.Auth = plainAuth{cfg.Username, cfg.Password}
		if !strings.Contains(strings.ToUpper(mechs), "PLAIN") && strings.Contains(strings.ToUpper(mechs), "LOGIN") {
			auth = loginAuth{cfg.Username, cfg.Password}
		}
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP hitelesítés: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("SMTP feladó: %w", err)
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return fmt.Errorf("SMTP címzett (%s): %w", t, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(Build(cfg.From, to, msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP küldés: %w", err)
	}
	return c.Quit()
}

// Build renders the RFC 5322 message (multipart/alternative, UTF-8, base64).
func Build(from string, to []string, msg Message) []byte {
	var b strings.Builder
	boundary := randHex(12)
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from)
	hdr("To", strings.Join(to, ", "))
	hdr("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	hdr("Date", time.Now().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+randHex(16)+"@bimonitor>")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + ctype + "; charset=UTF-8\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString([]byte(body))
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	text := msg.Text
	if text == "" {
		text = "Ez az üzenet HTML formátumú."
	}
	part("text/plain", text)
	if msg.HTML != "" {
		part("text/html", msg.HTML)
	}
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%x", buf)
}

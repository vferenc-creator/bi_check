package mailer

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server that records one message.
func fakeSMTP(t *testing.T, requireAuth bool) (addr string, got func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var data strings.Builder
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { conn.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					w("250 OK queued")
					continue
				}
				mu.Lock()
				data.WriteString(line + "\n")
				mu.Unlock()
				continue
			}
			cmd := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				if requireAuth {
					w("250-fake")
					w("250 AUTH PLAIN LOGIN")
				} else {
					w("250 fake")
				}
			case strings.HasPrefix(cmd, "AUTH PLAIN"):
				w("235 ok")
			case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"):
				w("250 ok")
			case cmd == "DATA":
				inData = true
				w("354 go")
			case cmd == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), func() string { mu.Lock(); defer mu.Unlock(); return data.String() }
}

func TestSendPlain(t *testing.T) {
	addr, got := fakeSMTP(t, true)
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	err := Send(Config{Host: host, Port: p, Security: "none", Username: "u", Password: "p", From: "bi@energofish.hu", Timeout: 3 * time.Second},
		Message{To: []string{"a@x.hu", " "}, Subject: "Hiányzik: Vezetői riport", HTML: "<b>árvíztűrő</b>", Text: "árvíztűrő"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	msg := got()
	if !strings.Contains(msg, "Subject: =?utf-8?q?") || !strings.Contains(msg, "multipart/alternative") {
		t.Fatalf("headers: %s", msg)
	}
	if !strings.Contains(msg, base64.StdEncoding.EncodeToString([]byte("<b>árvíztűrő</b>"))) {
		t.Fatal("html body missing")
	}
}

func TestSendErrors(t *testing.T) {
	if err := Send(Config{}, Message{To: []string{"a@b"}}); err == nil {
		t.Fatal("no host")
	}
	if err := Send(Config{Host: "x", From: "f"}, Message{}); err == nil {
		t.Fatal("no recipient")
	}
	addr, _ := fakeSMTP(t, false)
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	err := Send(Config{Host: host, Port: p, Security: "starttls", From: "f@x", Timeout: 2 * time.Second}, Message{To: []string{"a@b"}, Subject: "x"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS error, got %v", err)
	}
	// Unreachable server fails fast.
	start := time.Now()
	if err := Send(Config{Host: "127.0.0.1", Port: 1, From: "f@x", Timeout: time.Second}, Message{To: []string{"a@b"}}); err == nil || time.Since(start) > 5*time.Second {
		t.Fatal("expected quick connection error")
	}
}

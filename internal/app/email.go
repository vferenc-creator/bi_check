package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
	"strings"
	"time"

	"bimonitor/branding"
	"bimonitor/internal/calendar"
	"bimonitor/internal/mailer"
	"bimonitor/internal/model"
	"bimonitor/internal/notify"
)

func (a *App) mailConfig(s model.Settings) (mailer.Config, error) {
	e := s.Email
	cfg := mailer.Config{Host: strings.TrimSpace(e.Host), Port: e.Port, Security: e.Security, Username: e.Username, From: strings.TrimSpace(e.From)}
	if e.PasswordEnc != "" {
		raw, err := base64.StdEncoding.DecodeString(e.PasswordEnc)
		if err != nil {
			return cfg, errors.New("a tárolt SMTP jelszó sérült – adja meg újra")
		}
		plain, err := a.P.Unprotect(raw)
		if err != nil {
			return cfg, errors.New("a tárolt SMTP jelszó nem fejthető vissza (másik felhasználó/gép?) – adja meg újra")
		}
		cfg.Password = string(plain)
	}
	return cfg, nil
}

func splitAddrs(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if strings.Contains(p, "@") {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) sendMail(to []string, subject, htmlBody, text string) error {
	s := a.Settings.Get()
	cfg, err := a.mailConfig(s)
	if err != nil {
		return err
	}
	return mailer.Send(cfg, mailer.Message{To: to, Subject: subject, HTML: htmlBody, Text: text})
}

// sendNoticeEmails mails problems/recoveries: the global recipients get
// everything, an item's own recipients get that item only.
func (a *App) sendNoticeEmails(ns []notify.Notice) {
	s := a.Settings.Get()
	if !s.Email.Enabled {
		return
	}
	byRcpt := map[string][]notify.Notice{}
	for _, n := range ns {
		if n.Kind == notify.KindQuietSummary {
			continue
		}
		for _, r := range s.Email.To {
			if r = strings.TrimSpace(r); r != "" {
				byRcpt[strings.ToLower(r)] = append(byRcpt[strings.ToLower(r)], n)
			}
		}
		for _, r := range splitAddrs(n.Item.EmailTo) {
			byRcpt[strings.ToLower(r)] = append(byRcpt[strings.ToLower(r)], n)
		}
	}
	// Recipients with the same set of notices share one e-mail.
	groups := map[string][]string{}
	sets := map[string][]notify.Notice{}
	for r, list := range byRcpt {
		var ids []string
		for _, n := range list {
			ids = append(ids, n.Item.ID+fmt.Sprint(n.Kind))
		}
		sort.Strings(ids)
		k := strings.Join(ids, ",")
		groups[k] = append(groups[k], r)
		sets[k] = list
	}
	for k, rcpts := range groups {
		subj, body, text := renderNoticeMail(sets[k])
		if err := a.sendMail(rcpts, subj, body, text); err != nil {
			log.Printf("e-mail küldése sikertelen (%s): %v", strings.Join(rcpts, ", "), err)
			a.P.Push("toast", map[string]any{"text": "E-mail küldése sikertelen: " + err.Error(), "error": true})
		}
	}
}

func colorOf(st model.Status) string {
	c := branding.Current.Colors
	switch st {
	case model.StatusOK:
		return c["ok"]
	case model.StatusLate:
		return c["late"]
	case model.StatusMissing:
		return c["missing"]
	case model.StatusUnreachable:
		return c["unreachable"]
	case model.StatusSuspicious:
		return c["suspicious"]
	case model.StatusWaiting:
		return c["waiting"]
	}
	return c["disabled"]
}

func badge(st model.Status) string {
	return fmt.Sprintf(`<span style="display:inline-block;padding:2px 9px;border-radius:10px;background:%s;color:#fff;font-size:12px;font-weight:600">%s</span>`, colorOf(st), html.EscapeString(st.Label()))
}

func mailFrame(title, inner string) string {
	b := branding.Current
	return `<!doctype html><html><body style="margin:0;background:#f5f5f4;font-family:Segoe UI,Arial,sans-serif;color:#1e1b1f">
<table width="100%" cellpadding="0" cellspacing="0" style="background:#f5f5f4;padding:20px 0"><tr><td align="center">
<table width="680" cellpadding="0" cellspacing="0" style="background:#fff;border-radius:10px;overflow:hidden;border:1px solid #e4e2e0">
<tr><td style="background:` + b.Colors["primary"] + `;color:#fff;padding:14px 20px;border-bottom:3px solid ` + b.Colors["accent"] + `">
<div style="font-size:16px;font-weight:700">` + html.EscapeString(b.AppName) + `</div><div style="font-size:12px;opacity:.7">` + html.EscapeString(b.Company) + `</div></td></tr>
<tr><td style="padding:18px 20px"><h2 style="margin:0 0 12px;font-size:17px">` + html.EscapeString(title) + `</h2>` + inner + `</td></tr>
<tr><td style="padding:10px 20px;font-size:11px;color:#6b6770;border-top:1px solid #e4e2e0">Automatikus üzenet – ` + time.Now().Format("2006.01.02. 15:04") + `</td></tr>
</table></td></tr></table></body></html>`
}

func row(cells ...string) string {
	var b strings.Builder
	b.WriteString("<tr>")
	for _, c := range cells {
		b.WriteString(`<td style="padding:8px 10px;border-bottom:1px solid #eee;font-size:13px;vertical-align:top">` + c + `</td>`)
	}
	b.WriteString("</tr>")
	return b.String()
}

func table(head []string, rows string) string {
	var b strings.Builder
	b.WriteString(`<table width="100%" cellpadding="0" cellspacing="0" style="border-collapse:collapse"><tr>`)
	for _, h := range head {
		b.WriteString(`<th align="left" style="padding:6px 10px;font-size:11px;text-transform:uppercase;color:#6b6770;border-bottom:2px solid #e4e2e0">` + h + `</th>`)
	}
	b.WriteString("</tr>" + rows + "</table>")
	return b.String()
}

func renderNoticeMail(ns []notify.Notice) (subject, body, text string) {
	var probs, recs int
	var rows, txt strings.Builder
	for _, n := range ns {
		if n.Kind == notify.KindRecovery {
			recs++
		} else {
			probs++
		}
		path := `<div style="font-family:Consolas,monospace;font-size:11px;color:#6b6770">` + html.EscapeString(n.Item.Path) + `</div>`
		rows.WriteString(row(badge(n.Status), "<b>"+html.EscapeString(n.Item.Name)+"</b>"+path,
			html.EscapeString(n.Reason), html.EscapeString(strings.Trim(n.Item.Group+" · "+n.Item.Owner, " ·"))))
		fmt.Fprintf(&txt, "%s – %s: %s\n  %s\n", n.Status.Label(), n.Item.Name, n.Reason, n.Item.Path)
	}
	var parts []string
	if probs > 0 {
		parts = append(parts, fmt.Sprintf("%d probléma", probs))
	}
	if recs > 0 {
		parts = append(parts, fmt.Sprintf("%d helyreállt", recs))
	}
	if len(ns) == 1 {
		subject = fmt.Sprintf("[BI Monitor] %s: %s", ns[0].Status.Label(), ns[0].Item.Name)
	} else {
		subject = "[BI Monitor] " + strings.Join(parts, ", ")
	}
	body = mailFrame(strings.Join(parts, ", "), table([]string{"Állapot", "Elem", "Részletek", "Csoport / felelős"}, rows.String()))
	return subject, body, txt.String()
}

// ---- Daily summary --------------------------------------------------------------

func (a *App) maybeDailySummary(s model.Settings, now time.Time) {
	ds := s.DailySummary
	if !ds.Enabled {
		return
	}
	h, m, ok := parseHM(ds.Time)
	if !ok || now.Hour()*60+now.Minute() < h*60+m {
		return
	}
	day := now.Format("2006-01-02")
	if a.metaGet("dailySummary") == day {
		return
	}
	a.metaSet("dailySummary", day)
	if ds.WorkdaysOnly && !a.Calendar().IsWorkday(dateOf(now)) {
		return
	}
	a.sendDailySummary(s, false)
}

func (a *App) metaGet(k string) string {
	if a.History != nil {
		return a.History.Meta(k)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.memMeta[k]
}

func (a *App) metaSet(k, v string) {
	if a.History != nil {
		_ = a.History.SetMeta(k, v)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.memMeta[k] = v
}

func parseHM(s string) (int, int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// sendDailySummary sends the digest now (force = ignore "only problems").
func (a *App) sendDailySummary(s model.Settings, force bool) error {
	snap := a.snapshot()
	counts := map[model.Status]int{}
	var problems []ItemView
	for _, v := range snap.Items {
		counts[v.State.Status]++
		if v.State.Status.Severity() >= model.SevWarning {
			problems = append(problems, v)
		}
	}
	if s.DailySummary.OnlyProblems && len(problems) == 0 && !force {
		return nil
	}
	total := len(snap.Items)
	var parts []string
	for _, st := range []model.Status{model.StatusMissing, model.StatusUnreachable, model.StatusSuspicious, model.StatusLate} {
		if c := counts[st]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, strings.ToLower(st.Label())))
		}
	}
	headline := fmt.Sprintf("%d elemből %d rendben", total, counts[model.StatusOK]+counts[model.StatusWaiting])
	if len(parts) > 0 {
		headline += "; " + strings.Join(parts, ", ")
	}
	var lastDay int
	if a.History != nil {
		if tr, err := a.History.Transitions("", time.Now().Add(-24*time.Hour), 1000); err == nil {
			for _, t := range tr {
				if t.To == string(model.StatusMissing) || t.To == string(model.StatusUnreachable) || t.To == string(model.StatusSuspicious) {
					lastDay++
				}
			}
		}
	}
	if s.DailySummary.Toast || force {
		text := headline
		for i, p := range problems {
			if i == 4 {
				text += fmt.Sprintf("\n… és még %d", len(problems)-4)
				break
			}
			text += "\n• " + p.Item.Name + " – " + p.State.Status.Label()
		}
		kind := NotifyApp
		if len(problems) > 0 {
			kind = NotifyWarning
		}
		a.P.Notify("Napi összefoglaló", text, kind)
	}
	if s.DailySummary.Email && s.Email.Enabled {
		sort.SliceStable(snap.Items, func(i, j int) bool {
			return snap.Items[i].State.Status.Severity() > snap.Items[j].State.Status.Severity()
		})
		var rows strings.Builder
		for _, v := range snap.Items {
			mod := "–"
			if v.State.File != nil {
				mod = v.State.File.ModTime.In(a.Loc).Format("01.02. 15:04")
			}
			rows.WriteString(row(badge(v.State.Status), "<b>"+html.EscapeString(v.Item.Name)+"</b><div style=\"font-size:11px;color:#6b6770\">"+html.EscapeString(v.State.ScheduleText)+"</div>",
				mod, html.EscapeString(v.State.Reason)))
		}
		inner := `<p style="font-size:14px">` + html.EscapeString(headline) + `</p>` +
			fmt.Sprintf(`<p style="font-size:13px;color:#6b6770">Az elmúlt 24 órában %d problémás állapotváltozás történt.</p>`, lastDay) +
			table([]string{"Állapot", "Elem", "Utolsó módosítás", "Részletek"}, rows.String())
		if err := a.sendMail(s.Email.To, "[BI Monitor] Napi összefoglaló – "+time.Now().Format("2006.01.02."), mailFrame("Napi összefoglaló", inner), headline); err != nil {
			log.Printf("napi összefoglaló e-mail: %v", err)
			return err
		}
	}
	return nil
}

func dateOf(t time.Time) calendar.Date { return calendar.DateOf(t) }

// ---- RPC ------------------------------------------------------------------------

// EmailPatch is saved by the e-mail settings card.
type EmailPatch struct {
	Email       model.EmailSettings `json:"email"`
	NewPassword *string             `json:"newPassword"` // nil = unchanged, "" = clear
	Summary     model.DailySummary  `json:"dailySummary"`
}

func (a *App) registerEmailAPI() {
	a.register("saveEmail", func(p EmailPatch) (model.Settings, error) {
		if _, _, ok := parseHM(p.Summary.Time); !ok {
			return redact(a.Settings.Get()), errors.New("a napi összefoglaló időpontja ÓÓ:PP formátumú legyen")
		}
		var enc string
		if p.NewPassword != nil && *p.NewPassword != "" {
			b, err := a.P.Protect([]byte(*p.NewPassword))
			if err != nil {
				return redact(a.Settings.Get()), fmt.Errorf("a jelszó titkosítása nem sikerült: %w", err)
			}
			enc = base64.StdEncoding.EncodeToString(b)
		}
		s, err := a.Settings.Update(func(s *model.Settings) error {
			old := s.Email.PasswordEnc
			s.Email = p.Email
			s.Email.PasswordEnc = old
			if p.NewPassword != nil {
				s.Email.PasswordEnc = enc
			}
			var to []string
			for _, t := range p.Email.To {
				to = append(to, splitAddrs(t)...)
			}
			s.Email.To = to
			s.DailySummary = p.Summary
			return nil
		})
		return redact(s), err
	})
	a.register("sendTestEmail", func() error {
		s := a.Settings.Get()
		if len(s.Email.To) == 0 {
			return errors.New("nincs megadva címzett")
		}
		inner := `<p>Ez egy próbaüzenet. Ha megkapta, az e-mail értesítések működnek.</p>`
		return a.sendMail(s.Email.To, "[BI Monitor] Próbaüzenet", mailFrame("Próbaüzenet", inner), "Ez egy próbaüzenet.")
	})
	a.register("sendSummaryNow", func() error { return a.sendDailySummary(a.Settings.Get(), true) })
}

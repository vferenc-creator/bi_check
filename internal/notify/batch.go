package notify

import (
	"fmt"
	"sort"
	"strings"

	"bimonitor/internal/model"
)

// Toast is a rendered desktop notification.
type Toast struct {
	Title string
	Text  string
	Error bool // problem (red) vs. info
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// RenderToasts merges the toast-worthy notices of one batch into as few
// desktop notifications as sensible (Windows shows only one at a time).
func RenderToasts(ns []Notice) []Toast {
	var problems, recoveries []Notice
	var out []Toast
	for _, n := range ns {
		if !n.Toast {
			continue
		}
		switch n.Kind {
		case KindProblem:
			problems = append(problems, n)
		case KindRecovery:
			recoveries = append(recoveries, n)
		case KindQuietSummary:
			out = append(out, quietSummary(n))
		}
	}

	// Server outages: one notice per server instead of one per item.
	byServer := map[string][]Notice{}
	var rest []Notice
	for _, n := range problems {
		if n.Status == model.StatusUnreachable && n.ServerProblem && n.Server != "" {
			byServer[n.Server] = append(byServer[n.Server], n)
		} else {
			rest = append(rest, n)
		}
	}
	servers := make([]string, 0, len(byServer))
	for s := range byServer {
		servers = append(servers, s)
	}
	sort.Strings(servers)
	for _, s := range servers {
		g := byServer[s]
		if len(g) == 1 {
			rest = append(rest, g[0])
			continue
		}
		out = append(out, Toast{
			Title: clip(fmt.Sprintf("%s nem érhető el (%d elem)", s, len(g)), 63),
			Text:  clip(g[0].Reason+"\nÉrintett: "+names(g, 6), 255),
			Error: true,
		})
	}
	switch {
	case len(rest) == 1:
		n := rest[0]
		out = append(out, Toast{Title: clip(n.Status.Label()+": "+n.Item.Name, 63), Text: clip(detail(n), 255), Error: true})
	case len(rest) > 1:
		var lines []string
		for i, n := range rest {
			if i == 6 {
				lines = append(lines, fmt.Sprintf("… és még %d", len(rest)-6))
				break
			}
			lines = append(lines, "• "+n.Item.Name+" – "+n.Status.Label())
		}
		out = append(out, Toast{Title: fmt.Sprintf("%d új probléma", len(rest)), Text: clip(strings.Join(lines, "\n"), 255), Error: true})
	}
	switch {
	case len(recoveries) == 1:
		n := recoveries[0]
		out = append(out, Toast{Title: clip("Helyreállt: "+n.Item.Name, 63), Text: clip(n.Reason, 255)})
	case len(recoveries) > 1:
		out = append(out, Toast{Title: fmt.Sprintf("%d elem helyreállt", len(recoveries)), Text: clip(names(recoveries, 8), 255)})
	}
	return out
}

func detail(n Notice) string {
	var meta []string
	if n.Item.Group != "" {
		meta = append(meta, n.Item.Group)
	}
	if n.Item.Owner != "" {
		meta = append(meta, "felelős: "+n.Item.Owner)
	}
	s := n.Reason
	if len(meta) > 0 {
		s += "\n" + strings.Join(meta, " · ")
	}
	return s
}

func names(ns []Notice, max int) string {
	var parts []string
	for i, n := range ns {
		if i == max {
			parts = append(parts, fmt.Sprintf("+%d", len(ns)-max))
			break
		}
		parts = append(parts, n.Item.Name)
	}
	return strings.Join(parts, ", ")
}

func quietSummary(n Notice) Toast {
	counts := map[model.Status]int{}
	var lines []string
	for i, st := range n.Items {
		counts[st.Status]++
		if i < 5 {
			lines = append(lines, "• "+n.Names[st.ID]+" – "+st.Status.Label())
		}
	}
	if len(n.Items) > 5 {
		lines = append(lines, fmt.Sprintf("… és még %d", len(n.Items)-5))
	}
	var parts []string
	for _, s := range []model.Status{model.StatusMissing, model.StatusUnreachable, model.StatusSuspicious, model.StatusLate} {
		if c := counts[s]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, strings.ToLower(s.Label())))
		}
	}
	return Toast{
		Title: clip("Csendes időszak alatt: "+strings.Join(parts, ", "), 63),
		Text:  clip(strings.Join(lines, "\n"), 255),
		Error: true,
	}
}

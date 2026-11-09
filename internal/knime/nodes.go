package knime

// Reading which node of the workflow failed, from the output of the run:
// knime.log lines like
//
//	2026-11-06 08:00:01,000 : ERROR : KNIME-Worker-3-DB Reader 3:12 :  : Node : DB Reader : 3:12 : Execute failed: Login failed for user 'x'
//
// (the fields are separated by " : "; the node ID is a field of its own),
// and the node states printed by NodePit after the run ("Status of workflow
// after execution:"), read tolerantly: a line with a node ID (0:12, 0:5:3)
// and a node state (EXECUTED, CONFIGURED, IDLE, …).

import (
	"regexp"
	"strings"
)

var (
	nodeIDRe    = regexp.MustCompile(`^\d+(?::\d+)+$`)
	nodeIDInRe  = regexp.MustCompile(`(?:^|[\s"(\[#])(\d+(?::\d+)+)(?:$|[\s")\]:,])`)
	nodeStateRe = regexp.MustCompile(`\b(EXECUTED|CONFIGURED|IDLE|EXECUTING|EXECUTINGREMOTELY|QUEUED|PREEXECUTE|POSTEXECUTE|MARKEDFOREXEC|UNCONFIGURED_MARKEDFOREXEC|CONFIGURED_MARKEDFOREXEC|EXECUTED_MARKEDFOREXEC|WAITING_TO_BE_EXECUTABLE|HALTED)\b`)
)

// ParseFailedNode finds the failed node in the output: the first
// "Execute failed" error of a node, else the last error that names a node.
func ParseFailedNode(text string) *FailedNode {
	var last *FailedNode
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "ERROR") {
			continue
		}
		f := strings.Split(line, " : ")
		for i := 1; i < len(f); i++ {
			id := strings.TrimSpace(f[i])
			if !nodeIDRe.MatchString(id) {
				continue
			}
			name := strings.TrimSpace(f[i-1])
			if name == "" || strings.EqualFold(name, "ERROR") {
				break
			}
			msg := strings.TrimSpace(strings.Join(f[i+1:], " : "))
			if len(msg) > 400 {
				msg = msg[:400] + "…"
			}
			n := &FailedNode{Name: name, ID: id, Message: msg}
			if strings.Contains(msg, "Execute failed") {
				return n
			}
			last = n
			break
		}
	}
	return last
}

// ParseNodeStates counts the nodes in the status printed after the run
// (0, 0 if it is not in the output). Metanodes and components count as
// one node; "(end)" lines of nested workflows are skipped.
func ParseNodeStates(text string) (executed, total int) {
	i := strings.LastIndex(text, "Status of workflow after execution")
	if i < 0 {
		return 0, 0
	}
	lines := strings.Split(text[i:], "\n")
	for _, l := range lines[1:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "----") {
			continue
		}
		if t == "" || strings.HasPrefix(t, "====") || strings.Contains(t, " : INFO ") || strings.Contains(t, " : ERROR ") || strings.Contains(t, " : WARN ") {
			if total > 0 {
				break
			}
			continue
		}
		if strings.Contains(t, "(end)") {
			continue
		}
		if !nodeIDInRe.MatchString(t) {
			continue
		}
		st := nodeStateRe.FindString(t)
		if st == "" {
			continue
		}
		total++
		if st == "EXECUTED" {
			executed++
		}
	}
	return executed, total
}

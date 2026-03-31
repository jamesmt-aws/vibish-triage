package parser

import (
	"regexp"
	"strings"
)

// Prompt holds the parsed contents of a prompt markdown file.
type Prompt struct {
	Body       string   // the full markdown
	InfraSetup string   // shell commands from ## Infrastructure Setup
	Validation string   // shell commands from ## Validation
	Tools      []string // tool names from ## Tools
}

type section struct {
	level int
	title string
	body  string
}

var headingRe = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
var fenceRe = regexp.MustCompile("^```")

// Parse parses a prompt markdown string.
func Parse(md string) *Prompt {
	sections := splitSections(md)
	p := &Prompt{Body: md}
	for _, sec := range sections {
		if sec.level != 2 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(sec.title)) {
		case "infrastructure setup":
			p.InfraSetup = stripCodeFence(strings.TrimSpace(sec.body))
		case "validation":
			p.Validation = stripCodeFence(strings.TrimSpace(sec.body))
		case "tools":
			p.Tools = parseToolsList(strings.TrimSpace(sec.body))
		}
	}
	return p
}

func splitSections(md string) []section {
	lines := strings.Split(md, "\n")
	type heading struct {
		level   int
		title   string
		lineIdx int
	}
	var headings []heading
	inFence := false
	for i, line := range lines {
		if fenceRe.MatchString(strings.TrimSpace(line)) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := headingRe.FindStringSubmatch(line)
		if m != nil {
			headings = append(headings, heading{
				level:   len(m[1]),
				title:   m[2],
				lineIdx: i,
			})
		}
	}
	if len(headings) == 0 {
		return nil
	}
	var sections []section
	for i, h := range headings {
		bodyStart := h.lineIdx + 1
		var bodyEnd int
		if i+1 < len(headings) {
			bodyEnd = headings[i+1].lineIdx
		} else {
			bodyEnd = len(lines)
		}
		sections = append(sections, section{
			level: h.level,
			title: h.title,
			body:  strings.Join(lines[bodyStart:bodyEnd], "\n"),
		})
	}
	return sections
}

func parseToolsList(body string) []string {
	var tools []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		line = strings.TrimSpace(line)
		if line != "" {
			tools = append(tools, line)
		}
	}
	return tools
}

func stripCodeFence(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(first, "```") && last == "```" {
		return strings.Join(lines[1:len(lines)-1], "\n")
	}
	return s
}

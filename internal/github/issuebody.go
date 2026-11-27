package github

import (
	"regexp"
	"strings"
)

var (
	bodyComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	bodyHeading  = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	bodyList     = regexp.MustCompile(`^(?:[-*+]|[0-9]+[.)])\s+(.*)$`)
	bodyCheckbox = regexp.MustCompile(`^\[[ xX]\]\s+(.*)$`)
)

func bodyLines(body string) []string {
	var lines []string
	fence := ""
	for _, raw := range strings.Split(bodyComment.ReplaceAllString(strings.ReplaceAll(body, "\r\n", "\n"), ""), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:1]
			length := len(line) - len(strings.TrimLeft(line, marker))
			if fence == "" {
				if marker != "`" || !strings.Contains(line[length:], "`") {
					fence = line[:length]
					lines = append(lines, "")
					continue
				}
			} else {
				if strings.HasPrefix(line, fence) && strings.TrimSpace(line[length:]) == "" {
					fence = ""
				}
				continue
			}
		}
		if fence == "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func refLine(line string) string {
	key, value, ok := strings.Cut(line, ":")
	fields := strings.Fields(value)
	if ok && strings.EqualFold(key, "Metronome") && len(fields) == 1 {
		return fields[0]
	}
	return ""
}

func bodyRef(body string) string {
	for _, line := range bodyLines(body) {
		if ref := refLine(line); ref != "" {
			return ref
		}
	}
	return ""
}

func acceptanceSection(lines []string) (int, int) {
	for index, line := range lines {
		heading := bodyHeading.FindStringSubmatch(line)
		if heading == nil || !strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(heading[2], ":")), "Acceptance criteria") {
			continue
		}
		for end := index + 1; end < len(lines); end++ {
			next := bodyHeading.FindStringSubmatch(lines[end])
			if next != nil && len(next[1]) <= len(heading[1]) {
				return index, end
			}
		}
		return index, len(lines)
	}
	return -1, -1
}

func bodyIntent(body string) string {
	lines := bodyLines(body)
	start, end := acceptanceSection(lines)
	var paragraph []string
	list := false
	for index, line := range lines {
		if line == "" || bodyHeading.MatchString(line) || refLine(line) != "" || (index >= start && index < end) || bodyList.MatchString(line) {
			if len(paragraph) > 0 {
				break
			}
			list = bodyList.MatchString(line)
			continue
		}
		if !list {
			paragraph = append(paragraph, line)
		}
	}
	intent := strings.Join(strings.Fields(strings.Join(paragraph, " ")), " ")
	runes := []rune(intent)
	if len(runes) > 500 {
		intent = string(runes[:499])
		if last := strings.LastIndex(intent, " "); last > 0 && runes[499] != ' ' {
			intent = intent[:last]
		}
		intent = strings.TrimSpace(intent) + "…"
	}
	return intent
}

func bodyCriteria(body string) []string {
	lines := bodyLines(body)
	start, end := acceptanceSection(lines)
	if start >= 0 {
		lines = lines[start+1 : end]
	}
	criteria := []string{}
	continuing := false
	for _, line := range lines {
		match := bodyList.FindStringSubmatch(line)
		if match != nil {
			checkbox := bodyCheckbox.FindStringSubmatch(match[1])
			continuing = start >= 0 || checkbox != nil
			if continuing {
				text := match[1]
				if checkbox != nil {
					text = checkbox[1]
				}
				criteria = append(criteria, strings.Join(strings.Fields(text), " "))
			}
		} else if line == "" || bodyHeading.MatchString(line) || refLine(line) != "" {
			continuing = false
		} else if continuing {
			criteria[len(criteria)-1] += " " + strings.Join(strings.Fields(line), " ")
		}
	}
	return criteria
}

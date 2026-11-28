package ado

import (
	"html"
	"strings"
)

type blockKind string

const (
	paragraph blockKind = "paragraph"
	item      blockKind = "item"
	heading   blockKind = "heading"
)

type block struct {
	kind blockKind
	text string
}

func tagLetter(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func readTag(text string) (name string, closing bool, end int) {
	start := 1
	closing = strings.HasPrefix(text, "</")
	if closing {
		start++
	}
	if start >= len(text) || !tagLetter(text[start]) {
		return "", false, -1
	}
	index := start + 1
	for index < len(text) {
		char := text[index]
		if !tagLetter(char) && !(char >= '0' && char <= '9') && char != '-' {
			break
		}
		index++
	}
	if index == len(text) || !strings.ContainsRune(" >\t\n\r\f/", rune(text[index])) {
		return "", false, -1
	}
	name = strings.ToLower(text[start:index])

	var quote byte
	for ; index < len(text); index++ {
		char := text[index]
		if quote != 0 {
			if char == quote {
				quote = 0
			}
		} else if char == '\'' || char == '"' {
			quote = char
		} else if char == '>' {
			return name, closing, index
		} else if char == '<' {
			return "", false, -1
		}
	}
	return "", false, -1
}

func htmlBlocks(text string) []block {
	original := text
	foundMarkup := false
	var blocks []block
	var pending strings.Builder
	kind := paragraph
	flush := func() {
		if value := strings.Join(strings.Fields(html.UnescapeString(pending.String())), " "); value != "" {
			blocks = append(blocks, block{kind, value})
		}
		pending.Reset()
	}
	for len(text) > 0 {
		start := strings.IndexByte(text, '<')
		if start < 0 {
			pending.WriteString(text)
			break
		}
		pending.WriteString(text[:start])
		text = text[start:]
		if strings.HasPrefix(text, "<!--") {
			end := strings.Index(text[4:], "-->")
			if end >= 0 {
				foundMarkup = true
				text = text[end+7:]
				continue
			}
		}
		name, closing, end := readTag(text)
		if end < 0 {
			pending.WriteByte('<')
			text = text[1:]
			continue
		}
		foundMarkup = true
		text = text[end+1:]
		switch name {
		case "li":
			flush()
			kind = paragraph
			if !closing {
				kind = item
			}
		case "h1", "h2", "h3", "h4", "h5", "h6":
			flush()
			kind = paragraph
			if !closing {
				kind = heading
			}
		case "ul", "ol":
			flush()
			kind = paragraph
		case "p", "div", "br", "table", "tr", "td", "th", "blockquote", "pre":
			flush()
		}
	}
	if !foundMarkup {
		pending.Reset()
		for _, line := range strings.Split(original, "\n") {
			pending.WriteString(line)
			flush()
		}
		return blocks
	}
	flush()
	return blocks
}

package confluence

import (
	"encoding/json"
	"strings"
)

// Atlas Document Format (ADF) is Confluence Cloud's JSON document tree.
// We split top-level blocks into Onyx sections at h1/h2 boundaries and
// render each block to plain text. Unknown block types degrade gracefully.

type adfDoc struct {
	Type    string    `json:"type"`
	Version int       `json:"version"`
	Content []adfNode `json:"content"`
}

type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Attrs   map[string]any `json:"attrs"`
	Content []adfNode      `json:"content"`
}

// adfSplitHeadingLevel is the maximum heading level that starts a new
// section (1 = h1, 2 = h2).
const adfSplitHeadingLevel = 2

// adfToSections renders an ADF payload to plain-text section strings,
// splitting top-level content at headings of level <= adfSplitHeadingLevel.
// Returns nil when the payload is malformed or empty so callers can fall
// back to the storage-format parser.
func adfToSections(raw string) []string {
	var doc adfDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}

	var sections []string
	var current strings.Builder
	flush := func() {
		if txt := strings.TrimSpace(current.String()); txt != "" {
			sections = append(sections, txt)
		}
		current.Reset()
	}
	for _, node := range doc.Content {
		level := headingLevel(node)
		if level > 0 && level <= adfSplitHeadingLevel && strings.TrimSpace(current.String()) != "" {
			flush()
		}
		writeADFNode(&current, node)
	}
	flush()

	if len(sections) == 0 {
		return nil
	}
	if len(sections) > maxSections {
		merged := strings.Join(sections[maxSections-1:], "\n\n")
		sections = append(sections[:maxSections-1], merged)
	}
	return sections
}

// headingLevel returns the heading level (1-6) for heading nodes, 0 otherwise.
func headingLevel(node adfNode) int {
	if node.Type != "heading" {
		return 0
	}
	level, ok := node.Attrs["level"].(float64)
	if !ok {
		return 0
	}
	return int(level)
}

// writeADFNode renders one top-level block to the builder. Blocks are
// separated by newlines; content is plain text with structure preserved
// approximately (list markers, table pipes).
func writeADFNode(b *strings.Builder, node adfNode) {
	switch node.Type {
	case "paragraph", "heading", "blockquote":
		inlineText(b, node.Content)
		b.WriteString("\n")
	case "bulletList":
		writeList(b, node, "• ")
	case "orderedList":
		writeList(b, node, "1. ")
	case "codeBlock":
		for _, child := range node.Content {
			if child.Text != "" {
				b.WriteString(child.Text)
			}
		}
		b.WriteString("\n")
	case "table":
		writeTable(b, node)
	case "expand":
		if title, ok := node.Attrs["title"].(string); ok && title != "" {
			b.WriteString("[expand] " + title + "\n")
		}
		for _, child := range node.Content {
			writeADFNode(b, child)
		}
	case "rule":
		// skip
	case "mediaSingle", "extension", "bodiedExtension":
		// placeholders; attachments need a different pipeline (TODO)
	default:
		// best-effort: render children if present
		if len(node.Content) > 0 {
			inlineText(b, node.Content)
			b.WriteString("\n")
		}
	}
}

func writeList(b *strings.Builder, node adfNode, marker string) {
	for _, item := range node.Content {
		if item.Type != "listItem" {
			continue
		}
		for _, child := range item.Content {
			switch child.Type {
			case "bulletList", "orderedList":
				writeList(b, child, marker)
			default:
				b.WriteString(marker)
				inlineText(b, child.Content)
				b.WriteString("\n")
			}
		}
	}
}

func writeTable(b *strings.Builder, node adfNode) {
	for _, row := range node.Content {
		if row.Type != "tableRow" {
			continue
		}
		var cells []string
		for _, cell := range row.Content {
			if cell.Type != "tableCell" && cell.Type != "tableHeader" {
				continue
			}
			var cellText strings.Builder
			inlineText(&cellText, flattenInline(cell.Content))
			cells = append(cells, strings.TrimSpace(cellText.String()))
		}
		if len(cells) > 0 {
			b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		}
	}
}

// flattenInline wraps top-level block content so table cells (paragraphs)
// can be rendered through inlineText.
func flattenInline(content []adfNode) []adfNode {
	var out []adfNode
	for _, node := range content {
		if len(node.Content) > 0 && node.Type != "text" {
			out = append(out, flattenInline(node.Content)...)
			continue
		}
		out = append(out, node)
	}
	return out
}

// inlineText concatenates text within a block, handling hard breaks, marks,
// mentions, and inline cards.
func inlineText(b *strings.Builder, content []adfNode) {
	for _, node := range content {
		switch node.Type {
		case "text":
			b.WriteString(node.Text)
		case "hardBreak":
			b.WriteString(" ")
		case "mention":
			if text, ok := node.Attrs["text"].(string); ok {
				b.WriteString(text)
			}
		case "inlineCard":
			if url, ok := node.Attrs["url"].(string); ok {
				b.WriteString(url)
			}
		default:
			inlineText(b, node.Content)
		}
	}
}

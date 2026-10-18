package confluence

import (
	"strings"
	"unicode"
)

// blockTags separate text with a newline when encountered as opening tags
// in storage HTML. Closing tags contribute nothing (the matching opening
// tag already emitted a separator).
var blockTags = map[string]bool{
	"p": true, "br": true, "li": true, "tr": true, "table": true,
	"ul": true, "ol": true, "pre": true, "blockquote": true,
	"div": true, "section": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

var entityReplacer = strings.NewReplacer(
	"&nbsp;", " ",
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&apos;", "'",
	"&#39;", "'",
)

// htmlToText renders a rough plain-text version of Confluence storage-format
// HTML: tags are dropped, common block elements become newlines, common named
// entities are decoded, and whitespace is collapsed per line. Replace with a
// full converter (e.g. golang.org/x/net/html based) if higher fidelity to
// Confluence storage format ever matters.
func htmlToText(html string) string {
	var b strings.Builder
	b.Grow(len(html))
	i := 0
	for i < len(html) {
		if html[i] != '<' {
			b.WriteByte(html[i])
			i++
			continue
		}
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			break
		}
		raw := html[i+1 : i+end]
		closing := strings.HasPrefix(strings.TrimLeft(raw, " \t"), "/")
		if name := tagName(raw); !closing && blockTags[name] {
			b.WriteByte('\n')
		}
		i += end + 1
	}
	return collapseWhitespace(entityReplacer.Replace(b.String()))
}

// tagName extracts the lower-case element name from an opening or closing
// tag body, e.g. "/p" -> "p", "div class=x" -> "div".
func tagName(raw string) string {
	raw = strings.TrimLeft(raw, "/! \t")
	j := 0
	for j < len(raw) && (unicode.IsLetter(rune(raw[j])) || unicode.IsDigit(rune(raw[j]))) {
		j++
	}
	return strings.ToLower(raw[:j])
}

// collapseWhitespace trims each line, collapses interior whitespace, drops
// leading/trailing blank lines, and merges runs of interior blank lines.
func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.Join(strings.Fields(line), " ")
		if trimmed == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, trimmed)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

package confluence

import "strings"

// splitHeadingNames are the heading levels that start a new Onyx section.
// Deeper headings (h3+) stay inside their parent section to avoid fragmenting
// pages that use fine-grained structure.
var splitHeadingNames = map[string]bool{"h1": true, "h2": true}

// maxSections caps the number of sections produced per page; overflow is
// merged into the final section so pathological pages cannot flood Onyx.
const maxSections = 64

// splitSections divides Confluence storage HTML into plain-text chunks at
// h1/h2 boundaries. Each heading's text stays with the content that follows
// it, and any content before the first heading forms its own chunk. Empty
// chunks (e.g. adjacent headings) are dropped; an empty page yields a single
// empty-text chunk to preserve the "one section per page" contract.
func splitSections(html string) []string {
	var bounds []int
	i := 0
	for i < len(html) {
		lt := strings.IndexByte(html[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			break
		}
		raw := html[i+1 : i+end]
		closing := strings.HasPrefix(strings.TrimLeft(raw, " \t"), "/")
		if !closing && i > 0 && splitHeadingNames[tagName(raw)] {
			bounds = append(bounds, i)
		}
		i += end + 1
	}

	sections := make([]string, 0, len(bounds)+1)
	prev := 0
	for _, b := range bounds {
		if txt := htmlToText(html[prev:b]); txt != "" {
			sections = append(sections, txt)
		}
		prev = b
	}
	if txt := htmlToText(html[prev:]); txt != "" {
		sections = append(sections, txt)
	}

	if len(sections) > maxSections {
		merged := strings.Join(sections[maxSections-1:], "\n\n")
		sections = append(sections[:maxSections-1], merged)
	}
	if len(sections) == 0 {
		sections = []string{""}
	}
	return sections
}

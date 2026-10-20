package extract

import (
	"encoding/xml"
	"fmt"
	"html"
	"strings"
)

// drawioToText extracts the human-readable labels from an uncompressed
// .drawio file (mxGraph XML). Diagram text lives on `value` attributes of
// mxCell/UserObject elements, HTML-escaped with embedded markup; we decode
// entities, strip tags, and emit one label per line. Search indexing needs
// the vocabulary of the diagram, not its geometry.
//
// Compressed diagrams (base64+deflate payload inside <diagram>) are
// declined with ErrSkip: supporting them is a base64+flate+url unescape
// away, but doing so without test vectors from the source tenant is guess
// work. The space this ships for keeps diagrams uncompressed.
func drawioToText(data []byte) (string, error) {
	var root mxFile
	if err := xml.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("%w: drawio XML: %v", ErrSkip, err)
	}
	var labels []string
	seen := map[string]bool{}
	for _, d := range root.Diagrams {
		if c := strings.TrimSpace(d.Compressed); c != "" {
			return "", fmt.Errorf("%w: compressed drawio diagram %q", ErrSkip, d.Name)
		}
		for _, e := range d.Model.Root.All {
			lbl := cleanLabel(e.Value)
			if lbl == "" {
				lbl = cleanLabel(e.Label)
			}
			if lbl == "" || seen[lbl] {
				continue
			}
			seen[lbl] = true
			labels = append(labels, lbl)
		}
	}
	if len(root.Diagrams) > 0 && len(labels) == 0 {
		return "", fmt.Errorf("%w: drawio contains no node labels", ErrSkip)
	}
	if len(root.Diagrams) == 0 {
		return "", fmt.Errorf("%w: not a drawio mxfile", ErrSkip)
	}
	return strings.Join(labels, "\n"), nil
}

type mxFile struct {
	Diagrams []mxDiagram `xml:"diagram"`
}

type mxDiagram struct {
	Name       string  `xml:"name,attr"`
	Compressed string  `xml:",chardata"`
	Model      mxModel `xml:"mxGraphModel"`
}

type mxModel struct {
	Root mxRoot `xml:"root"`
}

type mxRoot struct {
	All []mxCell `xml:",any"`
}

// mxCell covers mxCell and UserObject (and their children are ignored:
// geometry/style elements contribute nothing searchable).
type mxCell struct {
	Value string `xml:"value,attr"`
	Label string `xml:"label,attr"`
}

var tagRE = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n")

// cleanLabel decodes HTML entities, replaces explicit line breaks with
// newlines, strips remaining tags, and collapses whitespace runs.
func cleanLabel(s string) string {
	if s == "" {
		return ""
	}
	s = html.UnescapeString(s)
	s = tagRE.Replace(s)
	var b strings.Builder
	tag := false
	for _, r := range s {
		switch {
		case r == '<':
			tag = true
		case r == '>':
			tag = false
		case !tag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

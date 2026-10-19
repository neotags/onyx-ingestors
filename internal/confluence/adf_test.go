package confluence

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const adfSample = `{
  "version": 1,
  "type": "doc",
  "content": [
    {"type": "paragraph", "content": [{"type": "text", "text": "Intro text"}]},
    {"type": "heading", "attrs": {"level": 2}, "content": [{"type": "text", "text": "Overview"}]},
    {"type": "paragraph", "content": [
      {"type": "text", "text": "Details body"},
      {"type": "hardBreak"},
      {"type": "text", "text": "second line"},
      {"type": "mention", "attrs": {"text": "@John"}}
    ]},
    {"type": "bulletList", "content": [
      {"type": "listItem", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "A"}]}]},
      {"type": "listItem", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "B"}]}]}
    ]},
    {"type": "table", "content": [
      {"type": "tableRow", "content": [
        {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "h1"}]}]},
        {"type": "tableHeader", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "h2"}]}]}
      ]},
      {"type": "tableRow", "content": [
        {"type": "tableCell", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "c1"}]}]},
        {"type": "tableCell", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "c2"}]}]}
      ]}
    ]},
    {"type": "heading", "attrs": {"level": 3}, "content": [{"type": "text", "text": "Sub"}]},
    {"type": "codeBlock", "content": [{"type": "text", "text": "print(1)"}]},
    {"type": "rule"}
  ]
}`

func TestADFToSections(t *testing.T) {
	sections := adfToSections(adfSample)
	require.Len(t, sections, 2)

	assert.Equal(t, "Intro text", sections[0])

	want := "Overview\n" +
		"Details body second line@John\n" +
		"• A\n" +
		"• B\n" +
		"| h1 | h2 |\n" +
		"| c1 | c2 |\n" +
		"Sub\n" +
		"print(1)"
	assert.Equal(t, want, sections[1])
}

func TestADFToSectionsLevel1Splits(t *testing.T) {
	raw := `{"type":"doc","content":[
		{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"One"}]},
		{"type":"paragraph","content":[{"type":"text","text":"a"}]},
		{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Two"}]},
		{"type":"paragraph","content":[{"type":"text","text":"b"}]}
	]}`
	sections := adfToSections(raw)
	require.Len(t, sections, 2)
	assert.Equal(t, "One\na", sections[0])
	assert.Equal(t, "Two\nb", sections[1])
}

func TestADFToSectionsNoHeadings(t *testing.T) {
	raw := `{"type":"doc","content":[
		{"type":"paragraph","content":[{"type":"text","text":"only"}]}
	]}`
	sections := adfToSections(raw)
	require.Len(t, sections, 1)
	assert.Equal(t, "only", sections[0])
}

func TestADFToSectionsInvalidReturnsNil(t *testing.T) {
	assert.Nil(t, adfToSections("not json"))
	assert.Nil(t, adfToSections(""))
	assert.Nil(t, adfToSections(`{"type":"doc","content":[]}`))
}

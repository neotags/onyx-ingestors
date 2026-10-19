package confluence

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitSectionsNoHeadings(t *testing.T) {
	sections := splitSections("<p>Intro only</p>")
	require.Len(t, sections, 1)
	assert.Equal(t, "Intro only", sections[0])
}

func TestSplitSectionsEmptyPage(t *testing.T) {
	sections := splitSections("")
	require.Len(t, sections, 1)
	assert.Equal(t, "", sections[0])
}

func TestSplitSectionsWithIntro(t *testing.T) {
	body := `<p>Intro text</p>
<h1>Overview</h1><p>Overview body</p>
<h2 id="details">Details</h2><p>Details <b>body</b></p>`
	sections := splitSections(body)
	require.Len(t, sections, 3)
	assert.Equal(t, "Intro text", sections[0])
	assert.Equal(t, "Overview\nOverview body", sections[1])
	assert.Equal(t, "Details\nDetails body", sections[2])
}

func TestSplitSectionsDeeperHeadingsStay(t *testing.T) {
	body := `<h2>Section</h2><h3>Sub</h3><p>body</p>`
	sections := splitSections(body)
	require.Len(t, sections, 1)
	assert.Equal(t, "Section\nSub\nbody", sections[0])
}

func TestSplitSectionsDropsEmptyChunks(t *testing.T) {
	body := `<h1>A</h1><h1>B</h1><p>content</p>`
	sections := splitSections(body)
	require.Len(t, sections, 2)
	assert.Equal(t, "A", sections[0])
	assert.Equal(t, "B\ncontent", sections[1])
}

func TestSplitSectionsCapsAtMax(t *testing.T) {
	var b strings.Builder
	b.WriteString("<p>intro</p>")
	for i := 0; i < maxSections+10; i++ {
		fmt.Fprintf(&b, "<h1>Heading %d</h1><p>body %d</p>", i, i)
	}
	sections := splitSections(b.String())
	assert.Len(t, sections, maxSections)
	last := sections[len(sections)-1]
	assert.Contains(t, last, fmt.Sprintf("Heading %d", maxSections))
	assert.Contains(t, last, "body")
}

package confluence

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHTMLToText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", "hello"},
		{"inline tags", "<p>Hello <b>world</b></p>", "Hello world"},
		{"blocks", "<p>One</p><p>Two</p>", "One\nTwo"},
		{"break", "one<br/>two", "one\ntwo"},
		{"entities", "Tom&nbsp;&amp;&nbsp;Jerry &lt;3 &quot;hi&quot;", `Tom & Jerry <3 "hi"`},
		{"whitespace", "<p>  a   b  c </p>", "a b c"},
		{"blank collapse", "<p>a</p>\n\n<p>b</p>", "a\n\nb"},
		{"closing-only", "</li>done", "done"},
		{"confluence macros", `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>Note</p></ac:rich-text-body></ac:structured-macro>`, "Note"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, htmlToText(tc.in))
		})
	}
}

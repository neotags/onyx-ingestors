package extract

import (
	"errors"
	"strings"
	"testing"
)

func TestDrawioExtractsLabels(t *testing.T) {
	raw := `<?xml version="1.0" encoding="UTF-8"?>
<mxfile host="app.diagrams.net">
  <diagram name="Page-1" id="abc">
    <mxGraphModel>
      <root>
        <mxCell id="0" />
        <mxCell id="1" parent="0" />
        <mxCell id="a" value="PLB 保留 30 天" parent="1" vertex="1" />
        <mxCell id="b" value="Inspect &lt;b&gt;audit&lt;/b&gt;&lt;br&gt;log pipeline" parent="1" vertex="1" />
        <UserObject id="c" label="专线网关" />
        <mxCell id="d" value="" parent="1" />
      </root>
    </mxGraphModel>
  </diagram>
</mxfile>`
	got, err := Convert("topology.drawio", []byte(raw))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	for _, want := range []string{"PLB 保留 30 天", "Inspect audit", "log pipeline", "专线网关"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<") || strings.Contains(got, "&lt;") {
		t.Errorf("output still contains markup:\n%s", got)
	}
}

func TestDrawioCompressedSkipped(t *testing.T) {
	raw := `<mxfile><diagram name="P" id="x">aGVsbG8gd29ybGQ=</diagram></mxfile>`
	_, err := Convert("c.drawio", []byte(raw))
	if !errors.Is(err, ErrSkip) {
		t.Fatalf("want ErrSkip, got %v", err)
	}
}

func TestTextPassthrough(t *testing.T) {
	got, err := Convert("notes.txt", []byte("\xEF\xBB\xBFhello \xff world"))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got != "hello  world" {
		t.Errorf("got %q", got)
	}
}

func TestUnsupportedTypeSkips(t *testing.T) {
	_, err := Convert("photo.png", []byte("\x89PNG"))
	if !errors.Is(err, ErrSkip) {
		t.Fatalf("want ErrSkip, got %v", err)
	}
}

func TestTruncate(t *testing.T) {
	in := strings.Repeat("界", 10)
	got := Truncate(in, 4)
	if !strings.HasPrefix(got, "界界界界") || !strings.Contains(got, "truncated") {
		t.Errorf("got %q", got)
	}
	if Truncate(in, 0) != in {
		t.Error("cap<=0 must not truncate")
	}
}

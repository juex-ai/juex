package observable_test

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/observable"
)

func TestPipeline_TextNoFiltersEmitsContent(t *testing.T) {
	pipe, err := observable.NewPipeline(validSpec("logs"))
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stdout", []byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Content != "hello\n" || units[0].Kind != "log_batch" || units[0].Severity != "info" {
		t.Fatalf("units = %+v", units)
	}
}

func TestPipeline_FilterOverridesDefaults(t *testing.T) {
	spec := validSpec("test-watch")
	spec.Defaults = observable.Defaults{Kind: "log_batch", Severity: "info"}
	spec.Filters = []observable.FilterSpec{{Contains: "FAIL", Kind: "test_failure", Severity: "error"}}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stderr", []byte("pkg/foo FAIL\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Kind != "test_failure" || units[0].Severity != "error" {
		t.Fatalf("units = %+v", units)
	}
	dropped, err := pipe.Accept("stderr", []byte("ok\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped units = %+v, want none", dropped)
	}
}

func TestPipeline_RegexFilterMatches(t *testing.T) {
	spec := validSpec("panic-watch")
	spec.Filters = []observable.FilterSpec{{Regex: `panic: .*`, Kind: "panic", Severity: "critical"}}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stderr", []byte("panic: boom\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Severity != "critical" {
		t.Fatalf("units = %+v", units)
	}
}

func TestPipeline_JSONLFieldMapping(t *testing.T) {
	spec := validSpec("lark-events")
	spec.Parser = &observable.ParserSpec{
		Type:          "jsonl",
		ContentField:  "content",
		KindField:     "type",
		SeverityField: "level",
	}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stdout", []byte(`{"type":"lark_notification","level":"warning","content":"hello"}`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Content != "hello" || units[0].Kind != "lark_notification" || units[0].Severity != "warning" {
		t.Fatalf("units = %+v", units)
	}
}

func TestPipeline_JSONLAttachmentFieldMapping(t *testing.T) {
	spec := validSpec("lark-events")
	spec.Parser = &observable.ParserSpec{
		Type:             "jsonl",
		ContentField:     "content",
		AttachmentsField: "attachments",
	}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"content":"image event","attachments":[{"path":".juex/inbox/photo.png","media_type":"image/png"}]}` + "\n"
	units, err := pipe.Accept("stdout", []byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("units = %+v, want one unit", units)
	}
	got := units[0].Attachments
	if len(got) != 1 || got[0].Path != ".juex/inbox/photo.png" || got[0].MediaType != "image/png" {
		t.Fatalf("attachments = %+v", got)
	}
}

func TestPipeline_JSONLNormalizesSeverity(t *testing.T) {
	spec := validSpec("lark-events")
	spec.Parser = &observable.ParserSpec{
		Type:          "jsonl",
		ContentField:  "content",
		SeverityField: "level",
	}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stdout", []byte(`{"level":"WARN","content":"hello"}`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Severity != "warning" {
		t.Fatalf("units = %+v, want warning severity", units)
	}
	units, err = pipe.Accept("stdout", []byte(`{"level":"ERROR","content":"boom"}`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Severity != "error" {
		t.Fatalf("units = %+v, want error severity", units)
	}
}

func TestPipeline_JSONLInvalidLineReturnsError(t *testing.T) {
	spec := validSpec("lark-events")
	spec.Parser = &observable.ParserSpec{Type: "jsonl", ContentField: "content"}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stdout", []byte("{bad json}\n"))
	if err == nil || !strings.Contains(err.Error(), "jsonl") {
		t.Fatalf("err = %v, want jsonl parse error", err)
	}
	if len(units) != 0 {
		t.Fatalf("units = %+v, want none", units)
	}
}

func TestPipeline_JSONLPreservesValidLinesAroundMalformedLine(t *testing.T) {
	spec := validSpec("lark-events")
	spec.Parser = &observable.ParserSpec{Type: "jsonl", ContentField: "content"}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("{\"content\":\"before\"}\n{bad json}\n{\"content\":\"after\"}\n")
	units, err := pipe.Accept("stdout", input)
	if err == nil || !strings.Contains(err.Error(), "jsonl") {
		t.Fatalf("err = %v, want jsonl parse error", err)
	}
	if len(units) != 2 || units[0].Content != "before" || units[1].Content != "after" {
		t.Fatalf("units = %+v, want valid lines around malformed line", units)
	}
}

func TestPipeline_JSONLFlushesFinalLineWithoutNewline(t *testing.T) {
	spec := validSpec("lark-events")
	spec.Parser = &observable.ParserSpec{
		Type:         "jsonl",
		ContentField: "content",
		KindField:    "type",
	}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stdout", []byte(`{"type":"lark_notification","content":"last event"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Fatalf("Accept units = %+v, want buffered final line", units)
	}
	units, err = pipe.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Content != "last event" || units[0].Kind != "lark_notification" {
		t.Fatalf("Flush units = %+v", units)
	}
}

func TestPipeline_BinaryLikeOutputIsSanitized(t *testing.T) {
	pipe, err := observable.NewPipeline(validSpec("binary"))
	if err != nil {
		t.Fatal(err)
	}
	units, err := pipe.Accept("stdout", append([]byte("abc\x00def"), make([]byte, 32)...))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || !strings.Contains(units[0].Content, "binary output omitted") {
		t.Fatalf("units = %+v, want binary placeholder", units)
	}
}

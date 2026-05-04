package frontmatter

import "testing"

func TestParse_Standard(t *testing.T) {
	in := "---\nname: hello\ndescription: world\ntype: feedback\n---\nbody line 1\nbody line 2"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Fields["name"] != "hello" || p.Fields["description"] != "world" || p.Fields["type"] != "feedback" {
		t.Fatalf("fields = %+v", p.Fields)
	}
	if p.Body != "body line 1\nbody line 2" {
		t.Fatalf("body = %q", p.Body)
	}
}

func TestParse_NoFrontmatter(t *testing.T) {
	in := "just body\nno fm"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fields) != 0 {
		t.Fatalf("fields = %+v", p.Fields)
	}
	if p.Body != in {
		t.Fatalf("body = %q", p.Body)
	}
}

func TestParse_QuotedValues(t *testing.T) {
	in := "---\nname: \"hello\"\ndescription: 'a quoted value'\n---\n"
	p, _ := Parse(in)
	if p.Fields["name"] != "hello" || p.Fields["description"] != "a quoted value" {
		t.Fatalf("fields = %+v", p.Fields)
	}
}

func TestParse_MissingClosing(t *testing.T) {
	in := "---\nname: x\nbody never closed"
	if _, err := Parse(in); err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_ValueWithEmbeddedQuotes(t *testing.T) {
	// Outer quote-stripping must not lose embedded quotes.
	in := "---\ndescription: A line with: \"inner\" and 'more'\n---\nbody"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Fields["description"]; got != `A line with: "inner" and 'more'` {
		t.Fatalf("description = %q", got)
	}
}

func TestParse_ValueWithEmbeddedColon(t *testing.T) {
	// Only the first ':' splits key from value; later colons stay in value.
	in := "---\nlink: https://example.com:8080/path\n---\n"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Fields["link"]; got != "https://example.com:8080/path" {
		t.Fatalf("link = %q", got)
	}
}

func TestParse_EmptyValue(t *testing.T) {
	in := "---\nname: x\nempty:\n---\n"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Fields["empty"]; !ok {
		t.Fatalf("empty field missing")
	}
	if p.Fields["empty"] != "" {
		t.Fatalf("empty field value = %q", p.Fields["empty"])
	}
}

func TestParse_HashCommentLineSkipped(t *testing.T) {
	in := "---\n# this is a comment\nname: real\n---\nbody"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Fields["name"] != "real" {
		t.Fatalf("fields = %+v", p.Fields)
	}
	if _, ok := p.Fields["#"]; ok {
		t.Fatalf("comment leaked into fields: %+v", p.Fields)
	}
}

func TestParse_BlankLineInsideFrontmatter(t *testing.T) {
	in := "---\nname: x\n\ndescription: d\n---\nbody"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Fields["name"] != "x" || p.Fields["description"] != "d" {
		t.Fatalf("fields = %+v", p.Fields)
	}
}

func TestFormat_RoundTripWithParse(t *testing.T) {
	fields := map[string]string{
		"name":        "round-trip",
		"description": "has: colon and \"quotes\"",
		"type":        "feedback",
	}
	body := "Body text\nwith multiple lines\n"
	doc := Format(fields, []string{"name", "description", "type"}, body)
	parsed, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range fields {
		if parsed.Fields[k] != want {
			t.Errorf("%s: got %q, want %q", k, parsed.Fields[k], want)
		}
	}
	if parsed.Body != body {
		t.Errorf("body mismatch: got %q want %q", parsed.Body, body)
	}
}

func TestFormat_KeyOrder(t *testing.T) {
	out := Format(map[string]string{"a": "1", "b": "2", "c": "3"}, []string{"b", "a"}, "body")
	want := "---\nb: 2\na: 1\nc: 3\n---\nbody"
	if out != want {
		t.Fatalf("got %q", out)
	}
}

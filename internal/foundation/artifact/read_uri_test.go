package artifact

import "testing"

func TestReadURIFormatsAndParsesRootRelativeArtifactPath(t *testing.T) {
	want := "threads/123456/spool/call-1.txt"
	uri, err := FormatReadURI(want)
	if err != nil {
		t.Fatal(err)
	}
	if uri != "artifact://"+want {
		t.Fatalf("FormatReadURI() = %q, want %q", uri, "artifact://"+want)
	}
	got, recognized, err := ParseReadURI(uri)
	if err != nil || !recognized || got != want {
		t.Fatalf("ParseReadURI() = (%q, %t, %v), want (%q, true, nil)", got, recognized, err, want)
	}
}

func TestParseReadURIRejectsUnsafeArtifactPath(t *testing.T) {
	if _, recognized, err := ParseReadURI("artifact://../outside.txt"); !recognized || err == nil {
		t.Fatalf("ParseReadURI() recognized=%t err=%v, want recognized unsafe reference", recognized, err)
	}
	if _, recognized, err := ParseReadURI("workspace.txt"); recognized || err != nil {
		t.Fatalf("ParseReadURI() recognized=%t err=%v, want unrelated path", recognized, err)
	}
}

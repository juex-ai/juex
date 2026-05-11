package netbootstrap

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestParseResolvConf_Empty(t *testing.T) {
	if got := parseResolvConf(nil); got != nil {
		t.Fatalf("nil body: got %v, want nil", got)
	}
	if got := parseResolvConf([]byte("")); got != nil {
		t.Fatalf("empty body: got %v, want nil", got)
	}
}

func TestParseResolvConf_SingleIPv4(t *testing.T) {
	got := parseResolvConf([]byte("nameserver 1.1.1.1\n"))
	want := []string{"1.1.1.1:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseResolvConf_MultipleNameservers(t *testing.T) {
	body := "nameserver 1.1.1.1\nnameserver 8.8.8.8\n"
	got := parseResolvConf([]byte(body))
	want := []string{"1.1.1.1:53", "8.8.8.8:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseResolvConf_IPv6(t *testing.T) {
	got := parseResolvConf([]byte("nameserver 2606:4700:4700::1111\n"))
	want := []string{"[2606:4700:4700::1111]:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseResolvConf_SkipsCommentsAndUnrelatedDirectives(t *testing.T) {
	body := `# this is a comment
; semicolon comment
search example.com
options ndots:1
nameserver 9.9.9.9
`
	got := parseResolvConf([]byte(body))
	want := []string{"9.9.9.9:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseResolvConf_SkipsMalformedLines(t *testing.T) {
	body := "nameserver\nnameserver  \nnameserver 1.1.1.1 extra\nnameserver 8.8.8.8\n"
	got := parseResolvConf([]byte(body))
	// "nameserver 1.1.1.1 extra" is treated as a single token after "nameserver"
	// so we accept the first token only.
	want := []string{"1.1.1.1:53", "8.8.8.8:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseResolvConf_TolerantWhitespace(t *testing.T) {
	body := "  nameserver\t1.1.1.1\r\n\tnameserver  8.8.8.8\n"
	got := parseResolvConf([]byte(body))
	want := []string{"1.1.1.1:53", "8.8.8.8:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// findNameservers tests use the envProbe injection seam.

func mkProbe(files map[string]string, env map[string]string) envProbe {
	return envProbe{
		stat: func(name string) (os.FileInfo, error) {
			if _, ok := files[name]; ok {
				return fakeStat{name: name}, nil
			}
			return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		},
		readFile: func(name string) ([]byte, error) {
			body, ok := files[name]
			if !ok {
				return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
			}
			return []byte(body), nil
		},
		getenv: func(k string) string { return env[k] },
	}
}

type fakeStat struct{ name string }

func (f fakeStat) Name() string       { return f.name }
func (f fakeStat) Size() int64        { return 0 }
func (f fakeStat) Mode() os.FileMode  { return 0 }
func (f fakeStat) ModTime() time.Time { return time.Time{} }
func (f fakeStat) IsDir() bool        { return false }
func (f fakeStat) Sys() any           { return nil }

func TestFindNameservers_SystemResolvConfPresent_ReturnsNil(t *testing.T) {
	p := mkProbe(
		map[string]string{"/etc/resolv.conf": "nameserver 1.1.1.1\n"},
		nil,
	)
	if got := findNameservers(p); got != nil {
		t.Fatalf("expected nil when /etc/resolv.conf exists, got %v", got)
	}
}

func TestFindNameservers_JuexDNSEnvVar_BareIP(t *testing.T) {
	p := mkProbe(nil, map[string]string{"JUEX_DNS": "1.1.1.1"})
	got := findNameservers(p)
	want := []string{"1.1.1.1:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindNameservers_JuexDNSEnvVar_IPWithPort(t *testing.T) {
	p := mkProbe(nil, map[string]string{"JUEX_DNS": "1.1.1.1:5353"})
	got := findNameservers(p)
	want := []string{"1.1.1.1:5353"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindNameservers_JuexDNSEnvVar_CommaSeparated(t *testing.T) {
	p := mkProbe(nil, map[string]string{"JUEX_DNS": "1.1.1.1, 8.8.8.8:53 ,9.9.9.9"})
	got := findNameservers(p)
	want := []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindNameservers_JuexDNSEnvVar_IPv6(t *testing.T) {
	p := mkProbe(nil, map[string]string{"JUEX_DNS": "2606:4700:4700::1111"})
	got := findNameservers(p)
	want := []string{"[2606:4700:4700::1111]:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindNameservers_TermuxPrefix(t *testing.T) {
	prefix := "/data/data/com.termux/files/usr"
	p := mkProbe(
		map[string]string{prefix + "/etc/resolv.conf": "nameserver 8.8.4.4\n"},
		map[string]string{"PREFIX": prefix},
	)
	got := findNameservers(p)
	want := []string{"8.8.4.4:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindNameservers_NoSignal_ReturnsNil(t *testing.T) {
	p := mkProbe(nil, nil)
	if got := findNameservers(p); got != nil {
		t.Fatalf("expected nil with no signal, got %v", got)
	}
}

func TestFindNameservers_JuexDNSWinsOverPrefix(t *testing.T) {
	prefix := "/data/data/com.termux/files/usr"
	p := mkProbe(
		map[string]string{prefix + "/etc/resolv.conf": "nameserver 8.8.4.4\n"},
		map[string]string{"PREFIX": prefix, "JUEX_DNS": "1.0.0.1"},
	)
	got := findNameservers(p)
	want := []string{"1.0.0.1:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestApplyResolver_Empty_DoesNotMutate(t *testing.T) {
	r := &net.Resolver{}
	applyResolver(r, nil)
	if r.PreferGo {
		t.Fatal("PreferGo should be false on empty input")
	}
	if r.Dial != nil {
		t.Fatal("Dial should be nil on empty input")
	}
}

func TestApplyResolver_NonEmpty_SetsPreferGoAndDial(t *testing.T) {
	r := &net.Resolver{}
	applyResolver(r, []string{"1.1.1.1:53"})
	if !r.PreferGo {
		t.Fatal("PreferGo should be true after install")
	}
	if r.Dial == nil {
		t.Fatal("Dial should be non-nil after install")
	}
}

// recordingDialer captures the (network, addr) pairs each call receives
// and returns the configured (conn,err) tuple from a script.
type recordingDialer struct {
	calls   []struct{ network, addr string }
	results []dialResult
}

type dialResult struct {
	conn net.Conn
	err  error
}

func (d *recordingDialer) dial(_ context.Context, network, addr string) (net.Conn, error) {
	d.calls = append(d.calls, struct{ network, addr string }{network, addr})
	if len(d.results) == 0 {
		return nil, errors.New("recordingDialer: no scripted result")
	}
	r := d.results[0]
	d.results = d.results[1:]
	return r.conn, r.err
}

func TestFallbackDial_PassesNetworkThrough(t *testing.T) {
	rec := &recordingDialer{results: []dialResult{{nil, nil}}}
	dial := makeFallbackDial([]string{"1.1.1.1:53"}, rec.dial)
	if _, err := dial(context.Background(), "tcp", ""); err != nil {
		t.Fatalf("dial err: %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0].network != "tcp" {
		t.Fatalf("expected one call with network=tcp, got %#v", rec.calls)
	}
}

func TestFallbackDial_RoundRobinAcrossCalls(t *testing.T) {
	servers := []string{"a:53", "b:53", "c:53"}
	rec := &recordingDialer{results: []dialResult{{nil, nil}, {nil, nil}, {nil, nil}}}
	dial := makeFallbackDial(servers, rec.dial)
	for i := 0; i < 3; i++ {
		if _, err := dial(context.Background(), "udp", ""); err != nil {
			t.Fatalf("call %d err: %v", i, err)
		}
	}
	got := []string{rec.calls[0].addr, rec.calls[1].addr, rec.calls[2].addr}
	want := []string{"a:53", "b:53", "c:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-robin order: got %v, want %v", got, want)
	}
}

func TestFallbackDial_RetriesEachServerInOrder(t *testing.T) {
	// First two servers fail, third succeeds. Within one Dial call we
	// should see attempts in fixed offset+i order, not skipping any.
	servers := []string{"a:53", "b:53", "c:53"}
	rec := &recordingDialer{
		results: []dialResult{
			{nil, errors.New("a down")},
			{nil, errors.New("b down")},
			{nil, nil},
		},
	}
	dial := makeFallbackDial(servers, rec.dial)
	if _, err := dial(context.Background(), "udp", ""); err != nil {
		t.Fatalf("expected success on third attempt, got %v", err)
	}
	got := []string{rec.calls[0].addr, rec.calls[1].addr, rec.calls[2].addr}
	want := []string{"a:53", "b:53", "c:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry order: got %v, want %v", got, want)
	}
}

func TestFallbackDial_AllFailReturnsLastErr(t *testing.T) {
	servers := []string{"a:53", "b:53"}
	rec := &recordingDialer{
		results: []dialResult{
			{nil, errors.New("a")},
			{nil, errors.New("b last")},
		},
	}
	dial := makeFallbackDial(servers, rec.dial)
	_, err := dial(context.Background(), "udp", "")
	if err == nil || err.Error() != "b last" {
		t.Fatalf("expected last error 'b last', got %v", err)
	}
}

func TestInstall_IsIdempotent(t *testing.T) {
	// Install twice; should not panic. We don't assert global state here
	// because tests must not mutate net.DefaultResolver in ways that break
	// later parallel tests. A real call site exercises Install at startup.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Install panicked: %v", r)
		}
	}()
	// Just call findNameservers + applyResolver against a local resolver
	// twice — covers the same code path Install would.
	r := &net.Resolver{}
	applyResolver(r, []string{"1.1.1.1:53"})
	applyResolver(r, []string{"1.1.1.1:53"})
}

// errProbe verifies that a stat error other than NotExist is treated the
// same as missing — we don't want a permission error to spuriously skip
// fallback installation.
func TestFindNameservers_StatError_TreatedAsMissing(t *testing.T) {
	p := envProbe{
		stat: func(name string) (os.FileInfo, error) {
			return nil, errors.New("boom")
		},
		readFile: func(name string) ([]byte, error) {
			return nil, errors.New("nope")
		},
		getenv: func(k string) string {
			if k == "JUEX_DNS" {
				return "1.1.1.1"
			}
			return ""
		},
	}
	got := findNameservers(p)
	want := []string{"1.1.1.1:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

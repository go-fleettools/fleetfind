package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mkZip(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zr(t testing.TB, b []byte) *zip.Reader {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const pfx = "github.com/go-statistics/stats@v1.2.3/"

func TestReadZipFindsFilesAndExportedIdentifiers(t *testing.T) {
	b := mkZip(t, map[string]string{
		pfx + "quantile.go":      "package stats\nfunc Quantile(x []float64, p float64) float64 { return 0 }\nfunc helper() {}\ntype Moments struct{}\nfunc (m Moments) Variance() float64 { return 0 }\nconst Pi = 3\nvar ErrEmpty error\n",
		pfx + "quantile_test.go": "package stats\nfunc TestOnlyInTests() {}\n",
		pfx + "testdata/gen.go":  "package x\nfunc FromTestdata() {}\n",
		pfx + "sub/chi2.go":      "package sub\nfunc Chi2() {}\n",
		pfx + "README.md":        "# stats\n",
	})
	files, idents, trunc, err := readZip(zr(t, b), pfx)
	if err != nil || trunc {
		t.Fatalf("err=%v truncated=%v", err, trunc)
	}
	joined := strings.Join(idents, " ")
	for _, want := range []string{"Quantile", "Moments", "Variance", "Pi", "ErrEmpty", "Chi2"} {
		if !strings.Contains(" "+joined+" ", " "+want+" ") {
			t.Errorf("identifier %s missing from %v", want, idents)
		}
	}
	for _, not := range []string{"helper", "TestOnlyInTests", "FromTestdata"} {
		if strings.Contains(joined, not) {
			t.Errorf("%s must not be indexed: %v", not, idents)
		}
	}
	if !strings.Contains(strings.Join(files, " "), "quantile.go") || !strings.Contains(strings.Join(files, " "), "sub/chi2.go") {
		t.Errorf("files %v", files)
	}
}

func TestCleanNameRefusesAnythingThatCouldEscape(t *testing.T) {
	for _, bad := range []string{
		"../evil.go", "a/../../evil.go", "/abs.go", "a//b.go", `a\b.go`, "a/./b.go",
		"nul\x00.go", "ctl\x07.go", "dir/", "", "bad\xff.go", strings.Repeat("x", maxNameLen+1),
	} {
		if rel, ok := cleanName(pfx+bad, pfx); ok {
			t.Errorf("cleanName accepted %q as %q", bad, rel)
		}
	}
	if _, ok := cleanName("other@v1/a.go", pfx); ok {
		t.Error("a name outside the prefix was accepted")
	}
	if rel, ok := cleanName(pfx+"a/b.go", pfx); !ok || rel != "a/b.go" {
		t.Errorf("a plain name was refused: %q %v", rel, ok)
	}
}

func TestAnEntryOutsideTheModulePrefixRefusesTheArchive(t *testing.T) {
	b := mkZip(t, map[string]string{pfx + "a.go": "package a", "../escape.go": "package a"})
	if _, _, _, err := readZip(zr(t, b), pfx); err == nil {
		t.Fatal("a zip with an entry outside the module prefix was read")
	}
}

// ⛔ Bomb tests assert a REFUSAL. The compressed archives are a few KiB; what
// they declare, or would inflate to, is far past the bounds.
func TestBombZipIsRefusedNotInflated(t *testing.T) {
	// 200 MiB of zeros in one file: compresses to ~200 KiB.
	big := strings.Repeat("\x00", 200<<20)
	b := mkZip(t, map[string]string{pfx + "bomb.go": big})
	if len(b) > 1<<20 {
		t.Fatalf("test archive is %d bytes, not a bomb", len(b))
	}
	_, _, _, err := readZip(zr(t, b), pfx)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a 200 MiB declared archive was not refused: %v", err)
	}
}

func TestManyEntriesAreRefused(t *testing.T) {
	m := map[string]string{}
	for i := 0; i < maxEntries+1; i++ {
		m[fmt.Sprintf("%sf%d.txt", pfx, i)] = ""
	}
	_, _, _, err := readZip(zr(t, mkZip(t, m)), pfx)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("an archive of %d entries was not refused: %v", maxEntries+1, err)
	}
}

func TestAnOversizeGoFileIsSkippedAndSaidSo(t *testing.T) {
	b := mkZip(t, map[string]string{pfx + "gen.go": "package a\nfunc Big() {}\n//" + strings.Repeat("x", maxGoFile)})
	_, idents, trunc, err := readZip(zr(t, b), pfx)
	if err != nil || !trunc || len(idents) != 0 {
		t.Errorf("err=%v truncated=%v idents=%v", err, trunc, idents)
	}
}

func FuzzCleanName(f *testing.F) {
	for _, s := range []string{"a.go", "../x", "a//b", "\x00", "a/b/c.go", `a\b`, "é.go", "/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		rel, ok := cleanName(pfx+name, pfx)
		if !ok {
			return
		}
		if strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") && hasDotDotSegment(rel) || strings.ContainsAny(rel, "\\\x00") {
			t.Fatalf("accepted unsafe name %q -> %q", name, rel)
		}
		if filepath.IsAbs(rel) || rel == "" {
			t.Fatalf("accepted %q -> %q", name, rel)
		}
	})
}

func hasDotDotSegment(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return true
		}
	}
	return false
}

// FuzzReadZip feeds arbitrary bytes as an archive: whatever they are, reading
// must return an error or names, never panic, and never name an escaping path.
func FuzzReadZip(f *testing.F) {
	f.Add(mkZip(f, map[string]string{pfx + "a.go": "package a\nfunc A() {}\n", pfx + "b/c.go": "package"}))
	f.Add(mkZip(f, map[string]string{"../x.go": "package a"}))
	f.Add([]byte("PK\x05\x06" + strings.Repeat("\x00", 18)))
	f.Add([]byte("not a zip"))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		files, _, _, err := readZip(r, pfx)
		if err != nil {
			return
		}
		for _, n := range files {
			if strings.HasPrefix(n, "/") || hasDotDotSegment(n) || strings.ContainsAny(n, "\\\x00") {
				t.Fatalf("escaping name %q came back", n)
			}
		}
	})
}

// ---- the proxy, against a local server ------------------------------------

func fakeProxy(t *testing.T, zipBytes []byte, calls *atomic.Int64) *proxy {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/@latest") && strings.Contains(r.URL.Path, "go-statistics/stats"):
			fmt.Fprint(w, `{"Version":"v1.2.3"}`)
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.Write(zipBytes)
		case strings.Contains(r.URL.Path, "flaky"):
			http.Error(w, "boom", 503)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &proxy{base: srv.URL, client: srv.Client(), maxZip: 1 << 20, backoff: time.Millisecond}
}

func TestProxyModuleIndexesAndDistinguishesAbsentFromUnread(t *testing.T) {
	z := mkZip(t, map[string]string{pfx + "quantile.go": "package stats\nfunc Quantile() {}\n"})
	p := fakeProxy(t, z, nil)

	e := p.module("go-statistics", "stats")
	if e.Status != statusOK || e.Version != "v1.2.3" || len(e.Files) != 1 || e.Idents[0] != "Quantile" {
		t.Errorf("got %+v", e)
	}
	if e := p.module("go-docs", "site"); e.Status != statusAbsent {
		t.Errorf("a 404 is a repository that is not a module, got %+v", e)
	}
	// ⛔ A 5xx is NOT "absent": it is a repository we could not read.
	if e := p.module("go-x", "flaky"); e.Status != statusUnread {
		t.Errorf("a 503 was reported as %+v", e)
	}
}

func TestProxyRefusesAnOversizeZip(t *testing.T) {
	z := mkZip(t, map[string]string{pfx + "a.go": "package a\n" + strings.Repeat("// pad\n", 50000)})
	p := fakeProxy(t, z, nil)
	p.maxZip = 64 // far below the archive
	e := p.module("go-statistics", "stats")
	if e.Status != statusUnread || !strings.Contains(e.Reason, "refused: zip over") {
		t.Errorf("an oversize zip was not refused: %+v", e)
	}
}

func TestProxyRefusesAZipServedWithoutEndOrLength(t *testing.T) {
	// A server streaming forever: the limit reader stops it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/@latest") {
			fmt.Fprint(w, `{"Version":"v1.0.0"}`)
			return
		}
		chunk := make([]byte, 64<<10)
		for i := 0; i < 1000; i++ { // 64 MiB offered
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	p := &proxy{base: srv.URL, client: srv.Client(), maxZip: 1 << 20, backoff: time.Millisecond}
	if e := p.module("a", "b"); e.Status != statusUnread || !strings.Contains(e.Reason, "refused") {
		t.Errorf("an endless body was not refused: %+v", e)
	}
}

func TestEscapePathIsTheProxysCaseEncoding(t *testing.T) {
	if got := escapePath("github.com/Azure/go-X"); got != "github.com/!azure/go-!x" {
		t.Errorf("got %s", got)
	}
}

// ---- incremental, honest -----------------------------------------------------

func TestBuildIndexOnlyReadsWhatChanged(t *testing.T) {
	rs := []repo{
		{Org: "a", Name: "same", Pushed: "p1", Lang: "Go"},
		{Org: "a", Name: "moved", Pushed: "p2", Lang: "Go"},
		{Org: "a", Name: "failed", Pushed: "p1", Lang: "Go"},
		{Org: "a", Name: "docs", Pushed: "p1", Lang: "Markdown"},
		{Org: "a", Name: "new", Pushed: "p1"},
	}
	old := &index{Entries: map[string]entry{
		"a/same":   {Pushed: "p1", Status: statusOK},
		"a/moved":  {Pushed: "p1", Status: statusOK},
		"a/failed": {Pushed: "p1", Status: statusUnread, Reason: "proxy: timeout"},
	}}
	var read []string
	fetch := func(org, name string) entry {
		read = append(read, name)
		return entry{Status: statusOK}
	}
	x, st := buildIndex(old, rs, fetch, 1, nil)
	if st.Reused != 1 || st.Fetched != 3 || st.NotGo != 1 {
		t.Errorf("stats %+v", st)
	}
	got := strings.Join(read, ",")
	for _, want := range []string{"moved", "failed", "new"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was not read: %s", want, got)
		}
	}
	if strings.Contains(got, "same") || strings.Contains(got, "docs") {
		t.Errorf("read what it should not have: %s", got)
	}
	if x.Entries["a/moved"].Pushed != "p2" {
		t.Errorf("the new pushed_at was not recorded")
	}
}

func TestACoverageNeverDropsAnUnreadRepositoryFromTheDenominator(t *testing.T) {
	rs := []repo{
		{Org: "a", Name: "ok", Lang: "Go"}, {Org: "a", Name: "abs", Lang: "Go"},
		{Org: "a", Name: "bad", Lang: "Go"}, {Org: "a", Name: "never", Lang: "Go"},
		{Org: "a", Name: "md", Lang: "Markdown"},
	}
	x := &index{Entries: map[string]entry{
		"a/ok": {Status: statusOK}, "a/abs": {Status: statusAbsent},
		"a/bad": {Status: statusUnread, Reason: "proxy: timeout"},
	}}
	c := measure(rs, x)
	if c.Indexed != 1 || c.Absent != 1 || c.Unread != 2 || c.NotGo != 1 || c.Total != 5 {
		t.Errorf("%+v", c)
	}
	// With no index at all, every Go repository is unread, not "zero found".
	if c := measure(rs, nil); c.Unread != 4 || c.Indexed != 0 {
		t.Errorf("no index: %+v", c)
	}
}

func TestZeroIsNeverReportedAsANoWhenPartWasNotRead(t *testing.T) {
	c := &cache{Taken: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC), Repos: make([]repo, 2253)}
	x := &index{}
	msg := zeroMessage(c, x, coverage{Indexed: 1500, Absent: 600, Unread: 12, Total: 2253}, "quantile")
	if strings.HasPrefix(msg, "nothing in 2253") || !strings.Contains(msg, "12 could NOT be read") || !strings.Contains(msg, "not a NO") {
		t.Errorf("a partial read said %q", msg)
	}
	if msg := zeroMessage(c, nil, coverage{}, "quantile"); strings.HasPrefix(msg, "nothing in 2253") || !strings.Contains(msg, "no file or identifier index") {
		t.Errorf("no index said %q", msg)
	}
	if msg := zeroMessage(c, x, coverage{Indexed: 1500, Absent: 700, Total: 2253}, "quantile"); !strings.Contains(msg, "files and identifiers were read for 1500") {
		t.Errorf("a complete read said %q", msg)
	}
}

func TestSearchFindsAFunctionNameThatNoDescriptionSays(t *testing.T) {
	rs := []repo{
		{Org: "go-statistics", Name: "stats", Desc: "descriptive statistics", Lang: "Go"},
		{Org: "go-odf", Name: "odf", Desc: "ODT converter", Lang: "Go"},
	}
	x := &index{Entries: map[string]entry{
		"go-statistics/stats": {Status: statusOK, Files: []string{"quantile.go", "moments.go"}, Idents: []string{"Quantile", "Variance"}},
		"go-odf/odf":          {Status: statusOK, Files: []string{"odf.go"}, Idents: []string{"Convert"}},
	}}
	hits := searchIndex(rs, x, []string{"quantile"}, false)
	if len(hits) != 1 || hits[0].r.Name != "stats" {
		t.Fatalf("hits %v", names(hits))
	}
	var out bytes.Buffer
	render(&out, hits)
	for _, want := range []string{"go-statistics/stats", "file:quantile.go", "ident:Quantile"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// Without the index the same question is the old name/description match: nothing.
	if got := search(rs, []string{"quantile"}, false); len(got) != 0 {
		t.Errorf("name/description search found %v", names(got))
	}
	// Terms narrow across all four sources.
	if got := searchIndex(rs, x, []string{"statistics", "quantile"}, false); len(got) != 1 {
		t.Errorf("path + identifier gave %v", names(got))
	}
	if got := searchIndex(rs, x, []string{"statistics", "convert"}, false); len(got) != 0 {
		t.Errorf("terms from different repositories matched: %v", names(got))
	}
	// An unread entry contributes nothing, rather than something wrong.
	x.Entries["go-statistics/stats"] = entry{Status: statusUnread}
	if got := searchIndex(rs, x, []string{"quantile"}, false); len(got) != 0 {
		t.Errorf("an unread entry matched: %v", names(got))
	}
}

func TestTheIndexSurvivesBeingWrittenAndReadBack(t *testing.T) {
	at := indexPath(filepath.Join(t.TempDir(), "deep", "inventory.json"))
	want := &index{Taken: time.Now().Truncate(time.Second), Entries: map[string]entry{"a/b": {Pushed: "p", Status: statusOK, Files: []string{"x.go"}}}}
	if err := saveIndex(at, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadIndex(at)
	if err != nil || got.Entries["a/b"].Files[0] != "x.go" || !got.Taken.Equal(want.Taken) {
		t.Errorf("%v %+v", err, got)
	}
}

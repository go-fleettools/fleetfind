package main

// The file and symbol index.
//
// ⛔ Why it exists (2026-11-15, go-fleettools/fleettools#306): `fleetfind
// quantile` printed "nothing in 2253 repositories" while go-statistics/stats
// has quantile.go. A repository's name and description name its DOMAIN
// ("descriptive statistics"), never its functions, so a question phrased as a
// function name could not be answered from them — and go-embedded-r rewrote
// kernels that already existed.
//
// The source is the Go module proxy, not GitHub: `@latest` for the version,
// then the module zip, of which only NAMES and exported top-level identifiers
// are read. That costs no REST budget at all (the 5000 an hour are shared by
// ~25 sessions and Renovate), and the index is incremental on `pushed_at`.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bounds. Every one of them is a REFUSAL, never a silent truncation of what is
// reported as read: a module over a bound is recorded as unread, with the bound.
const (
	proxyBase      = "https://proxy.golang.org"
	defaultMaxZip  = 32 << 20 // compressed bytes downloaded per module
	maxEntries     = 20000    // files in one zip
	maxUncompress  = 128 << 20
	maxGoFile      = 1 << 20 // a larger .go file is skipped and the module marked truncated
	maxNames       = 4000
	maxIdents      = 4000
	maxJSON        = 1 << 20
	maxNameLen     = 512
	defaultWorkers = 8
)

// Status of one repository in the index.
const (
	statusOK     = "ok"     // zip read
	statusAbsent = "absent" // the proxy has no module at github.com/org/repo (not a module, private, or unpublished)
	statusUnread = "unread" // we could not read it: network, 5xx, refusal over a bound
)

type entry struct {
	Pushed    string   `json:"pushed"`
	Version   string   `json:"version,omitempty"`
	Status    string   `json:"status"`
	Reason    string   `json:"reason,omitempty"`
	Files     []string `json:"files,omitempty"`
	Idents    []string `json:"idents,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

type index struct {
	Taken   time.Time        `json:"taken"`
	Entries map[string]entry `json:"entries"`
}

func indexPath(cachePath string) string {
	return filepath.Join(filepath.Dir(cachePath), "index.json")
}

func saveIndex(at string, x *index) error {
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	tmp := at + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, at)
}

func loadIndex(at string) (*index, error) {
	b, err := os.ReadFile(at)
	if err != nil {
		return nil, err
	}
	var x index
	if err := json.Unmarshal(b, &x); err != nil {
		return nil, err
	}
	return &x, nil
}

// ---- names and identifiers out of a module zip ----------------------------

// cleanName turns a zip entry name into a path relative to the module root, or
// reports ok=false. Names are only ever READ here, never used to write a file,
// but the same rules a zip-slip defence applies are applied anyway: a name
// that could escape a directory is never stored, so no later consumer can be
// tricked by it.
func cleanName(name, prefix string) (rel string, ok bool) {
	if !strings.HasPrefix(name, prefix) || len(name) > maxNameLen+len(prefix) {
		return "", false
	}
	rel = name[len(prefix):]
	if rel == "" || strings.HasSuffix(rel, "/") || !utf8.ValidString(rel) {
		return "", false
	}
	if strings.ContainsAny(rel, "\\\x00") || strings.HasPrefix(rel, "/") {
		return "", false
	}
	for _, r := range rel {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return rel, true
}

var errOutside = errors.New("zip entry outside the module prefix")

// readZip lists file names and the exported top-level identifiers of the
// non-test Go files. It refuses (returns an error) on a zip that exceeds a
// bound; it never reads more than maxUncompress bytes in total, whatever the
// archive DECLARES, because a declared size is the attacker's word.
func readZip(zr *zip.Reader, prefix string) (files, idents []string, truncated bool, err error) {
	if len(zr.File) > maxEntries {
		return nil, nil, false, fmt.Errorf("refused: %d entries, over %d", len(zr.File), maxEntries)
	}
	var declared uint64
	for _, f := range zr.File {
		declared += f.UncompressedSize64
		if declared > maxUncompress {
			return nil, nil, false, fmt.Errorf("refused: declares over %d MiB uncompressed", maxUncompress>>20)
		}
	}
	seenIdent := map[string]bool{}
	budget := int64(maxUncompress)
	fset := token.NewFileSet()
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			return nil, nil, false, errOutside
		}
		rel, ok := cleanName(f.Name, prefix)
		if !ok {
			continue
		}
		if len(files) < maxNames {
			files = append(files, rel)
		} else {
			truncated = true
		}
		if !wantsParse(rel) {
			continue
		}
		if f.UncompressedSize64 > maxGoFile {
			truncated = true
			continue
		}
		rc, err := f.Open()
		if err != nil {
			truncated = true
			continue
		}
		src, err := io.ReadAll(io.LimitReader(rc, maxGoFile+1))
		rc.Close()
		budget -= int64(len(src))
		if budget < 0 {
			return nil, nil, false, fmt.Errorf("refused: read over %d MiB", maxUncompress>>20)
		}
		if err != nil || len(src) > maxGoFile {
			truncated = true
			continue
		}
		af, _ := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if af == nil {
			truncated = true
			continue
		}
		for _, id := range exported(af) {
			if seenIdent[id] {
				continue
			}
			if len(idents) >= maxIdents {
				truncated = true
				break
			}
			seenIdent[id] = true
			idents = append(idents, id)
		}
	}
	sort.Strings(idents)
	return files, idents, truncated, nil
}

func wantsParse(rel string) bool {
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "testdata" || seg == "vendor" {
			return false
		}
	}
	return true
}

// exported returns the exported package-level names a file declares: funcs,
// methods, types, constants and variables. No type checking.
func exported(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Name.IsExported() {
				out = append(out, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						out = append(out, s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							out = append(out, n.Name)
						}
					}
				}
			}
		}
	}
	return out
}

// ---- the proxy -------------------------------------------------------------

// escapePath is the proxy's case encoding: an upper-case letter is "!" and its
// lower-case form.
func escapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type proxy struct {
	base    string
	client  *http.Client
	maxZip  int64
	backoff time.Duration
}

func newProxy(maxZip int64) *proxy {
	return &proxy{base: proxyBase, client: &http.Client{Timeout: 90 * time.Second}, maxZip: maxZip, backoff: time.Second}
}

// get fetches url, retrying twice on a network error, 429 or 5xx. The body is
// read through a limit of max+1 bytes; the caller sees over=true when it was
// cut, which is a refusal, not a short read.
func (p *proxy) get(url string, max int64) (body []byte, status int, over bool, err error) {
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(p.backoff * time.Duration(try))
		}
		resp, e := p.client.Get(url)
		if e != nil {
			err = e
			continue
		}
		if resp.ContentLength > max {
			resp.Body.Close()
			return nil, resp.StatusCode, true, nil
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, max+1))
		resp.Body.Close()
		if e != nil {
			err = e
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			err = fmt.Errorf("proxy HTTP %d", resp.StatusCode)
			status = resp.StatusCode
			continue
		}
		return b, resp.StatusCode, int64(len(b)) > max, nil
	}
	return nil, status, false, err
}

// module reads one repository's module at github.com/org/name.
func (p *proxy) module(org, name string) entry {
	mod := "github.com/" + org + "/" + name
	base := p.base + "/" + escapePath(mod)
	b, st, _, err := p.get(base+"/@latest", maxJSON)
	switch {
	case err != nil:
		return entry{Status: statusUnread, Reason: "proxy: " + short(err)}
	case st == 404 || st == 410:
		return entry{Status: statusAbsent, Reason: "not in the proxy"}
	case st != 200:
		return entry{Status: statusUnread, Reason: fmt.Sprintf("proxy HTTP %d on @latest", st)}
	}
	var info struct{ Version string }
	if json.Unmarshal(b, &info) != nil || info.Version == "" {
		return entry{Status: statusUnread, Reason: "proxy answered @latest without a version"}
	}
	zb, st, over, err := p.get(base+"/@v/"+escapePath(info.Version)+".zip", p.maxZip)
	switch {
	case err != nil:
		return entry{Status: statusUnread, Version: info.Version, Reason: "zip: " + short(err)}
	case over:
		return entry{Status: statusUnread, Version: info.Version, Reason: fmt.Sprintf("refused: zip over %d MiB", p.maxZip>>20)}
	case st != 200:
		return entry{Status: statusUnread, Version: info.Version, Reason: fmt.Sprintf("proxy HTTP %d on the zip", st)}
	}
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		return entry{Status: statusUnread, Version: info.Version, Reason: "zip: not an archive"}
	}
	files, idents, trunc, err := readZip(zr, mod+"@"+info.Version+"/")
	if err != nil {
		return entry{Status: statusUnread, Version: info.Version, Reason: err.Error()}
	}
	return entry{Status: statusOK, Version: info.Version, Files: files, Idents: idents, Truncated: trunc}
}

// short keeps a reason groupable: no URLs, no addresses.
func short(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// ---- building --------------------------------------------------------------

// goish reports whether a repository could be a Go module from its primary
// language. Unknown (an inventory taken before languages were kept) is "maybe".
func goish(lang string) bool {
	switch lang {
	case "", "Go", "Assembly", "C", "C++":
		return true
	}
	return false
}

type fetchFn func(org, name string) entry

type buildStats struct {
	Reused, Fetched, NotGo int
}

// buildIndex reads every repository that is not already current. A repository
// is current when the index holds a non-unread entry for the same pushed_at;
// an unread one is always tried again.
func buildIndex(old *index, rs []repo, fetch fetchFn, workers int, progress io.Writer) (*index, buildStats) {
	x := &index{Taken: time.Now(), Entries: map[string]entry{}}
	var st buildStats
	type job struct {
		key string
		r   repo
	}
	var todo []job
	for _, r := range rs {
		if !goish(r.Lang) {
			st.NotGo++
			continue
		}
		key := r.Org + "/" + r.Name
		if old != nil {
			if e, ok := old.Entries[key]; ok && e.Status != statusUnread && e.Pushed == r.Pushed {
				x.Entries[key] = e
				st.Reused++
				continue
			}
		}
		todo = append(todo, job{key, r})
	}
	if workers < 1 {
		workers = 1
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	ch := make(chan job)
	done := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				e := fetch(j.r.Org, j.r.Name)
				e.Pushed = j.r.Pushed
				mu.Lock()
				x.Entries[j.key] = e
				done++
				if progress != nil && done%25 == 0 {
					fmt.Fprintf(progress, "\rindex %d/%d", done, len(todo))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range todo {
		ch <- j
	}
	close(ch)
	wg.Wait()
	if progress != nil {
		fmt.Fprintln(progress)
	}
	st.Fetched = len(todo)
	return x, st
}

// coverage counts the inventory against the index. Nothing is dropped from the
// denominator: a repository that is Go-ish and has no entry is NOT READ.
type coverage struct {
	Total, NotGo, Indexed, Absent, Unread int
	Reasons                               map[string]int
}

func measure(rs []repo, x *index) coverage {
	c := coverage{Total: len(rs), Reasons: map[string]int{}}
	for _, r := range rs {
		if !goish(r.Lang) {
			c.NotGo++
			continue
		}
		var e entry
		ok := false
		if x != nil {
			e, ok = x.Entries[r.Org+"/"+r.Name]
		}
		switch {
		case !ok:
			c.Unread++
			c.Reasons["not attempted"]++
		case e.Status == statusOK:
			c.Indexed++
		case e.Status == statusAbsent:
			c.Absent++
		default:
			c.Unread++
			c.Reasons[e.Reason]++
		}
	}
	return c
}

func (c coverage) String() string {
	s := fmt.Sprintf("files and identifiers read for %d Go repositories; %d not in the module proxy (not a module, private or unpublished); %d UNREAD; %d not Go (skipped), of %d",
		c.Indexed, c.Absent, c.Unread, c.NotGo, c.Total)
	if len(c.Reasons) > 0 {
		var rs []string
		for k, v := range c.Reasons {
			rs = append(rs, fmt.Sprintf("%s ×%d", k, v))
		}
		sort.Strings(rs)
		s += " (unread: " + strings.Join(rs, "; ") + ")"
	}
	return s
}

// ---- searching it ----------------------------------------------------------

func contains(hay, needleLower string) bool {
	return strings.Contains(strings.ToLower(hay), needleLower)
}

// indexMatches returns what of one entry matches a term: files first, then
// identifiers, an exact identifier before a partial one. Capped so one broad
// word does not print a thousand lines.
func indexMatches(e entry, term string) (matches []string, score int) {
	t := strings.ToLower(term)
	var exact, part []string
	for _, f := range e.Files {
		if contains(filepath.Base(f), t) {
			matches = append(matches, "file:"+f)
			score = 5
			if len(matches) == maxShown {
				break
			}
		}
	}
	for _, id := range e.Idents {
		l := strings.ToLower(id)
		switch {
		case l == t:
			exact = append(exact, "ident:"+id)
		case strings.Contains(l, t):
			part = append(part, "ident:"+id)
		}
	}
	if len(exact) > 0 {
		score = max(score, 8)
	} else if len(part) > 0 {
		score = max(score, 4)
	}
	matches = append(matches, exact...)
	matches = append(matches, part...)
	if len(matches) > maxShown {
		matches = matches[:maxShown]
	}
	return matches, score
}

const maxShown = 4

// fleetfind answers "which repository already does this?" from LIVE data.
//
// ⛔ It exists because the hand-kept capability map drifts, and a drifted index
// is worse than none: it gives the false assurance of having looked. The map
// says so about itself, in bold, and it was still wrong about an org that had
// moved. Twice in three days a tool was rebuilt here that the fleet already
// had — once a mutation runner that had shipped five hours earlier, once very
// nearly an icon pack.
//
// So this keeps no opinions. It fetches every organisation's repositories,
// writes them to a dated cache, and every answer carries the date it was taken.
// A cache older than a day says so on stderr before it answers, because the
// failure mode being designed against is an index that reads as the present.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type repo struct {
	Org      string   `json:"org"`
	Name     string   `json:"name"`
	Desc     string   `json:"desc"`
	Archived bool     `json:"archived"`
	Pushed   string   `json:"pushed"`
	Topics   []string `json:"topics,omitempty"`
	Lang     string   `json:"lang,omitempty"`
}

type cache struct {
	Taken time.Time `json:"taken"`
	Orgs  int       `json:"orgs"`
	Repos []repo    `json:"repos"`
}

func main() {
	refresh := flag.Bool("refresh", false, "fetch every organisation's repositories again (about one call per organisation), then update the file and identifier index")
	reindex := flag.Bool("index", false, "update the file and identifier index from the Go module proxy (no GitHub calls; only repositories pushed since are read)")
	noIndex := flag.Bool("no-index", false, "with -refresh: do not update the file and identifier index")
	maxZip := flag.Int64("max-zip", defaultMaxZip>>20, "refuse a module zip larger than this many MiB")
	at := flag.String("cache", defaultCache(), "where the inventory is kept (the index sits beside it)")
	all := flag.Bool("all", false, "include archived repositories")
	flag.Parse()
	ix := indexPath(*at)

	if *refresh {
		start := time.Now()
		c, err := fetch()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleetfind:", err)
			os.Exit(1)
		}
		if err := save(*at, c); err != nil {
			fmt.Fprintln(os.Stderr, "fleetfind:", err)
			os.Exit(1)
		}
		fmt.Printf("%d repositories across %d organisations, taken %s (%d gh calls, %s)\n",
			len(c.Repos), c.Orgs, c.Taken.Format(time.RFC3339), ghCalls.Load(), time.Since(start).Round(time.Second))
		if !*noIndex {
			*reindex = true
		}
		if flag.NArg() == 0 && !*reindex {
			return
		}
	}

	if *reindex {
		c, err := load(*at)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleetfind:", err, "\n  run `fleetfind -refresh` first")
			os.Exit(1)
		}
		start := time.Now()
		old, _ := loadIndex(ix) // a missing or unreadable index is simply rebuilt
		p := newProxy(*maxZip << 20)
		x, st := buildIndex(old, c.Repos, p.module, defaultWorkers, os.Stderr)
		if err := saveIndex(ix, x); err != nil {
			fmt.Fprintln(os.Stderr, "fleetfind:", err)
			os.Exit(1)
		}
		fmt.Printf("index: %d read from the module proxy, %d already current (0 GitHub calls, %s)\n%s\n",
			st.Fetched, st.Reused, time.Since(start).Round(time.Second), measure(c.Repos, x))
		if flag.NArg() == 0 {
			return
		}
	}

	c, err := load(*at)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleetfind:", err, "\n  run `fleetfind -refresh` first")
		os.Exit(1)
	}
	x, xerr := loadIndex(ix)
	if xerr != nil {
		x = nil
		fmt.Fprintln(os.Stderr, "fleetfind: no file and identifier index — names and descriptions only; run `fleetfind -index`")
	}
	// ⛔ Said before the answer, not after it. An inventory that reads as the
	// present is the thing this was written to replace.
	if note := staleNote(c.Taken, time.Now()); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "fleetfind: give one or more words to look for")
		os.Exit(2)
	}

	hits := searchIndex(c.Repos, x, flag.Args(), *all)
	cov := measure(c.Repos, x)
	if len(hits) == 0 {
		// ⛔ Said out loud. A sweep that prints nothing is indistinguishable
		// from a sweep that could not read.
		fmt.Println(zeroMessage(c, x, cov, strings.Join(flag.Args(), " ")))
		return
	}
	render(os.Stdout, hits)
	fmt.Fprintf(os.Stderr, "\n%d of %d repositories, inventory of %s\n",
		len(hits), len(c.Repos), c.Taken.Format("2006-01-02"))
	if x != nil {
		fmt.Fprintf(os.Stderr, "%s\n", cov)
	}
}

// render prints one block per repository: the unchanged name-and-description
// line, then one `org/repo  file:…` or `org/repo  ident:…` line for each file
// or identifier that matched, so a grep for the repository finds both.
func render(w io.Writer, hits []hit) {
	for _, h := range hits {
		mark := ""
		if h.r.Archived {
			mark = "  [archived]"
		}
		path := h.r.Org + "/" + h.r.Name
		fmt.Fprintf(w, "%-34s %s%s\n", path, first(h.r.Desc, 92), mark)
		for _, m := range h.matches {
			fmt.Fprintf(w, "%-34s %s\n", path, m)
		}
	}
}

// zeroMessage is the answer to "nothing matched". ⛔ It states what was and was
// not READ: "nothing in 2253 repositories" is false when part of them could not
// be read, and a scan that could not read must not report zero.
func zeroMessage(c *cache, x *index, cov coverage, q string) string {
	date := c.Taken.Format("2006-01-02")
	if x == nil {
		return fmt.Sprintf("nothing in the names and descriptions of %d repositories (taken %s) matches %q; no file or identifier index was read (run `fleetfind -index`)",
			len(c.Repos), date, q)
	}
	if cov.Unread == 0 {
		return fmt.Sprintf("nothing in %d repositories (taken %s) matches %q; files and identifiers were read for %d Go repositories, %d are not in the module proxy",
			len(c.Repos), date, q, cov.Indexed, cov.Absent)
	}
	return fmt.Sprintf("nothing found for %q in the names and descriptions of %d repositories (taken %s) or in the files and identifiers of the %d Go repositories read — but %d could NOT be read, so this is not a NO.\n%s",
		q, len(c.Repos), date, cov.Indexed, cov.Unread, cov)
}

// stale is how old an inventory may be before an answer from it is worth
// doubting. A day, because the fleet gains repositories in an afternoon.
const stale = 24 * time.Hour

// staleNote is the warning, or "" when the inventory is fresh enough.
//
// ⛔ A function rather than four lines inside main, because this is the one
// thing the tool exists to say and nothing could reach it there: the mutation
// that disabled the warning survived the whole suite.
func staleNote(taken, now time.Time) string {
	age := now.Sub(taken)
	if age <= stale {
		return ""
	}
	return fmt.Sprintf("fleetfind: this inventory was taken %s ago (%s) — refresh before believing a NO",
		age.Round(time.Hour), taken.Format("2006-01-02"))
}

type hit struct {
	r       repo
	score   int
	matches []string // file:… and ident:… from the index
}

// search ranks a name match above a description match: an org called
// go-odf/ods is a stronger answer about spreadsheets than a sentence that
// happens to say "spreadsheet".
func search(rs []repo, terms []string, withArchived bool) []hit {
	return searchIndex(rs, nil, terms, withArchived)
}

// searchIndex is search plus the file and identifier index (nil = none). A
// term counts when it matches the path, the description or topics, a file
// name, or an exported identifier; every term must count. A file name scores 5,
// an identifier 4 (8 when exact), under a path's 10 and over a description's 3.
func searchIndex(rs []repo, x *index, terms []string, withArchived bool) []hit {
	var out []hit
	for _, r := range rs {
		if r.Archived && !withArchived {
			continue
		}
		path := strings.ToLower(r.Org + "/" + r.Name)
		desc := strings.ToLower(r.Desc + " " + strings.Join(r.Topics, " "))
		var e entry
		var haveEntry bool
		if x != nil {
			e, haveEntry = x.Entries[r.Org+"/"+r.Name]
			haveEntry = haveEntry && e.Status == statusOK
		}
		score, every := 0, true
		var matches []string
		for _, t := range terms {
			t = strings.ToLower(t)
			switch {
			case strings.Contains(path, t):
				score += 10
			case strings.Contains(desc, t):
				score += 3
			default:
				ms, s := []string(nil), 0
				if haveEntry {
					ms, s = indexMatches(e, t)
				}
				if s == 0 {
					every = false
					continue
				}
				score += s
				matches = append(matches, ms...)
			}
		}
		if every && score > 0 {
			out = append(out, hit{r, score, dedupe(matches)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].r.Org+out[i].r.Name < out[j].r.Org+out[j].r.Name
	})
	return out
}

func fetch() (*cache, error) {
	orgs, err := lines("api", "user/orgs", "--paginate", "-q", ".[].login")
	if err != nil {
		return nil, fmt.Errorf("listing organisations: %w", err)
	}
	if len(orgs) == 0 {
		return nil, fmt.Errorf("no organisations came back: reading none is not reading zero")
	}
	c := &cache{Taken: time.Now(), Orgs: len(orgs)}
	for i, org := range orgs {
		fmt.Fprintf(os.Stderr, "\r%d/%d %-40s", i+1, len(orgs), org)
		out, err := run("api", "orgs/"+org+"/repos?per_page=100", "--paginate",
			"-q", `.[] | {name, desc: (.description // ""), archived, pushed: .pushed_at, topics, lang: (.language // "")} | tostring`)
		if err != nil {
			continue // an organisation that will not list is not a reason to lose the rest
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			var r repo
			if json.Unmarshal([]byte(line), &r) != nil {
				continue
			}
			r.Org = org
			c.Repos = append(c.Repos, r)
		}
	}
	fmt.Fprintln(os.Stderr)
	if len(c.Repos) == 0 {
		return nil, fmt.Errorf("%d organisations listed and not one repository: the query shape changed", len(orgs))
	}
	return c, nil
}

// run is a variable so a test can answer for gh without one being installed:
// the ranking and the staleness warning are this tool's own behaviour, and
// neither should need a network or a token to be asserted.
var run = func(args ...string) (string, error) {
	// A --paginate call is several requests; this counts invocations, so it is
	// a lower bound on REST requests, and is printed as "gh calls".
	ghCalls.Add(1)
	out, err := exec.Command("gh", args...).Output()
	return string(out), err
}

func lines(args ...string) ([]string, error) {
	out, err := run(args...)
	if err != nil {
		return nil, err
	}
	var ls []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			ls = append(ls, l)
		}
	}
	return ls, nil
}

func defaultCache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "fleetfind.json"
	}
	return filepath.Join(dir, "fleetfind", "inventory.json")
}

func save(at string, c *cache) error {
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(at, b, 0o644)
}

func load(at string) (*cache, error) {
	b, err := os.ReadFile(at)
	if err != nil {
		return nil, err
	}
	var c cache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

var ghCalls atomic.Int64

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type repo struct {
	Org      string   `json:"org"`
	Name     string   `json:"name"`
	Desc     string   `json:"desc"`
	Archived bool     `json:"archived"`
	Pushed   string   `json:"pushed"`
	Topics   []string `json:"topics,omitempty"`
}

type cache struct {
	Taken time.Time `json:"taken"`
	Orgs  int       `json:"orgs"`
	Repos []repo    `json:"repos"`
}

func main() {
	refresh := flag.Bool("refresh", false, "fetch every organisation's repositories again (about one call per organisation)")
	at := flag.String("cache", defaultCache(), "where the inventory is kept")
	all := flag.Bool("all", false, "include archived repositories")
	flag.Parse()

	if *refresh {
		c, err := fetch()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleetfind:", err)
			os.Exit(1)
		}
		if err := save(*at, c); err != nil {
			fmt.Fprintln(os.Stderr, "fleetfind:", err)
			os.Exit(1)
		}
		fmt.Printf("%d repositories across %d organisations, taken %s\n",
			len(c.Repos), c.Orgs, c.Taken.Format(time.RFC3339))
		if flag.NArg() == 0 {
			return
		}
	}

	c, err := load(*at)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleetfind:", err, "\n  run `fleetfind -refresh` first")
		os.Exit(1)
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

	hits := search(c.Repos, flag.Args(), *all)
	if len(hits) == 0 {
		// ⛔ Said out loud. A sweep that prints nothing is indistinguishable
		// from a sweep that could not read.
		fmt.Printf("nothing in %d repositories (taken %s) matches %q\n",
			len(c.Repos), c.Taken.Format("2006-01-02"), strings.Join(flag.Args(), " "))
		return
	}
	for _, h := range hits {
		mark := ""
		if h.r.Archived {
			mark = "  [archived]"
		}
		fmt.Printf("%-34s %s%s\n", h.r.Org+"/"+h.r.Name, first(h.r.Desc, 92), mark)
	}
	fmt.Fprintf(os.Stderr, "\n%d of %d repositories, inventory of %s\n",
		len(hits), len(c.Repos), c.Taken.Format("2006-01-02"))
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
	r     repo
	score int
}

// search ranks a name match above a description match: an org called
// go-odf/ods is a stronger answer about spreadsheets than a sentence that
// happens to say "spreadsheet".
func search(rs []repo, terms []string, withArchived bool) []hit {
	var out []hit
	for _, r := range rs {
		if r.Archived && !withArchived {
			continue
		}
		path := strings.ToLower(r.Org + "/" + r.Name)
		desc := strings.ToLower(r.Desc + " " + strings.Join(r.Topics, " "))
		score, every := 0, true
		for _, t := range terms {
			t = strings.ToLower(t)
			switch {
			case strings.Contains(path, t):
				score += 10
			case strings.Contains(desc, t):
				score += 3
			default:
				every = false
			}
		}
		if every && score > 0 {
			out = append(out, hit{r, score})
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
			"-q", `.[] | {name, desc: (.description // ""), archived, pushed: .pushed_at, topics} | tostring`)
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

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

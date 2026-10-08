package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fleet = []repo{
	{Org: "go-odf", Name: "odf", Desc: "ODT (OpenDocument Text) ⇄ richdoc converter, pure Go"},
	{Org: "go-rtf", Name: "rtf", Desc: "RTF <-> richdoc converter, pure Go"},
	{Org: "go-richdoc", Name: "markdown", Desc: "Markdown (CommonMark + GFM) <-> richdoc converter"},
	{Org: "go-icons", Name: "iconoir", Desc: "Iconoir UI icons as embedded SVG"},
	{Org: "go-iconoir", Name: "iconoir", Desc: "the old home", Archived: true},
	{Org: "go-pdfkit", Name: "render", Desc: "PDF rasteriser", Topics: []string{"pdf", "raster"}},
}

func names(hs []hit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.r.Org+"/"+h.r.Name)
	}
	return out
}

func TestANameMattersMoreThanASentenceThatMentionsIt(t *testing.T) {
	// go-richdoc/markdown and go-odf/odf both say "richdoc"; only one of them
	// IS it. A tool answering "who does X" has to put the owner first or the
	// answer is a list to read rather than an answer.
	got := names(search(fleet, []string{"markdown"}, false))
	if len(got) == 0 || got[0] != "go-richdoc/markdown" {
		t.Errorf("searching for markdown gave %v", got)
	}
	got = names(search(fleet, []string{"richdoc"}, false))
	if len(got) != 3 {
		t.Fatalf("three repositories mention richdoc, search found %v", got)
	}
	// ⛔ The one that IS richdoc comes first. All three say the word; only
	// go-richdoc/markdown carries it in its path, and an answer that buries
	// the owner among the mentions is a list to read rather than an answer.
	if got[0] != "go-richdoc/markdown" {
		t.Errorf("richdoc ranked %v — the repository whose own name says it is not first", got)
	}
}

func TestEveryWordHasToMatch(t *testing.T) {
	// Two words narrow; they do not widen. "odf converter" is a question
	// about one repository, not about everything converting anything.
	if got := names(search(fleet, []string{"odf", "converter"}, false)); len(got) != 1 || got[0] != "go-odf/odf" {
		t.Errorf("odf+converter gave %v", got)
	}
	if got := search(fleet, []string{"odf", "rasteriser"}, false); len(got) != 0 {
		t.Errorf("a word no repository matches still returned %v", names(got))
	}
}

func TestAnArchivedRepositoryIsNotAnAnswerUnlessAsked(t *testing.T) {
	// ⛔ The go-onigmo incident: a retired org kept answering to the standard
	// name and a clone went to the frozen copy. An archived repository is
	// still findable — on purpose, because knowing it moved is the useful
	// answer — but it is never what a plain question returns.
	plain := names(search(fleet, []string{"iconoir"}, false))
	if len(plain) != 1 || plain[0] != "go-icons/iconoir" {
		t.Errorf("searching for iconoir gave %v, and one of those is archived", plain)
	}
	if all := names(search(fleet, []string{"iconoir"}, true)); len(all) != 2 {
		t.Errorf("asking for archived ones too gave %v", all)
	}
}

func TestATopicIsSearchedLikeADescription(t *testing.T) {
	if got := names(search(fleet, []string{"raster"}, false)); len(got) != 1 {
		t.Errorf("a topic did not answer: %v", got)
	}
}

func TestTheInventorySurvivesBeingWrittenAndReadBack(t *testing.T) {
	at := filepath.Join(t.TempDir(), "deep", "inventory.json")
	want := &cache{Taken: time.Now().Truncate(time.Second), Orgs: 2, Repos: fleet}
	if err := save(at, want); err != nil {
		t.Fatal(err)
	}
	got, err := load(at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Repos) != len(want.Repos) || got.Orgs != want.Orgs {
		t.Errorf("%d repositories of %d organisations came back, wrote %d of %d",
			len(got.Repos), got.Orgs, len(want.Repos), want.Orgs)
	}
	if !got.Taken.Equal(want.Taken) {
		t.Errorf("the date came back as %s, wrote %s — a cache that forgets WHEN it was "+
			"taken is the hand-kept map again", got.Taken, want.Taken)
	}
}

func TestFetchRefusesToReportAnEmptyFleet(t *testing.T) {
	// ⛔ Reading none is not reading zero. A gh that answers nothing — no
	// token, a changed flag, an org listing that moved — must not write an
	// empty inventory over a good one and let every later question answer
	// "nothing in 0 repositories".
	was := run
	t.Cleanup(func() { run = was })

	run = func(args ...string) (string, error) { return "", nil }
	_, err := fetch()
	if err == nil {
		t.Fatal("an empty organisation list was accepted as a fleet of none")
	}
	// ⛔ And it says WHICH half failed. The repository check below would also
	// refuse this, so the two guards are indistinguishable by outcome — the
	// only thing the first one adds is naming the step, which is the whole
	// difference between "the fleet is empty" and "I could not list it".
	// ⛔ It must NOT read like the other refusal. Both messages name
	// organisations, so asserting that word passes either way — and the
	// mutation that removed this guard survived exactly that test.
	if strings.Contains(err.Error(), "repository") {
		t.Errorf("a gh that answered nothing was reported as %q, which is the message for "+
			"organisations that listed and brought back nothing — a different failure", err)
	}

	// Organisations listed, and not one repository between them: the query
	// shape changed, which is not a fleet with no repositories in it.
	run = func(args ...string) (string, error) {
		if strings.Contains(args[1], "user/orgs") {
			return "go-odf\ngo-rtf\n", nil
		}
		return "", nil
	}
	_, err = fetch()
	if err == nil {
		t.Fatal("two organisations and no repositories was accepted")
	}
	if !strings.Contains(err.Error(), "repository") {
		t.Errorf("organisations that listed and brought back nothing were reported as %q, "+
			"which reads like the first failure rather than this one", err)
	}
}

func TestFetchKeepsTheRestWhenOneOrganisationWillNotList(t *testing.T) {
	// One organisation that refuses is not a reason to lose the other three
	// hundred — and the count it returns is what tells somebody something was
	// missed.
	was := run
	t.Cleanup(func() { run = was })
	run = func(args ...string) (string, error) {
		switch {
		case strings.Contains(args[1], "user/orgs"):
			return "go-odf\ngo-broken\ngo-rtf\n", nil
		case strings.Contains(args[1], "go-broken"):
			return "", fmt.Errorf("404")
		case strings.Contains(args[1], "go-odf"):
			return `{"name":"odf","desc":"ODT converter","archived":false,"pushed":"2026-10-01"}`, nil
		default:
			return `{"name":"rtf","desc":"RTF converter","archived":false,"pushed":"2026-10-02"}`, nil
		}
	}
	c, err := fetch()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Repos) != 2 {
		t.Errorf("%d repositories came back from two organisations that answered", len(c.Repos))
	}
	if c.Orgs != 3 {
		t.Errorf("it reports %d organisations; three were asked, and the count is how "+
			"somebody sees that one of them said nothing", c.Orgs)
	}
}

func TestALongDescriptionIsCutAndSaysSo(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := first(long, 20)
	if len([]rune(got)) != 20 {
		t.Errorf("a 200-character description was cut to %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut description does not say it was cut: %q", got)
	}
	if first("short", 20) != "short" {
		t.Error("a short description was touched")
	}
}

func TestAnOldInventorySaysSoBeforeItAnswers(t *testing.T) {
	// ⛔ The whole point of the tool. A "nothing matches" from a week-old
	// inventory is the hand-kept map again: it reads as the present, it is
	// believed, and something gets rebuilt that already exists.
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	if note := staleNote(now.Add(-2*time.Hour), now); note != "" {
		t.Errorf("a two-hour-old inventory warned: %q", note)
	}
	if note := staleNote(now.Add(-stale), now); note != "" {
		t.Errorf("an inventory exactly at the limit warned: %q", note)
	}
	note := staleNote(now.Add(-8*24*time.Hour), now)
	if note == "" {
		t.Fatal("a week-old inventory said nothing")
	}
	for _, want := range []string{"192h", "2026-09-30", "believing a NO"} {
		if !strings.Contains(note, want) {
			t.Errorf("the warning %q does not carry %q — age, date, and what to doubt "+
				"are each the reason it is said at all", note, want)
		}
	}
}

package collect

import (
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forge-scout/internal/forge"
)

// The dashboard shows only what asks for action: outside Scout health, a
// tile or table that would show 0 or nothing hides.

// hiddenUnless is every panel placed to hide while its query has no data,
// as "<tab> / <title> / <viz>", declared apart from the gates it checks so
// a gate removed from one fails the test rather than leaving it.
var hiddenUnless = []string{
	"Overview / Scout stalled / stat", "Overview / Scan integrity / stat", "Overview / Failing workflows / stat",
	"Overview / Critical and high alerts / stat", "Overview / PRs with failing checks / stat", "Overview / PRs from other people / stat",
	"Overview / Issues from other people / stat", "Overview / PRs untouched for 30+ days / stat", "Overview / Issues untouched for 30+ days / stat",
	"Overview / PR checks this token cannot read / stat", "Overview / Counts as of / stat", "Overview / All clear / stat",
	"Overview / Failing on the default branch / table", "Overview / Pull requests needing attention / table", "Overview / Critical and high alerts to fix / table",
	"CI runs / Failed runs, newest first / table", "CI runs / Workflows GitHub disabled / table", "CI runs / Runs that passed after a re-run / table",
	"CI runs / Slowest workflows by p95 run time / table",
	"Open work / Open pull requests / stat", "Open work / Open issues / stat", "Open work / PRs untouched for 30+ days / stat",
	"Open work / Issues untouched for 30+ days / stat", "Open work / PRs open for 90+ days / stat", "Open work / Issues open for 90+ days / stat",
	"Open work / Counts as of / stat", "Open work / Open pull requests / table", "Open work / Open issues / table",
	"Open work / PRs and issues by repository / table",
	"Code scanning / Open security alerts / stat", "Code scanning / Critical / stat", "Code scanning / High / stat", "Code scanning / Medium / stat",
	"Code scanning / Low / stat", "Code scanning / Unrated / stat", "Code scanning / Error, no security severity / stat",
	"Code scanning / Warning, no security severity / stat", "Code scanning / Note, no security severity / stat", "Code scanning / Counts as of / stat",
	"Code scanning / Open code scanning alerts / table", "Code scanning / Open alerts by tool / table", "Code scanning / Code scanning coverage / table",
}

// attentionPRs is the Overview's table of the open pull requests that need
// the reader.
const attentionPRs = "Pull requests needing attention"

// hiddenPoints is the maxDataPoints every hidden-unless panel pins.
const hiddenPoints = 500

// codeScanningTab shows only while a selected connection reads code
// scanning, and holds every code-scanning panel but the Overview's.
const codeScanningTab = "Code scanning"

// partialElsewhere maps a count whose tile hides whenever its read is not
// whole to the count whose tile reads Partial for that same read.
var partialElsewhere = map[string]string{"checks_unsupported_for_token": "failing_checks_prs"}

// asOfTitle is the title of each tab's tile dating the counts it keeps.
const asOfTitle = "Counts as of"

// countTabs are the tabs whose tiles keep a stopped connection's counts.
var countTabs = []string{"Overview", "Open work", codeScanningTab}

// layoutItem is one placement of an element: its tab and row, and the
// grid or auto-grid item's spec.
type layoutItem struct {
	spec          map[string]any
	tab, row      string
	name, kind    string
	rowHideHeader bool
}

// layoutRow is one row of a tab.
type layoutRow struct {
	spec      map[string]any
	tab, name string
}

// dashboardLayout is every row and item of d in reading order.
func dashboardLayout(d map[string]any) (rows []layoutRow, items []layoutItem) {
	tabs, _ := field(d, "spec", "layout", "spec", "tabs").([]any)
	for _, tab := range tabs {
		tabTitle, _ := field(tab, "spec", "title").(string)
		tabRows, _ := field(tab, "spec", "layout", "spec", "rows").([]any)
		for _, r := range tabRows {
			spec, _ := field(r, "spec").(map[string]any)
			title, _ := spec["title"].(string)
			rows = append(rows, layoutRow{spec: spec, tab: tabTitle, name: title})
			list, _ := field(spec, "layout", "spec", "items").([]any)
			for _, it := range list {
				kind, _ := field(it, "kind").(string)
				s, _ := field(it, "spec").(map[string]any)
				name, _ := field(s, "element", "name").(string)
				items = append(items, layoutItem{spec: s, tab: tabTitle, row: title, name: name, kind: kind, rowHideHeader: spec["hideHeader"] == true})
			}
		}
	}
	return rows, items
}

// hasDataRule reports a conditional rendering rule anywhere under v that
// hides on the query result.
func hasDataRule(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if x["kind"] == "ConditionalRenderingData" {
			return true
		}
		for _, y := range x {
			if hasDataRule(y) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(x, hasDataRule)
	}
	return false
}

// gatedPanels is each element placed with a has-data rule.
func gatedPanels(d map[string]any) map[string]layoutItem {
	out := map[string]layoutItem{}
	_, items := dashboardLayout(d)
	for _, it := range items {
		if hasDataRule(it.spec["conditionalRendering"]) {
			out[it.name] = it
		}
	}
	return out
}

// startsHidden is why a has-data item could show before its query answers,
// or "". Grafana 13.2.3 shows an item while its has-data rule is undecided,
// on load and on every refresh slower than 200 ms (grafana/grafana#134713),
// so an or group adds a time range size rule that is always false. A
// display: none panel measures no width, so its query runs only with a
// pinned maxDataPoints.
func startsHidden(d map[string]any, it *layoutItem) string {
	if it.kind != "AutoGridLayoutItem" {
		return "a " + it.kind + " cannot hide, want an auto-grid item"
	}
	group := it.spec["conditionalRendering"]
	rules, _ := field(group, "spec", "items").([]any)
	if field(group, "kind") != "ConditionalRenderingGroup" || field(group, "spec", "visibility") != "show" ||
		field(group, "spec", "condition") != "or" || len(rules) != 2 {
		return "want a show group matching either of two rules"
	}
	hasData, neverTrue := false, false
	for _, r := range rules {
		switch field(r, "kind") {
		case "ConditionalRenderingData":
			hasData = field(r, "spec", "value") == true
		case "ConditionalRenderingTimeRangeSize":
			neverTrue = field(r, "spec", "value") == "0s"
		}
	}
	if !hasData || !neverTrue {
		return `want a has-data rule and a time range size rule of "0s"`
	}
	points, _ := field(d, "spec", "elements", it.name, "spec", "data", "spec", "queryOptions", "maxDataPoints").(float64)
	if points != hiddenPoints {
		return "want queryOptions.maxDataPoints " + strconv.Itoa(hiddenPoints) + " on the panel, got " + strconv.FormatFloat(points, 'f', -1, 64)
	}
	return ""
}

func TestDashboard_hidden_unless_items_start_hidden_until_their_query_answers(t *testing.T) {
	d := loadDashboard(t)
	if field(d, "spec", "preload") != true {
		t.Errorf("spec.preload = %v, want true, so a tile below the fold queries on load and on refresh, not when scrolled to", field(d, "spec", "preload"))
	}
	titles := map[string]string{}
	for _, p := range dashboardPanels(d) {
		titles[p.name] = p.title + " / " + p.viz
	}
	gated := gatedPanels(d)
	_, items := dashboardLayout(d)
	var placed []string
	for _, it := range items {
		key := it.tab + " / " + titles[it.name]
		if slices.Contains(hiddenUnless, key) {
			placed = append(placed, key)
			if why := startsHidden(d, &it); why != "" {
				t.Errorf("%s (%s): %s", key, it.name, why)
			}
		} else if _, ok := gated[it.name]; ok {
			t.Errorf("%s (%s) hides on its data, want it listed in hiddenUnless or shown", key, it.name)
		}
	}
	slices.Sort(placed)
	if want := slices.Sorted(slices.Values(hiddenUnless)); !slices.Equal(placed, want) {
		t.Errorf("hidden-unless panels placed %v, want each of %v once", placed, want)
	}
	// A row whose panels all hide still draws its header.
	allGated, hides := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		at := it.tab + " / " + it.row
		_, ok := gated[it.name]
		if all, seen := allGated[at]; seen {
			ok = ok && all
		}
		allGated[at], hides[at] = ok, it.rowHideHeader
	}
	for _, at := range slices.Sorted(maps.Keys(allGated)) {
		if allGated[at] && !hides[at] {
			t.Errorf("row %s holds only hidden-unless panels and shows its header, want hideHeader so an empty row draws nothing", at)
		}
	}
	bad := layoutItem{kind: "AutoGridLayoutItem", name: "panel-10", spec: map[string]any{"conditionalRendering": map[string]any{
		"kind": "ConditionalRenderingGroup", "spec": map[string]any{"visibility": "show", "condition": "and", "items": []any{
			map[string]any{"kind": "ConditionalRenderingData", "spec": map[string]any{"value": true}},
		}},
	}}}
	if startsHidden(d, &bad) == "" {
		t.Error("startsHidden(a stock has-data rule) reported nothing, want the flash it shows on every load")
	}
}

func TestDashboard_scout_health_hides_nothing(t *testing.T) {
	rows, items := dashboardLayout(loadDashboard(t))
	n := 0
	for _, r := range rows {
		if r.tab != healthTab {
			continue
		}
		if r.spec["conditionalRendering"] != nil || r.spec["hideHeader"] == true {
			t.Errorf("Scout health row %q can hide (conditionalRendering %v, hideHeader %v), want every zero on the tab shown", r.name, r.spec["conditionalRendering"], r.spec["hideHeader"])
		}
	}
	for _, it := range items {
		if it.tab != healthTab {
			continue
		}
		n++
		if it.spec["conditionalRendering"] != nil {
			t.Errorf("Scout health panel %s in %q hides on %v, want it shown at 0", it.name, it.row, it.spec["conditionalRendering"])
		}
	}
	if n == 0 {
		t.Fatalf("Setup: %s places no panel on %s", dashboardPath, healthTab)
	}
}

// summaryCounts is the scan complete counts p's queries unwrap.
func summaryCounts(p *dashPanel) []string {
	var out []string
	for _, q := range p.queries {
		logged := jsonKeys(q.expr)
		for _, m := range unwrapRe.FindAllStringSubmatch(q.expr, -1) {
			if logged[m[1]] && m[1] != "scan_id" && !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

// countLines is one scan complete line per {connection, scan id, read
// state, count}: every read of p's tile in that state and, unless the count
// is "", every count the tile sums at it.
func countLines(t *testing.T, p *dashPanel, lines ...[4]string) []lokiLine {
	t.Helper()
	var out []lokiLine
	for _, l := range lines {
		id, _ := strconv.ParseInt(l[1], 10, 64)
		out = append(out, newLokiLine(t, id, tileScanLine("scan complete", l[0], id, l[2], l[3], p)))
	}
	return out
}

func TestDashboard_count_tiles_off_scout_health_hide_a_zero_and_still_read_partial(t *testing.T) {
	d := loadDashboard(t)
	gated, tabs := gatedPanels(d), panelTabs(d)
	hidden := 0
	for _, p := range dashboardPanels(d) {
		counts := summaryCounts(&p)
		if p.viz != "stat" || len(counts) == 0 || tabs[p.name] == healthTab || composedTile(&p) {
			continue
		}
		if _, isGated := gated[p.name]; !isGated {
			t.Errorf("tile %q on %s shows at 0, want it hidden", p.title, tabs[p.name])
			continue
		}
		eval := func(lines []lokiLine) []sample {
			t.Helper()
			if len(p.queries) != 1 {
				t.Fatalf("tile %q carries %d queries, want one value", p.title, len(p.queries))
			}
			got, err := (&lokiModel{lines: lines, at: 5000, maxSeries: lokiMaxSeries}).eval(p.queries[0].expr)
			if err != nil {
				t.Fatalf("tile %q: %v", p.title, err)
			}
			return got
		}
		none := eval(nil)
		zero := eval(countLines(t, &p, [4]string{"a", "1000", "complete", "0"}, [4]string{"b", "2000", "complete", "0"}))
		lit := eval(countLines(t, &p, [4]string{"a", "1000", "complete", "2"}, [4]string{"b", "2000", "complete", "0"}))
		partial := eval(countLines(t, &p, [4]string{"a", "1000", "complete", "0"}, [4]string{"b", "2000", "partial", ""}))
		if len(lit) != 1 || !(lit[0].value > 0) {
			t.Errorf("tile %q with a count of 2 = %v, want one value above 0", p.title, lit)
		}
		hidden++
		if len(none) != 0 || len(zero) != 0 {
			t.Errorf("tile %q with no scan = %v and at 0 = %v, want no data both times, so the tile hides", p.title, none, zero)
		}
		if other, ok := partialElsewhere[counts[0]]; ok {
			if len(partial) != 0 {
				t.Errorf("tile %q with a read in part = %v, want no data: the %s tile reads Partial for it", p.title, partial, other)
			}
			continue
		}
		if len(partial) != 1 || !math.IsNaN(partial[0].value) {
			t.Errorf("tile %q with a read in part = %v, want NaN, which reads %q", p.title, partial, nanText(t))
		}
	}
	if hidden < 23 {
		t.Fatalf("Setup: %d hidden-unless count tiles evaluated, want every one", hidden)
	}
	for count, other := range partialElsewhere {
		found := false
		for _, p := range dashboardPanels(d) {
			if p.viz == "stat" && slices.Equal(summaryCounts(&p), []string{other}) {
				_, found = gated[p.name]
			}
		}
		if !found {
			t.Errorf("no hidden-unless tile of %s reads Partial for the read the %s tile hides on", other, count)
		}
	}
}

func TestDashboard_every_stat_tile_shows_one_value(t *testing.T) {
	stats := 0
	for _, p := range dashboardPanels(loadDashboard(t)) {
		if p.viz != "stat" {
			continue
		}
		stats++
		opts := field(p.el, "spec", "vizConfig", "spec", "options")
		if len(p.queries) != 1 || field(opts, "textMode") != "value" {
			t.Errorf("tile %q shows %d queries with text mode %v, want one value per box", p.title, len(p.queries), field(opts, "textMode"))
		}
	}
	if stats == 0 {
		t.Fatalf("Setup: %s has no stat panel", dashboardPath)
	}
}

// overviewWorld is the counts the Overview's hidden tiles add and the reads
// they are read through, from which line builds a clean scan line.
type overviewWorld struct {
	counts []string
	reads  []string
}

func newOverviewWorld(t *testing.T, d map[string]any) *overviewWorld {
	t.Helper()
	w := &overviewWorld{}
	tabs, gated := panelTabs(d), gatedPanels(d)
	for _, p := range dashboardPanels(d) {
		if _, ok := gated[p.name]; !ok || tabs[p.name] != "Overview" {
			continue
		}
		counts := summaryCounts(&p)
		var reads []string
		for _, c := range counts {
			reads = append(reads, countReads(c)...)
		}
		for _, c := range counts {
			if !slices.Contains(w.counts, c) {
				w.counts = append(w.counts, c)
			}
		}
		for _, r := range reads {
			if !slices.Contains(w.reads, r) {
				w.reads = append(w.reads, r)
			}
		}
	}
	if len(w.counts) < 9 {
		t.Fatalf("Setup: the Overview's hidden tiles count %v, want every attention count", w.counts)
	}
	return w
}

// line is a scan line of connection gh at at, its fields over a clean one.
func (w *overviewWorld) line(t *testing.T, msg string, at time.Time, set map[string]string) lokiLine {
	t.Helper()
	l := map[string]string{"msg": msg, "forge": "github", "connection": "gh", "scan_id": strconv.FormatInt(at.UnixMilli(), 10), "degraded": "false", "scan_interval_s": "900"}
	for _, r := range w.reads {
		l[r] = "complete"
	}
	if msg == "scan complete" {
		for _, c := range w.counts {
			l[c] = "0"
		}
	}
	maps.Copy(l, set)
	return newLokiLine(t, at.UnixMilli(), l)
}

// composedTile reports whether p's query is other tiles' queries under
// unless, as All clear's is. The tile checks read those legs on the tiles
// themselves, and TestDashboard_all_clear_lights_only_when_nothing_else_on_the_overview_does
// checks the composition.
func composedTile(p *dashPanel) bool {
	if p.viz != "stat" || len(p.queries) != 1 {
		return false
	}
	n, err := parseLogQL(p.queries[0].expr)
	b, ok := n.(binNode)
	return err == nil && ok && b.op == "unless"
}

// countTile reports whether p is a count tile: a stat that reads NaN for a
// withheld count, apart from Scan integrity's verdict and a composed tile.
func countTile(p *dashPanel) bool {
	return p.viz == "stat" && p.title != "Scan integrity" && !composedTile(p) &&
		slices.ContainsFunc(p.queries, func(q dashQuery) bool { return strings.Contains(q.expr, "NaN") })
}

// litTiles is the title of every Overview tile that shows over lines at at.
func litTiles(t *testing.T, d map[string]any, lines []lokiLine, at time.Time) []string {
	t.Helper()
	tabs, gated := panelTabs(d), gatedPanels(d)
	var lit []string
	for _, p := range dashboardPanels(d) {
		if _, ok := gated[p.name]; !ok || tabs[p.name] != "Overview" || p.viz != "stat" {
			continue
		}
		m := &lokiModel{lines: lines, at: at.UnixMilli(), rangeWindow: 24 * time.Hour, maxSeries: lokiMaxSeries}
		got, err := m.eval(p.queries[0].expr)
		if err != nil {
			t.Fatalf("tile %q: %v\nexpr: %s", p.title, err, p.queries[0].expr)
		}
		if len(got) > 0 {
			lit = append(lit, p.title)
		}
	}
	slices.Sort(lit)
	return lit
}

func TestDashboard_all_clear_lights_only_when_nothing_else_on_the_overview_does(t *testing.T) {
	d := loadDashboard(t)
	w := newOverviewWorld(t, d)
	scan := now
	fresh := scan.Add(time.Minute)
	cases := map[string]struct {
		lines []lokiLine
		at    time.Time
		want  []string
	}{
		"clean scan": {lines: []lokiLine{w.line(t, "scan complete", scan, nil)}, at: fresh, want: []string{"All clear"}},
		"degraded scan": {
			lines: []lokiLine{w.line(t, "scan complete", scan, map[string]string{"degraded": "true"})}, at: fresh, want: []string{"Scan integrity"},
		},
		"budget stop after a clean scan": {
			lines: []lokiLine{w.line(t, "scan complete", scan.Add(-15*time.Minute), nil), w.line(t, "scan stopped", scan, nil)}, at: fresh,
			want: []string{asOfTitle, "Scan integrity"},
		},
		"scout stalled": {lines: []lokiLine{w.line(t, "scan complete", scan, nil)}, at: scan.Add(2 * time.Hour), want: []string{"Scout stalled"}},
		// old's only line of the 7 days, a stop, falls before the range.
		"a stop before the range": {lines: []lokiLine{
			w.line(t, "scan stopped", scan.Add(-3*24*time.Hour), map[string]string{"connection": "old"}), w.line(t, "scan complete", scan, nil),
		}, at: fresh},
		// old's only line, a whole read in a degraded scan, falls before the
		// range, so Scan integrity hides.
		"a degraded scan before the range": {lines: []lokiLine{
			w.line(t, "scan complete", scan.Add(-3*24*time.Hour), map[string]string{"connection": "old", "degraded": "true"}),
			w.line(t, "scan complete", scan, nil),
		}, at: fresh, want: []string{"All clear"}},
		"a stop before the range after a clean scan": {lines: []lokiLine{
			w.line(t, "scan complete", scan.Add(-3*24*time.Hour-15*time.Minute), map[string]string{"connection": "old"}),
			w.line(t, "scan stopped", scan.Add(-3*24*time.Hour), map[string]string{"connection": "old"}),
			w.line(t, "scan complete", scan, nil),
		}, at: fresh, want: []string{asOfTitle}},
	}
	for _, c := range w.counts {
		cases["one "+c] = struct {
			lines []lokiLine
			at    time.Time
			want  []string
		}{lines: []lokiLine{w.line(t, "scan complete", scan, map[string]string{c: "1"})}, at: fresh}
	}
	for name, tc := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			lit := litTiles(t, d, tc.lines, tc.at)
			if tc.want != nil && !slices.Equal(lit, tc.want) {
				t.Errorf("Overview lights %v, want %v", lit, tc.want)
			}
			if clear := slices.Contains(lit, "All clear"); len(lit) == 0 || (clear && len(lit) > 1) {
				t.Errorf("Overview lights %v, want All clear alone or a tile naming what to act on", lit)
			}
		})
	}
}

func TestDashboard_overview_verdict_tiles_draw_their_verdict(t *testing.T) {
	d := loadDashboard(t)
	w := newOverviewWorld(t, d)
	lines := []lokiLine{w.line(t, "scan complete", now, nil)}
	for title, tc := range map[string]struct {
		at   time.Time
		want string
	}{
		"All clear":     {now.Add(time.Minute), "All clear"},
		"Scout stalled": {now.Add(2 * time.Hour), "Stalled"},
	} {
		p := panelTitled(t, dashboardPanels(d), title)
		m := &lokiModel{lines: lines, at: tc.at.UnixMilli(), rangeWindow: 24 * time.Hour, maxSeries: lokiMaxSeries}
		got, err := m.eval(p.queries[0].expr)
		if err != nil || len(got) != 1 || valueTexts(&p)[strconv.FormatFloat(got[0].value, 'f', -1, 64)] != tc.want {
			t.Errorf("%s after a clean scan, at %v = %v (%v), want one value drawn as %q", title, tc.at, got, err, tc.want)
		}
	}
}

func TestDashboard_all_clear_needs_a_scan_of_the_selected_forge(t *testing.T) {
	d := loadDashboard(t)
	w := newOverviewWorld(t, d)
	lines := []lokiLine{w.line(t, "scan complete", now, nil)}
	p := panelTitled(t, dashboardPanels(d), "All clear")
	for forge, want := range map[string]int{"github": 1, "forgejo": 0} {
		t.Run(forge, func(t *testing.T) {
			m := &lokiModel{vars: map[string]string{"forge": forge}, lines: lines, at: now.Add(time.Minute).UnixMilli(), rangeWindow: 24 * time.Hour, maxSeries: lokiMaxSeries}
			got, err := m.eval(p.queries[0].expr)
			if err != nil || len(got) != want {
				t.Errorf("All clear after a clean GitHub scan, Forge %s = %v (%v), want %d series", forge, got, err, want)
			}
		})
	}
}

func TestDashboard_each_count_tab_dates_its_kept_counts_in_one_tile(t *testing.T) {
	d := loadDashboard(t)
	tabs, gated := panelTabs(d), gatedPanels(d)
	found := map[string]int{}
	for _, p := range dashboardPanels(d) {
		if p.title != asOfTitle {
			continue
		}
		found[tabs[p.name]]++
		if _, ok := gated[p.name]; !ok || p.viz != "stat" {
			t.Errorf("%q on %s is a %s shown at no data: %v, want a hidden-unless tile", p.title, tabs[p.name], p.viz, ok)
		}
		if unit := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "defaults", "unit"); unit != asOfUnit {
			t.Errorf("%q on %s has unit %v, want %s: the newest complete scan can be days old", p.title, tabs[p.name], unit, asOfUnit)
		}
		reads := readKeys(p.queries[0].expr)
		for s := readUnread; s <= readExcluded; s++ {
			// a counted in its newest complete scan at 1000 and stopped at
			// 2000, the stop reading every signal in state s; b is whole.
			whole := func(conn string, at int64, msg, state string) lokiLine {
				l := map[string]string{"msg": msg, "connection": conn, "scan_id": strconv.FormatInt(at, 10)}
				for _, r := range reads {
					l[r] = state
				}
				return newLokiLine(t, at, l)
			}
			kept := []lokiLine{whole("a", 1000, "scan complete", "complete"), whole("b", 2500, "scan complete", "complete")}
			stopped := append(slices.Clone(kept), whole("a", 2000, "scan stopped", s.String()))
			model := func(lines []lokiLine) []sample {
				got, err := (&lokiModel{lines: lines, at: 5000, maxSeries: lokiMaxSeries}).eval(p.queries[0].expr)
				if err != nil {
					t.Fatalf("%q on %s: %v", p.title, tabs[p.name], err)
				}
				return got
			}
			if got := model(stopped); len(got) != 1 || got[0].value != 1000 {
				t.Errorf("%q on %s with a's stop reading %s = %v, want a's kept scan 1000", p.title, tabs[p.name], s, got)
			}
			if got := model(kept); len(got) != 0 {
				t.Errorf("%q on %s with no stop = %v, want no data, so the tile hides", p.title, tabs[p.name], got)
			}
		}
	}
	for _, tab := range countTabs {
		if found[tab] != 1 {
			t.Errorf("tab %s carries %d %q tiles, want one", tab, found[tab], asOfTitle)
		}
	}
}

// readKeys is every _read field expr extracts.
func readKeys(expr string) []string {
	var out []string
	for k := range jsonKeys(expr) {
		if strings.HasSuffix(k, "_read") {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func TestDashboard_overview_tables_list_only_what_its_tiles_count(t *testing.T) {
	w := theSnapshotWorld(t)
	at := w.end.Add(time.Minute).UnixMilli()
	for title, tc := range map[string]struct {
		label string
		want  []string
	}{
		// 1 is untouched for over 30 days, 2 from someone else with failing
		// checks; 3 is a bot's and 4 is yours and new.
		attentionPRs:                      {label: "number", want: []string{"1", "2"}},
		"Critical and high alerts to fix": {label: "severity", want: []string{"critical", "high"}},
	} {
		t.Run(strings.ReplaceAll(title, " ", "_"), func(t *testing.T) {
			p := panelTitled(t, dashboardPanels(loadDashboard(t)), title)
			got, err := w.model(at).eval(p.queries[0].expr)
			if err != nil {
				t.Fatalf("panel %q: %v", p.title, err)
			}
			var shown []string
			for _, s := range got {
				if !slices.Contains(shown, s.labels[tc.label]) {
					shown = append(shown, s.labels[tc.label])
				}
			}
			slices.Sort(shown)
			if !slices.Equal(shown, tc.want) {
				t.Errorf("panel %q lists %s %v, want %v", p.title, tc.label, shown, tc.want)
			}
		})
	}
}

func TestDashboard_scout_health_shows_the_items_exclude_rules_leave_out(t *testing.T) {
	h := newHarness(t, excludeEndpoint())
	h.scan(t)
	line := lineFor(t, h.rec, "scan complete", "gh")
	if line["excluded_prs"] != "2" || line["excluded_issues"] != "1" {
		t.Fatalf("Setup: scan complete excluded_prs %q excluded_issues %q, want 2 and 1", line["excluded_prs"], line["excluded_issues"])
	}
	d := loadDashboard(t)
	tabs := panelTabs(d)
	logged := maps.Clone(line)
	logged["msg"] = "scan complete"
	lines := []lokiLine{newLokiLine(t, 1000, logged)}
	for title, count := range map[string]string{"PRs excluded by filters": "excluded_prs", "Issues excluded by filters": "excluded_issues"} {
		t.Run(strings.ReplaceAll(title, " ", "_"), func(t *testing.T) {
			p := panelOfViz(t, title, "stat")
			if tabs[p.name] != healthTab {
				t.Errorf("%q sits on %s, want %s beside what connections cannot read", title, tabs[p.name], healthTab)
			}
			if c := summaryCounts(&p); !slices.Equal(c, []string{count}) {
				t.Errorf("%q counts %v, want %s", title, c, count)
			}
			got, err := (&lokiModel{lines: lines, at: 5000, maxSeries: lokiMaxSeries}).eval(p.queries[0].expr)
			if err != nil || len(got) != 1 || strconv.FormatFloat(got[0].value, 'f', -1, 64) != line[count] {
				t.Errorf("%q = %v, %v, want the scan's %s %s", title, got, err, count, line[count])
			}
		})
	}
	if text := docSection(t, readMonitoringDoc(t), "The count tiles add up")[0]; !strings.Contains(text, "PRs excluded by filters") || !strings.Contains(text, "`excluded_prs`") {
		t.Errorf("%s tile paragraph does not name PRs excluded by filters and `excluded_prs`: %q", monitoringDoc, text)
	}
}

func TestDashboard_pull_requests_needing_attention_lists_one_behind_any_number_of_idler_ones(t *testing.T) {
	p := panelTitled(t, dashboardPanels(loadDashboard(t)), attentionPRs)
	const scanID = 1000
	conn := map[string]string{"forge": "github", "connection": "gh", "scan_id": strconv.Itoa(scanID)}
	complete := map[string]string{"msg": "scan complete"}
	maps.Copy(complete, conn)
	lines := []lokiLine{newLokiLine(t, scanID, complete)}
	// 26 open pull requests idlest first: 25 of yours, untouched for 10
	// days with passing checks, then one from someone else opened today.
	for rank := 1; rank <= 26; rank++ {
		n := strconv.Itoa(rank)
		l := map[string]string{
			"msg": "open pull request", "rank": n, "forge_rank": n, "repo": "o/r", "number": n, "ref": "#" + n, "title": "t" + n,
			"from": "you", "checks": "passing", "draft": "false", "url": "https://gh.test/o/r/pull/" + n, "updated_at": "2026-01-01T00:00:00Z", "idle_days": "10",
		}
		if rank == 26 {
			l["from"], l["idle_days"] = "someone_else", "0"
		}
		maps.Copy(l, conn)
		lines = append(lines, newLokiLine(t, scanID+int64(rank), l))
	}
	got, err := (&lokiModel{lines: lines, at: 5000, maxSeries: lokiMaxSeries}).eval(p.queries[0].expr)
	if err != nil {
		t.Fatalf("panel %q: %v", p.title, err)
	}
	var shown []string
	for _, s := range got {
		shown = append(shown, s.labels["number"])
	}
	if !slices.Equal(shown, []string{"26"}) {
		t.Errorf("panel %q lists numbers %v, want [26], the one pull request from someone else", p.title, shown)
	}
}

func TestDashboard_pull_requests_needing_attention_say_why(t *testing.T) {
	p := panelTitled(t, dashboardPanels(loadDashboard(t)), attentionPRs)
	const scanID = 1000
	conn := map[string]string{"forge": "github", "connection": "gh", "scan_id": strconv.Itoa(scanID)}
	complete := map[string]string{"msg": "scan complete"}
	maps.Copy(complete, conn)
	lines := []lokiLine{newLokiLine(t, scanID, complete)}
	// Each pull request's from, checks and idle days, and the Why it reads;
	// "" is one the table leaves out.
	cases := []struct{ from, checks, idle, want string }{
		{"you", "failing", "0", "Failing checks"},
		{"you", "passing", "30", "Idle 30+ days"},
		{"someone_else", "passing", "29", "From someone else"},
		{"bot", "failing", "45", "Failing checks, Idle 30+ days"},
		{"someone_else", "failing", "2", "Failing checks, From someone else"},
		{"someone_else", "none", "31", "Idle 30+ days, From someone else"},
		{"someone_else", "failing", "90", "Failing checks, Idle 30+ days, From someone else"},
		{"you", "passing", "29", ""},
		{"bot", "unknown", "0", ""},
	}
	want := map[string]string{}
	for i, c := range cases {
		n := strconv.Itoa(i + 1)
		l := map[string]string{
			"msg": "open pull request", "rank": n, "forge_rank": n, "repo": "o/r", "number": n, "ref": "#" + n, "title": "t" + n,
			"from": c.from, "checks": c.checks, "draft": "false", "url": "https://gh.test/o/r/pull/" + n, "updated_at": "2026-01-01T00:00:00Z", "idle_days": c.idle,
		}
		maps.Copy(l, conn)
		lines = append(lines, newLokiLine(t, scanID+int64(i+1), l))
		if c.want != "" {
			want[n] = c.want
		}
	}
	got, err := (&lokiModel{lines: lines, at: 5000, maxSeries: lokiMaxSeries}).eval(p.queries[0].expr)
	if err != nil {
		t.Fatalf("panel %q: %v", p.title, err)
	}
	shown := map[string]string{}
	for _, s := range got {
		shown[s.labels["number"]] = s.labels["why"]
	}
	if !maps.Equal(shown, want) {
		t.Errorf("panel %q reads Why by number %v, want %v", p.title, shown, want)
	}
	kept := false
	for _, tr := range p.transforms {
		if tr["group"] == "filterFieldsByName" {
			kept = slices.Contains(anyStrings(field(tr, "include", "names")), "why")
		}
	}
	if !kept || columnName(&p, "why") != "Why" {
		t.Errorf("panel %q keeps why %v under the header %q, want a column headed Why", p.title, kept, columnName(&p, "why"))
	}
}

// columnName is the header p's overrides give field name, or "".
func columnName(p *dashPanel, name string) string {
	overrides, _ := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "overrides").([]any)
	for _, o := range overrides {
		if field(o, "matcher", "id") != "byName" || field(o, "matcher", "options") != name {
			continue
		}
		props, _ := field(o, "properties").([]any)
		for _, pr := range props {
			if field(pr, "id") == "displayName" {
				s, _ := field(pr, "value").(string)
				return s
			}
		}
	}
	return ""
}

// codeScanningRe matches a query that reads only code scanning: its lines,
// its counts or its read.
var codeScanningRe = regexp.MustCompile("msg=`[^`]*security[^`]*`|unwrap security_alerts|\\| security_alerts_read")

// overviewCodeScanning is the Overview's code-scanning panels, each of
// which shows only while a connection reads code scanning.
var overviewCodeScanning = []string{"Critical and high alerts", "Critical and high alerts to fix"}

// readsCodeScanning reports a show rule on the security_forges variable
// holding a forge, alone in its group.
func readsCodeScanning(rule any) bool {
	items, _ := field(rule, "spec", "items").([]any)
	return field(rule, "kind") == "ConditionalRenderingGroup" && field(rule, "spec", "visibility") == "show" && len(items) == 1 &&
		field(items[0], "kind") == "ConditionalRenderingVariable" && field(items[0], "spec", "variable") == "security_forges" &&
		field(items[0], "spec", "operator") == "matches" && field(items[0], "spec", "value") == ".+"
}

func TestDashboard_code_scanning_has_one_tab_shown_while_a_connection_reads_it(t *testing.T) {
	d := loadDashboard(t)
	tabs, _ := field(d, "spec", "layout", "spec", "tabs").([]any)
	found := false
	for _, tab := range tabs {
		if field(tab, "spec", "title") == codeScanningTab {
			found = true
			if rule := field(tab, "spec", "conditionalRendering"); !readsCodeScanning(rule) {
				t.Errorf("tab %s shows on %v, want only while security_forges holds a forge", codeScanningTab, rule)
			}
		}
	}
	if !found {
		t.Fatalf("Setup: %s has no %s tab", dashboardPath, codeScanningTab)
	}
	rows, _ := dashboardLayout(d)
	panelTab := panelTabs(d)
	for _, p := range dashboardPanels(d) {
		reads := slices.ContainsFunc(p.queries, func(q dashQuery) bool { return codeScanningRe.MatchString(q.expr) })
		switch tab := panelTab[p.name]; {
		case !reads || composedTile(&p) || tab == codeScanningTab:
		case tab == "Overview" && slices.Contains(overviewCodeScanning, p.title):
		default:
			t.Errorf("code-scanning panel %q sits on %s, want %s", p.title, tab, codeScanningTab)
		}
	}
	for _, r := range rows {
		if r.tab == "Overview" && r.name == "Code scanning alerts to fix" && !readsCodeScanning(r.spec["conditionalRendering"]) {
			t.Errorf("Overview row %q shows on %v, want only while security_forges holds a forge", r.name, r.spec["conditionalRendering"])
		}
	}
}

func TestDashboard_code_scanning_reads_nothing_from_a_connection_that_does_not_read_it(t *testing.T) {
	d := loadDashboard(t)
	var stream string
	vars, _ := field(d, "spec", "variables").([]any)
	for _, v := range vars {
		if field(v, "spec", "name") == "security_forges" {
			stream, _ = field(v, "spec", "query", "spec", "stream").(string)
		}
	}
	if stream == "" {
		t.Fatalf("Setup: %s has no security_forges stream", dashboardPath)
	}
	tile := panelOfViz(t, "Critical and high alerts", "stat")
	alert := forge.Alert{Repo: "o/r", Number: 1, Rule: "go/sql-injection", SecuritySeverity: "critical", Tool: "CodeQL", Source: "code_scanning", CreatedAt: now.Add(-24 * time.Hour)}
	for name, tc := range map[string]struct {
		ep       func() *endpoint
		forges   []string
		critical []float64
	}{
		"gitlab only": {ep: func() *endpoint { return gitlabEndpoint("gl") }},
		"github with code scanning off": {ep: func() *endpoint {
			ep := githubEndpoint("gh")
			ep.cfg.Security.Enabled = false
			ep.gh.alerts["o/r"] = []forge.Alert{alert}
			return ep
		}},
		"github with one critical alert": {ep: func() *endpoint {
			ep := githubEndpoint("gh")
			ep.gh.alerts["o/r"] = []forge.Alert{alert}
			return ep
		}, forges: []string{"github"}, critical: []float64{1}},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			h := newHarness(t, tc.ep())
			h.scan(t)
			var lines []lokiLine
			for i, l := range loggedLines(h.rec) {
				lines = append(lines, newLokiLine(t, now.UnixMilli()+int64(i), l))
			}
			m := &lokiModel{lines: lines, at: now.Add(time.Minute).UnixMilli(), rangeWindow: time.Hour, maxSeries: lokiMaxSeries}
			got, err := m.eval("count by (forge) (count_over_time(" + stream + " [$__range]))")
			if err != nil {
				t.Fatalf("security_forges: %v", err)
			}
			var forges []string
			for _, s := range got {
				forges = append(forges, s.labels["forge"])
			}
			if !slices.Equal(forges, tc.forges) {
				t.Errorf("security_forges = %v, want %v", forges, tc.forges)
			}
			got, err = m.eval(tile.queries[0].expr)
			if err != nil {
				t.Fatalf("tile %q: %v", tile.title, err)
			}
			var values []float64
			for _, s := range got {
				values = append(values, s.value)
			}
			if !slices.Equal(values, tc.critical) {
				t.Errorf("tile %q = %v, want %v", tile.title, values, tc.critical)
			}
		})
	}
}

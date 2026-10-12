package collect

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/cplieger/github-scout/internal/forge"
)

// asOfName is the name a count tile shows the time of a kept count under.
const asOfName = "As of"

// asOfUnit dates a kept count: the scan it comes from can be days old.
const asOfUnit = "time:MMM D, HH:mm"

// asOfQuery is the query of a count tile that dates a kept count.
func asOfQuery(p *dashPanel) (dashQuery, bool) {
	names := refIDNames(p.el)
	for _, q := range p.queries {
		if names[q.refID] == asOfName {
			return q, true
		}
	}
	return dashQuery{}, false
}

func TestDashboard_every_as_of_mark_carries_its_date(t *testing.T) {
	marks := 0
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if props, ok := x["properties"].([]any); ok {
				named, unit := false, ""
				for _, pr := range props {
					pm, _ := pr.(map[string]any)
					named = named || (pm["id"] == "displayName" && pm["value"] == asOfName)
					if pm["id"] == "unit" {
						unit, _ = pm["value"].(string)
					}
				}
				if named {
					marks++
					if unit != asOfUnit {
						t.Errorf("an %s mark has unit %q, want %s: the newest complete scan can be days old", asOfName, unit, asOfUnit)
					}
				}
			}
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(field(loadDashboard(t), "spec", "elements"))
	if marks < 30 {
		t.Fatalf("Setup: %s has %d %s marks, want one on every count tile and item table", dashboardPath, marks, asOfName)
	}
}

// statLayout models a stat value at 1280 px with Grafana's menu docked, its
// narrowest common desktop layout: a grid column is 33.8 px, and Grafana
// splits a tile's width evenly between its values, or stacks them in rows
// of the tile's width, a name beside its value in a tile of three. A rune at
// 28 px takes 14.8 px as a digit, a capital or a colon and 12 px otherwise,
// measured on Partial (85 px) and 13:43 (74 px); a text near the bound needs
// a render to prove it fits.
const (
	// nameGapPx is the gap between a value and its name beside it.
	nameGapPx    = 8.0
	gridColumnPx = 33.8
	wideRunePx   = 14.8
	narrowRunePx = 12.0
)

func textPx(s string, size float64) float64 {
	w := 0.0
	for _, r := range s {
		if unicode.IsDigit(r) || unicode.IsUpper(r) || r == ':' {
			w += wideRunePx
		} else {
			w += narrowRunePx
		}
	}
	return w * size / 28
}

// fitsValue reports whether s fits a value box boxPx wide in at most lines
// lines, wrapped at spaces.
func fitsValue(s string, size, boxPx float64, lines int) bool {
	n, line := 1, ""
	for word := range strings.FieldsSeq(s) {
		next := strings.TrimSpace(line + " " + word)
		if textPx(next, size) <= boxPx {
			line = next
			continue
		}
		if textPx(word, size) > boxPx {
			return false
		}
		n++
		line = word
	}
	return n <= lines
}

// statRow is one value of a stat panel: its name and every text it can show
// in its value's place.
type statRow struct {
	name  string
	texts []string
}

// statRows is each query's value: a sample count, the panel's mapped texts,
// its own override's texts, and for a dated time unit its widest date and
// time. The panel's no-value text shows alone, in place of every row.
func statRows(p *dashPanel) []statRow {
	fc, _ := field(p.el, "spec", "vizConfig", "spec", "fieldConfig").(map[string]any)
	dflt, _ := fc["defaults"].(map[string]any)
	shared := append([]string{"000"}, mappedTexts(dflt["mappings"])...)
	overrides, _ := fc["overrides"].([]any)
	var rows []statRow
	for _, q := range p.queries {
		row := statRow{texts: slices.Clone(shared)}
		for _, o := range overrides {
			if ref, _ := field(o, "matcher", "options").(string); ref != q.refID {
				continue
			}
			props, _ := field(o, "properties").([]any)
			for _, pr := range props {
				pm, _ := pr.(map[string]any)
				switch v := pm["value"]; pm["id"] {
				case "displayName":
					row.name, _ = v.(string)
				case "unit":
					if v == asOfUnit {
						row.texts = append(row.texts, "Sep 30, 00:00")
					}
				}
			}
			row.texts = append(row.texts, mappedTexts(o)...)
		}
		rows = append(rows, row)
	}
	return rows
}

// mappedTexts is every no value and mapped text under v.
func mappedTexts(v any) []string {
	var texts []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				if s, ok := y.(string); ok && (k == "text" || (k == "value" && x["id"] == "noValue") || k == "noValue") {
					texts = append(texts, s)
				}
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(v)
	return texts
}

// layoutWidths is each element's width in grid columns.
func layoutWidths(d map[string]any) map[string]float64 {
	out := map[string]float64{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["kind"] == "GridLayoutItem" {
				name, _ := field(x, "spec", "element", "name").(string)
				out[name], _ = field(x, "spec", "width").(float64)
			}
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(field(d, "spec", "layout"))
	return out
}

// checkStatTexts reports every text of p that overflows its value box in a
// tile cols columns wide.
func checkStatTexts(p *dashPanel, cols float64) []string {
	opts := field(p.el, "spec", "vizConfig", "spec", "options")
	size, _ := field(opts, "text", "valueSize").(float64)
	titleSize, _ := field(opts, "text", "titleSize").(float64)
	stacked := field(opts, "orientation") == "horizontal"
	tile := cols*gridColumnPx - 4
	var problems []string
	if noValue, _ := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "defaults", "noValue").(string); noValue != "" && !fitsValue(noValue, size, tile, 2) {
		problems = append(problems, "the no-value text "+strconv.Quote(noValue)+" overflows the tile")
	}
	rows := statRows(p)
	for _, row := range rows {
		box, lines := tile/float64(len(rows)), 1
		switch {
		case stacked && len(rows) >= 3:
			box = tile - textPx(row.name, titleSize) - nameGapPx
		case stacked:
			box = tile
		}
		if len(rows) == 1 {
			lines = 2
		}
		for _, s := range row.texts {
			if !fitsValue(s, size, box, lines) {
				problems = append(problems, strconv.Quote(s)+" overflows its "+strconv.Itoa(int(box))+" px box")
			}
		}
	}
	return problems
}

func TestDashboard_stat_texts_fit_their_value_box(t *testing.T) {
	d := loadDashboard(t)
	widths := layoutWidths(d)
	checked := 0
	for _, p := range dashboardPanels(d) {
		if p.viz != "stat" {
			continue
		}
		checked++
		for _, problem := range checkStatTexts(&p, widths[p.name]) {
			t.Errorf("panel %q at %v columns: %s at 1280 px", p.title, widths[p.name], problem)
		}
	}
	if checked == 0 {
		t.Fatalf("Setup: %s has no stat panel", dashboardPath)
	}
	for name, tc := range map[string]struct {
		s     string
		size  float64
		box   float64
		lines int
		want  bool
	}{
		"the old tile text in half a tile": {"No whole count in the newest scan", 28, 131, 1, false},
		"a time in half a 4-column tile":   {"13:43", 28, 63, 1, false},
		"a dated time in a 4-column tile":  {"Oct 12, 02:30", 28, 131, 1, false},
		"a dated time in an 8-column tile": {"Oct 12, 02:30", 28, 266, 1, true},
		"a stop wrapped onto two lines":    {"Stopped: API budget", 28, 266, 2, true},
		"partial in a third of a tile":     {"Partial", 28, 88, 1, true},
	} {
		if got := fitsValue(tc.s, tc.size, tc.box, tc.lines); got != tc.want {
			t.Errorf("fitsValue(%s) = %v, want %v", name, got, tc.want)
		}
	}
}

func TestDashboard_stat_values_keep_their_font_size(t *testing.T) {
	for _, p := range dashboardPanels(loadDashboard(t)) {
		if p.viz != "stat" {
			continue
		}
		if size := field(p.el, "spec", "vizConfig", "spec", "options", "text", "valueSize"); size != 28.0 {
			t.Errorf("panel %q value size = %v, want 28: a mark fits by layout, never a smaller count", p.title, size)
		}
	}
}

func TestDashboard_open_work_count_tiles_stack_counted_excluded_and_as_of(t *testing.T) {
	for _, title := range []string{"Open pull requests", "Open issues"} {
		p := panelOfViz(t, title, "stat")
		opts := field(p.el, "spec", "vizConfig", "spec", "options")
		var names []string
		for _, q := range p.queries {
			names = append(names, refIDNames(p.el)[q.refID])
		}
		if field(opts, "orientation") != "horizontal" || field(opts, "textMode") != "value_and_name" || !slices.Equal(names, []string{"Counted", excludedName, asOfName}) {
			t.Errorf("tile %q orientation %v, text mode %v, values %v, want stacked rows Counted, %s and %s", title, field(opts, "orientation"), field(opts, "textMode"), names, excludedName, asOfName)
		}
	}
}

func TestDashboard_severity_tiles_sit_three_to_a_row(t *testing.T) {
	d := loadDashboard(t)
	var items []map[string]any
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["kind"] == "RowsLayoutRow" && field(x, "spec", "title") == "Open alerts by severity" {
				list, _ := field(x, "spec", "layout", "spec", "items").([]any)
				for _, it := range list {
					m, _ := it.(map[string]any)
					items = append(items, m)
				}
				return
			}
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(field(d, "spec", "layout"))
	if len(items) < 6 {
		t.Fatalf("Setup: the severity row holds %d tiles, want at least 6", len(items))
	}
	rows := map[float64]int{}
	for _, it := range items {
		x, _ := field(it, "spec", "x").(float64)
		y, _ := field(it, "spec", "y").(float64)
		if w := field(it, "spec", "width"); w != 8.0 || int(x)%8 != 0 || field(it, "spec", "height") != 4.0 {
			t.Errorf("severity tile %v is %v wide at x %v, want 8 columns on a third of the row", field(it, "spec", "element", "name"), w, x)
		}
		rows[y]++
	}
	for y, n := range rows {
		if n != 3 {
			t.Errorf("severity row at y %v holds %d tiles, want 3", y, n)
		}
	}
}

// integrityTexts is the Scan integrity text of each per-connection verdict.
func integrityTexts(p *dashPanel) map[string]string {
	out := map[string]string{}
	mappings, _ := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "defaults", "mappings").([]any)
	for _, m := range mappings {
		opts, _ := field(m, "options").(map[string]any)
		for k, v := range opts {
			out[k], _ = field(v, "text").(string)
		}
	}
	return out
}

func TestDashboard_scan_integrity_reads_a_budget_stop_as_stopped(t *testing.T) {
	p := panelTitled(t, dashboardPanels(loadDashboard(t)), "Scan integrity")
	expr := p.queries[0].expr
	f := tileFormatRe.FindStringSubmatch(expr)
	if f == nil || !strings.HasPrefix(expr, "max(") {
		t.Fatalf("Scan integrity = %s, want the worst of each connection's newest verdict", expr)
	}
	texts := integrityTexts(&p)
	for name, tc := range map[string]struct {
		line map[string]string
		want string
	}{
		"complete":    {map[string]string{"msg": "scan complete", "degraded": "false"}, "Complete"},
		"degraded":    {map[string]string{"msg": "scan complete", "degraded": "true"}, "Degraded"},
		"budget stop": {map[string]string{"msg": "scan stopped", "degraded": "false", "reason": stopReadReserve}, "Stopped: API budget"},
		"other stop":  {map[string]string{"msg": "scan stopped", "degraded": "true", "reason": stopRateLimited}, "Degraded"},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			v := formatLabels(t, f[2], []map[string]string{tc.line})[0]
			if texts[v] != tc.want {
				t.Errorf("Scan integrity reads %q (%s) for a %s, want %q", texts[v], v, name, tc.want)
			}
		})
	}
	if worst := []string{"Complete", "Stopped: API budget", "Degraded"}; !slices.Equal([]string{texts["0"], texts["1"], texts["2"]}, worst) {
		t.Errorf("Scan integrity verdicts 0, 1, 2 read %q %q %q, want %v in rising order, so max shows the worst", texts["0"], texts["1"], texts["2"], worst)
	}
	raw := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "defaults", "thresholds", "steps")
	steps, _ := raw.([]any)
	colors := map[float64]string{}
	for _, s := range steps {
		if v, ok := field(s, "value").(float64); ok {
			colors[v], _ = field(s, "color").(string)
		}
	}
	if want := map[float64]string{0: "green", 1: "orange", 2: "red"}; !maps.Equal(colors, want) {
		t.Errorf("Scan integrity colors %v, want %v: a budget stop in amber", colors, want)
	}
}

func TestDashboard_scout_health_lists_the_scans_stopped_for_the_budget(t *testing.T) {
	p := panelTitled(t, dashboardPanels(loadDashboard(t)), "Scans stopped for API budget")
	gh := githubEndpoint("gh")
	gh.conn.errs = map[string]error{"runs o/r": forge.ErrReadDeferred}
	h := newHarness(t, gh)
	h.scan(t)
	stopped := lineFor(t, h.rec, "scan stopped", "gh")
	w := &snapshotWorld{complete: map[string][]int64{}}
	w.add(t, now, loggedLines(h.rec))
	reset, err := time.Parse(time.RFC3339, stopped["budget_reset"])
	if err != nil {
		t.Fatalf("Setup: budget_reset %q: %v", stopped["budget_reset"], err)
	}
	want := []string{"1", stopped["scan_id"], strconv.FormatInt(reset.UnixMilli(), 10)}
	var got []string
	for _, q := range p.queries {
		if !q.instant {
			t.Errorf("query %s is a range query, want one instant read of the range", q.refID)
		}
		v, err := (&lokiModel{lines: w.lines, at: now.Add(time.Minute).UnixMilli(), rangeWindow: time.Hour, maxSeries: lokiMaxSeries}).eval(q.expr)
		if err != nil || len(v) != 1 {
			t.Fatalf("query %s = %v, %v, want one series for gh", q.refID, v, err)
		}
		got = append(got, strconv.FormatFloat(v[0].value, 'f', -1, 64))
	}
	if !slices.Equal(got, want) {
		t.Errorf("panel %q reads %v for gh, want the count, the newest stop's scan_id and its budget reset in ms, %v", p.title, got, want)
	}
}

func TestDashboard_scout_health_renews_each_budget_stop_at_its_own_reset(t *testing.T) {
	p := panelTitled(t, dashboardPanels(loadDashboard(t)), "Scans stopped for API budget")
	lower := now.Add(30 * time.Minute)
	noReset := -1.0
	if text := columnTexts(&p, "budget_reset")["-1"]; text != "-" {
		t.Errorf("Budget renews shows %v as %q, want %q: a stop that reported no reset keeps its column", noReset, text, "-")
	}
	for name, tc := range map[string]struct {
		newest func(q *fakeQuota)
		// reset is the newest stop's budget_reset, "" for none.
		reset string
		want  float64
	}{
		"newest stop with no reset":    {newest: func(q *fakeQuota) { q.noReset = true }, want: noReset},
		"newest stop with a lower one": {newest: func(q *fakeQuota) { q.reset = lower }, reset: stamp(lower), want: float64(lower.UnixMilli())},
	} {
		t.Run(name, func(t *testing.T) {
			gh := githubEndpoint("gh")
			gh.conn.errs = map[string]error{"runs o/r": forge.ErrReadDeferred}
			h := newHarness(t, gh)
			h.scan(t)
			w := &snapshotWorld{complete: map[string][]int64{}}
			w.add(t, now, loggedLines(h.rec))
			if first := lineFor(t, h.rec, "scan stopped", "gh"); first["budget_reset"] != stamp(now.Add(time.Hour)) {
				t.Fatalf("Setup: first stop %v, want a reset an hour out", first)
			}
			*h.clock = now.Add(15 * time.Minute)
			tc.newest(gh.conn.quota)
			h.scan(t)
			w.add(t, *h.clock, loggedLines(h.rec))
			stopped := lineFor(t, h.rec, "scan stopped", "gh")
			if stopped["budget_reset"] != tc.reset {
				t.Fatalf("Setup: newest stop's budget_reset = %q, want %q", stopped["budget_reset"], tc.reset)
			}
			m := &lokiModel{lines: w.lines, at: now.Add(20 * time.Minute).UnixMilli(), rangeWindow: time.Hour, maxSeries: lokiMaxSeries}
			got := map[string][]float64{}
			for _, q := range p.queries {
				v, err := m.eval(q.expr)
				if err != nil {
					t.Fatalf("query %s: %v", q.refID, err)
				}
				for _, s := range v {
					got[q.refID] = append(got[q.refID], s.value)
				}
			}
			id, _ := strconv.ParseFloat(stopped["scan_id"], 64)
			want := map[string][]float64{"A": {2}, "B": {id}, "C": {tc.want}}
			if !maps.EqualFunc(got, want, func(a, b []float64) bool { return slices.Equal(a, b) }) {
				t.Errorf("panel %q reads %v for gh, want %v: Budget renews is the newest stop's own reset, -1 where it reported none", p.title, got, want)
			}
		})
	}
}

// columnTexts maps each value p's field name maps to the text it shows.
func columnTexts(p *dashPanel, name string) map[string]string {
	out := map[string]string{}
	overrides, _ := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "overrides").([]any)
	for _, o := range overrides {
		if field(o, "matcher", "id") != "byName" || field(o, "matcher", "options") != name {
			continue
		}
		props, _ := field(o, "properties").([]any)
		for _, pr := range props {
			if field(pr, "id") != "mappings" {
				continue
			}
			mappings, _ := field(pr, "value").([]any)
			for _, m := range mappings {
				opts, _ := field(m, "options").(map[string]any)
				for k, v := range opts {
					out[k], _ = field(v, "text").(string)
				}
			}
		}
	}
	return out
}

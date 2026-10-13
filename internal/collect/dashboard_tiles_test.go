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

// asOfName is the name an item table shows the time of a kept row under.
const asOfName = "As of"

// asOfUnit dates a kept count: the scan it comes from can be days old.
const asOfUnit = "time:MMM D, HH:mm"

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
	if marks < 8 {
		t.Fatalf("Setup: %s has %d %s columns, want one on every item table", dashboardPath, marks, asOfName)
	}
}

// A stat value is modelled at its narrowest common width: a grid
// column is 33.8 px at 1280 px with Grafana's menu docked, and an auto-grid
// tile is at least its column width. A rune at 28 px takes 14.8 px as a
// digit, a capital or a colon and 12 px otherwise, measured on Partial
// (85 px) and 13:43 (74 px); a text near the bound needs a render to prove
// it fits.
const (
	gridColumnPx = 33.8
	wideRunePx   = 14.8
	narrowRunePx = 12.0
	// titlePadPx is the title row's padding and its menu icon.
	titlePadPx = 40.0
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

// statTexts is every text a stat panel can show in its value's place: a
// sample count, its mapped texts, its no-value text, and for a dated time
// unit its widest date and time.
func statTexts(p *dashPanel) []string {
	dflt := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "defaults")
	texts := append([]string{"000"}, mappedTexts(field(dflt, "mappings"))...)
	if noValue, _ := field(dflt, "noValue").(string); noValue != "" {
		texts = append(texts, noValue)
	}
	if field(dflt, "unit") == asOfUnit {
		texts = append(texts, "Sep 30, 00:00")
	}
	return append(texts, mappedTexts(field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "overrides"))...)
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

// tileWidths is each placed element's narrowest width in px: a grid item's
// columns, or an auto grid's custom column width, 0 where neither is set.
func tileWidths(d map[string]any) map[string]float64 {
	out := map[string]float64{}
	var walk func(v any, auto float64)
	walk = func(v any, auto float64) {
		switch x := v.(type) {
		case map[string]any:
			switch x["kind"] {
			case "AutoGridLayout":
				auto = 0
				if field(x, "spec", "columnWidthMode") == "custom" {
					auto, _ = field(x, "spec", "columnWidth").(float64)
				}
			case "GridLayoutItem":
				name, _ := field(x, "spec", "element", "name").(string)
				cols, _ := field(x, "spec", "width").(float64)
				out[name] = cols * gridColumnPx
			case "AutoGridLayoutItem":
				name, _ := field(x, "spec", "element", "name").(string)
				out[name] = auto
			}
			for _, y := range x {
				walk(y, auto)
			}
		case []any:
			for _, y := range x {
				walk(y, auto)
			}
		}
	}
	walk(field(d, "spec", "layout"), 0)
	return out
}

// checkStatTexts reports every text of p that overflows a tile tilePx wide:
// its title on one line, its value on two.
func checkStatTexts(p *dashPanel, tilePx float64) []string {
	opts := field(p.el, "spec", "vizConfig", "spec", "options")
	size, _ := field(opts, "text", "valueSize").(float64)
	titleSize, _ := field(opts, "text", "titleSize").(float64)
	box := tilePx - 4
	var problems []string
	if textPx(p.title, titleSize) > tilePx-titlePadPx {
		problems = append(problems, "the title overflows the tile")
	}
	for _, s := range statTexts(p) {
		if !fitsValue(s, size, box, 2) {
			problems = append(problems, strconv.Quote(s)+" overflows its "+strconv.Itoa(int(box))+" px box")
		}
	}
	return problems
}

// tileColumnPx is the column width of every auto grid of tiles: it fits
// the longest title and value at the standard sizes.
const tileColumnPx = 300

func TestDashboard_stat_texts_fit_their_value_box(t *testing.T) {
	d := loadDashboard(t)
	widths := tileWidths(d)
	checked := 0
	for _, p := range dashboardPanels(d) {
		if p.viz != "stat" {
			continue
		}
		checked++
		if widths[p.name] == 0 {
			t.Errorf("panel %q sits in an auto grid with no custom column width, so its width is unknown", p.title)
			continue
		}
		for _, problem := range checkStatTexts(&p, widths[p.name]) {
			t.Errorf("panel %q at %v px: %s", p.title, widths[p.name], problem)
		}
	}
	if checked == 0 {
		t.Fatalf("Setup: %s has no stat panel", dashboardPath)
	}
	gated := gatedPanels(d)
	for name := range gated {
		if w := widths[name]; field(d, "spec", "elements", name, "spec", "vizConfig", "group") == "stat" && w != tileColumnPx {
			t.Errorf("tile %s sits in a %v px column, want %d px, the width the fit is checked at", name, w, tileColumnPx)
		}
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
		"a dated time in a 300 px tile":    {"Oct 12, 02:30", 28, 296, 1, true},
		"a stop wrapped onto two lines":    {"Stopped: API budget", 28, 296, 2, true},
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
		text := field(p.el, "spec", "vizConfig", "spec", "options", "text")
		if field(text, "valueSize") != 28.0 || field(text, "titleSize") != 14.0 {
			t.Errorf("panel %q text = %v, want titleSize 14 and valueSize 28: a value fits by layout, never a smaller font", p.title, text)
		}
	}
}

// valueTexts is the text p's value mappings draw for each value.
func valueTexts(p *dashPanel) map[string]string {
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
	texts := valueTexts(&p)
	for name, tc := range map[string]struct {
		line map[string]string
		want string
	}{
		"complete":    {map[string]string{"msg": "scan complete", "degraded": "false"}, ""},
		"degraded":    {map[string]string{"msg": "scan complete", "degraded": "true"}, "Degraded"},
		"budget stop": {map[string]string{"msg": "scan stopped", "degraded": "false", "reason": stopReadReserve}, "Stopped: API budget"},
		"other stop":  {map[string]string{"msg": "scan stopped", "degraded": "true", "reason": stopRateLimited}, "Degraded"},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			line := maps.Clone(tc.line)
			line["connection"] = "gh"
			m := &lokiModel{lines: []lokiLine{newLokiLine(t, 1000, line)}, at: 5000, rangeWindow: time.Hour, maxSeries: lokiMaxSeries}
			got, err := m.eval(p.queries[0].expr)
			if err != nil {
				t.Fatalf("Scan integrity: %v", err)
			}
			shown := ""
			if len(got) == 1 {
				shown = texts[strconv.FormatFloat(got[0].value, 'f', -1, 64)]
			}
			if len(got) > 1 || shown != tc.want {
				t.Errorf("Scan integrity shows %q (%v) for a %s, want %q, nothing for a whole scan", shown, got, name, tc.want)
			}
		})
	}
	if worst := []string{"Stopped: API budget", "Degraded"}; !slices.Equal([]string{texts["1"], texts["2"]}, worst) {
		t.Errorf("Scan integrity verdicts 1, 2 read %q %q, want %v in rising order, so max shows the worst", texts["1"], texts["2"], worst)
	}
	raw := field(p.el, "spec", "vizConfig", "spec", "fieldConfig", "defaults", "thresholds", "steps")
	steps, _ := raw.([]any)
	colors := map[float64]string{}
	for _, s := range steps {
		if v, ok := field(s, "value").(float64); ok {
			colors[v], _ = field(s, "color").(string)
		}
	}
	if want := map[float64]string{1: "orange", 2: "red"}; !maps.Equal(colors, want) {
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

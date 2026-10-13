package collect

import (
	"slices"
	"strconv"
	"testing"
)

// thinCollapsibleRows is each row of rows showing its header, which
// collapses it, over fewer than two panels that show whatever their data, as
// "<tab> / <row>". A grid item always shows; an auto-grid item shows unless
// it carries a show rule.
func thinCollapsibleRows(rows []layoutRow) []string {
	var out []string
	for _, r := range rows {
		if r.spec["hideHeader"] == true {
			continue
		}
		items, _ := field(r.spec, "layout", "spec", "items").([]any)
		shown := 0
		for _, it := range items {
			if field(it, "spec", "conditionalRendering") == nil {
				shown++
			}
		}
		if shown < 2 {
			out = append(out, r.tab+" / "+r.name)
		}
	}
	return out
}

func TestDashboard_every_collapsible_row_holds_two_panels_that_always_show(t *testing.T) {
	rows, _ := dashboardLayout(loadDashboard(t))
	headed := 0
	for _, r := range rows {
		if r.spec["hideHeader"] != true {
			headed++
		}
	}
	if headed == 0 {
		t.Fatalf("Setup: %s has no row that shows its header", dashboardPath)
	}
	if thin := thinCollapsibleRows(rows); len(thin) > 0 {
		t.Errorf("rows %v collapse over fewer than two panels that always show, want them merged into a neighbour or their header hidden", thin)
	}
	item := func(rule any) any {
		spec := map[string]any{}
		if rule != nil {
			spec["conditionalRendering"] = rule
		}
		return map[string]any{"spec": spec}
	}
	row := func(hideHeader bool, items ...any) layoutRow {
		return layoutRow{tab: "T", name: "R", spec: map[string]any{"hideHeader": hideHeader, "layout": map[string]any{"spec": map[string]any{"items": items}}}}
	}
	gate := map[string]any{"kind": "ConditionalRenderingGroup"}
	for name, tc := range map[string]struct {
		row  layoutRow
		want []string
	}{
		"one panel":                    {row: row(false, item(nil)), want: []string{"T / R"}},
		"one panel beside hidden ones": {row: row(false, item(nil), item(gate), item(gate)), want: []string{"T / R"}},
		"two panels":                   {row: row(false, item(nil), item(nil))},
		"one panel with no header":     {row: row(true, item(nil))},
	} {
		if got := thinCollapsibleRows([]layoutRow{tc.row}); !slices.Equal(got, tc.want) {
			t.Errorf("thinCollapsibleRows(%s) = %v, want %v", name, got, tc.want)
		}
	}
}

// Grafana 13.2.3 geometry, measured on a render at 1280 px: a panel's title
// bar and padding take 50 px, a table's header 34 px and each of its rows 36
// px at cellHeight sm, a table legend's header and each of its rows 24 px,
// and a grid item h units tall is 38h - 8 px.
const (
	panelChromePx = 50
	tableHeaderPx = 34
	tableRowPx    = 36
	legendRowPx   = 24
	gridUnitPx    = 38
	gridGapPx     = 8
)

// shortLists are the tables and table legends cut to about 8 rows, as
// "<tab> / <title> / <viz>"; the rest of each list scrolls.
var shortLists = []string{
	"CI runs / Failed runs per day by repository / timeseries",
	"Open work / Open pull requests / table", "Open work / Open issues / table", "Open work / PRs and issues by repository / table",
}

// matchedHeights maps a chart to the panel whose height it keeps: a chart
// beside a short list, and each Backlog chart the over-time chart of its
// family above it.
var matchedHeights = map[string]string{
	"CI runs / Runs created per day by result / timeseries": "CI runs / Failed runs per day by repository / timeseries",
	"Open work / Open pull requests by age / timeseries":    "Open work / Open pull requests over time / timeseries",
	"Open work / Open issues by age / timeseries":           "Open work / Open issues over time / timeseries",
}

// panelHeights is each placed element's height in px: a grid item's units,
// or its auto grid's custom row height.
func panelHeights(rows []layoutRow) map[string]float64 {
	out := map[string]float64{}
	for _, r := range rows {
		layout := field(r.spec, "layout")
		rowHeight, _ := field(layout, "spec", "rowHeight").(float64)
		items, _ := field(layout, "spec", "items").([]any)
		for _, it := range items {
			name, _ := field(it, "spec", "element", "name").(string)
			switch field(it, "kind") {
			case "GridLayoutItem":
				h, _ := field(it, "spec", "height").(float64)
				out[name] = h*gridUnitPx - gridGapPx
			case "AutoGridLayoutItem":
				out[name] = rowHeight
			}
		}
	}
	return out
}

// panelsByKey is each panel of d by "<tab> / <title> / <viz>".
func panelsByKey(d map[string]any) map[string]dashPanel {
	tabs := panelTabs(d)
	out := map[string]dashPanel{}
	for _, p := range dashboardPanels(d) {
		out[tabs[p.name]+" / "+p.title+" / "+p.viz] = p
	}
	return out
}

// shownRows is how many rows of p's list show whole or in part at heightPx,
// below the header of its table or table legend, or -1 for a panel with
// neither.
func shownRows(p *dashPanel, heightPx float64) float64 {
	switch {
	case p.viz == "table":
		return (heightPx - panelChromePx - tableHeaderPx) / tableRowPx
	case field(p.el, "spec", "vizConfig", "spec", "options", "legend", "displayMode") == "table":
		return (heightPx - panelChromePx - legendRowPx) / legendRowPx
	}
	return -1
}

// TestDashboard_short_lists_show_seven_rows_and_part_of_the_eighth pins each
// list's height: cut through its eighth row, a list shows that it scrolls on.
func TestDashboard_short_lists_show_seven_rows_and_part_of_the_eighth(t *testing.T) {
	d := loadDashboard(t)
	rows, _ := dashboardLayout(d)
	heights, byKey := panelHeights(rows), panelsByKey(d)
	for _, key := range shortLists {
		p, ok := byKey[key]
		if !ok {
			t.Errorf("no panel %s", key)
			continue
		}
		if n := shownRows(&p, heights[p.name]); n < 7 || n >= 8 {
			t.Errorf("%s is %v px tall, showing %s rows, want 7 whole and part of the 8th", key, heights[p.name], strconv.FormatFloat(n, 'f', 2, 64))
		}
	}
}

func TestDashboard_charts_keep_the_height_of_the_chart_they_match(t *testing.T) {
	d := loadDashboard(t)
	rows, _ := dashboardLayout(d)
	heights, byKey := panelHeights(rows), panelsByKey(d)
	for chart, other := range matchedHeights {
		c, ok := byKey[chart]
		if o, found := byKey[other]; !ok || !found || heights[c.name] != heights[o.name] {
			t.Errorf("%s is %v px tall, want the %v px of %s", chart, heights[c.name], heights[o.name], other)
		}
	}
}

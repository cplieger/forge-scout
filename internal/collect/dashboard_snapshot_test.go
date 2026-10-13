package collect

import (
	"cmp"
	"log/slog"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/cplieger/github-scout/internal/forge"
	"github.com/cplieger/slogx/capture"
)

// A connection whose scans stop at the API budget logs no rows or counts, so
// every snapshot read (count tiles, item tables, read-state tables and the
// tool table) reads each connection's newest scan complete over a fixed
// lookback, and keeps a row only when its scan_id is that scan's. Loki
// returns a log query's newest lines across every connection, so no snapshot
// table is a log query.

// snapshotLookback is the fixed window of every snapshot read.
const snapshotLookback = "7d"

// healthTab follows the time picker, its panels telling the scans apart,
// except the counts and coverage it shows from the fixed lookback.
const healthTab = "Scout health"

// volatileLabels change from scan to scan or name a line's place in its
// list, so a series per value multiplies a family's series past Loki's
// limit; a snapshot read carries them as values, if at all.
var volatileLabels = []string{"age_days", "idle_days", "age", "rank", "forge_rank", "runs", "p50_seconds", "p95_seconds", "consecutive_failures", "streak", "alerts"}

// snapshotLabels are the labels each family's rows may carry: the item's
// identity and what the dashboard derives from it alone.
var snapshotLabels = map[string][]string{
	"open pull request": {"connection", "forge", "repo", "number", "ref", "title", "author", "from", "tags", "draft", "checks", "url", "created_at", "updated_at", "link", "item"},
	"open issue":        {"connection", "forge", "repo", "number", "ref", "title", "author", "from", "tags", "url", "created_at", "updated_at", "link", "item"},
	// A failing workflow's url and run_id are its newest failed run, which
	// changes only with a new failure.
	"failing workflow": {"connection", "forge", "repo", "workflow", "branch", "state", "run_id", "url", "last_run_at", "failing_since", "failing_since_clipped", "link"},
	// A slow workflow's url is its slowest run, which changes as the window
	// slides, so it is not a label.
	"slow workflow":      {"connection", "forge", "repo", "workflow"},
	"disabled workflow":  {"connection", "forge", "repo", "workflow", "path", "state", "url", "link"},
	"security alert":     {"connection", "forge", "repo", "number", "severity", "rule", "tool", "url", "created_at", "link"},
	"top security alert": {"connection", "forge", "repo", "number", "severity", "rule", "tool", "url", "created_at", "link"},
	"security tool":      {"connection", "forge", "tool"},
}

// snapshotIdentity is the labels naming one item of each family. A read
// carrying any other label keeps one series per item through topk by
// (identity) (1, ...): a label that changes between scans, such as
// updated_at, would otherwise give an item a series per value, and the join
// operand would reach Loki's series limit far below the item count.
var snapshotIdentity = map[string][]string{
	"open pull request":  {"connection", "forge", "repo", "number"},
	"open issue":         {"connection", "forge", "repo", "number"},
	"failing workflow":   {"connection", "forge", "repo", "workflow", "branch"},
	"slow workflow":      {"connection", "forge", "repo", "workflow"},
	"disabled workflow":  {"connection", "forge", "repo", "workflow", "path"},
	"security alert":     {"connection", "forge", "repo", "number"},
	"top security alert": {"connection", "forge", "repo", "number"},
	"security tool":      {"connection", "forge", "tool"},
}

// loggedLines is every line rec holds, oldest first, each with its msg.
func loggedLines(rec *capture.Recorder) []map[string]string {
	var out []map[string]string
	for _, r := range rec.Records() {
		m := map[string]string{"msg": r.Message}
		r.Attrs(func(a slog.Attr) bool {
			m[a.Key] = a.Value.Resolve().String()
			return true
		})
		out = append(out, m)
	}
	return out
}

// panelTabs is the tab each element sits in.
func panelTabs(d map[string]any) map[string]string {
	out := map[string]string{}
	var walk func(v any, tab string)
	walk = func(v any, tab string) {
		switch x := v.(type) {
		case map[string]any:
			if x["kind"] == "TabsLayoutTab" {
				tab, _ = field(x, "spec", "title").(string)
			}
			if x["kind"] == "ElementReference" {
				name, _ := x["name"].(string)
				out[name] = tab
			}
			for _, y := range x {
				walk(y, tab)
			}
		case []any:
			for _, y := range x {
				walk(y, tab)
			}
		}
	}
	walk(field(d, "spec", "layout"), "")
	return out
}

// snapshotPanel reports a panel that shows each connection's current state:
// a count tile, or a table of snapshot rows or of read states, outside
// Scout health or reading the fixed lookback there.
func snapshotPanel(p *dashPanel, tab string) bool {
	if tab == healthTab && !slices.ContainsFunc(p.queries, func(q dashQuery) bool { return strings.Contains(q.expr, "["+snapshotLookback+"]") }) {
		return false
	}
	switch p.viz {
	case "stat":
		return countTile(p)
	case "table":
		return slices.ContainsFunc(p.queries, func(q dashQuery) bool {
			return slices.ContainsFunc(snapshotMessages, func(msg string) bool { return strings.Contains(q.expr, "msg=`"+msg+"`") }) ||
				(strings.Contains(q.expr, "msg=`scan complete`") && strings.Contains(q.expr, `_read="`))
		})
	}
	return false
}

// rangeMessages is the msg values a range aggregation's pipeline selects.
func rangeMessages(r *rangeNode) []string {
	msgs, _ := pipelineMessages(r.pipeline[strings.Index(r.pipeline, "}")+1:])
	return msgs
}

func unwrapped(r *rangeNode) string {
	if m := unwrapRe.FindStringSubmatch(r.pipeline); m != nil {
		return m[1]
	}
	return ""
}

// newestComplete reports each connection's newest scan complete over the
// lookback, by its scan_id.
func newestComplete(n logqlNode) bool {
	r, ok := n.(rangeNode)
	return ok && r.fn == "last" && r.window == snapshotLookback && slices.Equal(r.by, []string{"connection"}) &&
		slices.Equal(rangeMessages(&r), []string{"scan complete"}) && unwrapped(&r) == "scan_id"
}

// newestJoin reports a row read kept only where its scan_id is its own
// connection's newest complete scan.
func newestJoin(b *binNode) bool {
	return b.op == "==" && b.matched && slices.Equal(b.on, []string{"connection"}) && b.groupLeft && newestComplete(b.rhs)
}

// joinsUnder reports whether n holds a newest-complete join of a read over
// msg, or of any read when msg is empty.
func joinsUnder(n logqlNode, msg string) bool {
	for _, r := range rangeNodes(n) {
		if len(r.path) > 0 && r.path[0].left && newestJoin(&r.path[0].bin) && (msg == "" || slices.Contains(rangeMessages(&r.node), msg)) {
			return true
		}
	}
	return false
}

// keptByJoin reports a read that is the left side of a newest-complete join,
// or the left side of an and keeping only the series of such a join.
func keptByJoin(r *rangeInContext, msg string) bool {
	if len(r.path) == 0 || !r.path[0].left {
		return false
	}
	b := &r.path[0].bin
	if newestJoin(b) {
		return unwrapped(&r.node) == "scan_id"
	}
	return b.op == "and" && b.matched && slices.Equal(b.on, r.node.by) && joinsUnder(b.rhs, msg)
}

// fallbackArm reports a scan stopped read that only stands in where a
// connection has no complete scan: the right side of an or on connection,
// reading NaN for the count it unwraps or partial for a read the stop
// finished, or the stop an As of compares the newest complete scan with.
func fallbackArm(r *rangeInContext) bool {
	for _, st := range r.path {
		b := &st.bin
		if st.left || !b.matched || !slices.Equal(b.on, []string{"connection"}) {
			continue
		}
		u := unwrapped(&r.node)
		switch b.op {
		case "or":
			return stopFormat(r.node.pipeline, u) == "NaN" ||
				(u == "scan_id" && strings.Contains(r.node.pipeline, `"complete" }}partial{{ else }}`))
		case "<":
			return u == "scan_id"
		}
	}
	return false
}

// stopFormat is what pipeline's label_format of f makes of a stop that
// lacks no read, or "" with no label_format of f.
func stopFormat(pipeline, f string) string {
	m := regexp.MustCompile(`\|\s*label_format\s+` + regexp.QuoteMeta(f) + "=`([^`]*)`").FindStringSubmatch(pipeline)
	if m == nil {
		return ""
	}
	tmpl, err := template.New(f).Option("missingkey=zero").Parse(m[1])
	if err != nil {
		return ""
	}
	var b strings.Builder
	if tmpl.Execute(&b, map[string]string{}) != nil {
		return ""
	}
	return b.String()
}

// checkSnapshotQuery applies the snapshot rule to one query of a snapshot
// panel.
func checkSnapshotQuery(q *dashQuery) []string {
	var problems []string
	if logQueryRe.MatchString(q.expr) || q.maxLines > 0 {
		problems = append(problems, "a snapshot read is a log query, which Loki cuts at its line limit across every connection")
	}
	if !q.instant {
		problems = append(problems, "a snapshot read is a range query")
	}
	for _, macro := range []string{"$__range", "$__interval"} {
		if strings.Contains(q.expr, macro) {
			problems = append(problems, "a snapshot read follows the time picker ("+macro+"), so a narrow range during a stop loses the last complete scan")
		}
	}
	n, err := parseLogQL(q.expr)
	if err != nil {
		return append(problems, "the query does not parse: "+err.Error())
	}
	reads := rangeNodes(n)
	if len(reads) == 0 {
		problems = append(problems, "no range aggregation")
	}
	for i := range reads {
		r := &reads[i]
		if r.node.window != snapshotLookback {
			problems = append(problems, "a read over ["+r.node.window+"], want the fixed ["+snapshotLookback+"]")
		}
		problems = append(problems, checkSnapshotRead(r)...)
	}
	return problems
}

func checkSnapshotRead(r *rangeInContext) []string {
	msgs := rangeMessages(&r.node)
	switch {
	case len(msgs) == 1 && snapshotLabels[msgs[0]] != nil:
		var problems []string
		if !keptByJoin(r, msgs[0]) {
			problems = append(problems, "a read of "+msgs[0]+" rows is not kept to its connection's newest scan complete (== on (connection) group_left ())")
		}
		if !slices.Contains(r.node.by, "connection") {
			problems = append(problems, "a read of "+msgs[0]+" rows does not keep connection")
		}
		for _, l := range r.node.by {
			if !slices.Contains(snapshotLabels[msgs[0]], l) {
				problems = append(problems, "a read of "+msgs[0]+" rows carries the label "+l+", which its family does not allow")
			}
		}
		ident := snapshotIdentity[msgs[0]]
		extra := slices.ContainsFunc(r.node.by, func(l string) bool { return !slices.Contains(ident, l) })
		if p := r.parent; extra && (p == nil || p.op != "topk" || p.param != 1 || !slices.Equal(slices.Sorted(slices.Values(p.by)), slices.Sorted(slices.Values(ident)))) {
			problems = append(problems, "a read of "+msgs[0]+" rows by ("+strings.Join(r.node.by, ", ")+") is not kept to one series per item by topk by ("+strings.Join(ident, ", ")+") (1, ...)")
		}
		return problems
	case slices.Equal(msgs, []string{"scan complete"}):
		var problems []string
		perState := false
		for _, l := range r.node.by {
			switch {
			case l == "connection" || l == "forge":
			case strings.HasSuffix(l, "_read"):
				perState = true
			default:
				problems = append(problems, "a scan complete read carries the label "+l+", so one connection has a series per value")
			}
		}
		if !slices.Contains(r.node.by, "connection") {
			problems = append(problems, "a scan complete read is not per connection")
		}
		if perState && !keptByJoin(r, "") {
			problems = append(problems, "a read state is not kept to its connection's newest scan complete, so an older scan's state shows beside it")
		}
		return problems
	case slices.Equal(msgs, []string{"scan stopped"}):
		if !fallbackArm(r) {
			return []string{"a scan stopped read stands in for a complete scan: only a NaN or partial arm for a connection with no complete scan, or the stop an As of compares with, may read one"}
		}
		return nil
	}
	return []string{"a snapshot read selects " + strings.Join(msgs, " and ") + ", want a snapshot family or scan complete alone"}
}

func TestDashboard_snapshot_reads_keep_each_connections_newest_complete_scan(t *testing.T) {
	d := loadDashboard(t)
	tabs := panelTabs(d)
	kinds := map[string]int{}
	for _, p := range dashboardPanels(d) {
		if !snapshotPanel(&p, tabs[p.name]) {
			continue
		}
		kinds[p.viz]++
		joined := false
		for i := range p.queries {
			q := &p.queries[i]
			for _, problem := range checkSnapshotQuery(q) {
				t.Errorf("panel %q query %s: %s\nexpr: %s", p.title, q.refID, problem, q.expr)
			}
			if n, err := parseLogQL(q.expr); err == nil && joinsUnder(n, "") {
				joined = true
			}
		}
		if p.viz == "table" && !joined {
			t.Errorf("panel %q joins no read on its connection's newest scan complete", p.title)
		}
	}
	if kinds["stat"] < 20 || kinds["table"] < 11 {
		t.Fatalf("Setup: %d count tiles and %d snapshot tables found, want every one", kinds["stat"], kinds["table"])
	}
}

func TestDashboard_snapshot_label_lists_leave_out_what_changes_every_scan(t *testing.T) {
	for msg, labels := range snapshotLabels {
		for _, l := range labels {
			if slices.Contains(volatileLabels, l) {
				t.Errorf("%s rows may carry %s, which multiplies their series", msg, l)
			}
		}
	}
	if slices.Contains(snapshotLabels["slow workflow"], "url") {
		t.Error("slow workflow rows may carry url, the slowest run, which changes as the window slides")
	}
	if want := slices.Sorted(maps.Keys(snapshotLabels)); !slices.Equal(want, slices.Sorted(slices.Values(snapshotMessages))) {
		t.Errorf("label lists name %v, want every snapshot family %v", want, snapshotMessages)
	}
	if want := slices.Sorted(maps.Keys(snapshotLabels)); !slices.Equal(want, slices.Sorted(maps.Keys(snapshotIdentity))) {
		t.Errorf("identities name %v, want every snapshot family %v", slices.Sorted(maps.Keys(snapshotIdentity)), want)
	}
	for msg, ident := range snapshotIdentity {
		for _, l := range ident {
			if !slices.Contains(snapshotLabels[msg], l) {
				t.Errorf("%s's identity names %s, which its rows may not carry", msg, l)
			}
		}
	}
}

func TestDashboard_snapshot_check_rejects(t *testing.T) {
	const sel = `{container="forge-scout"}`
	newest := "last_over_time(" + sel + " |= `scan complete` | json msg=\"msg\", connection=\"connection\", scan_id=\"scan_id\" | msg=`scan complete` | unwrap scan_id | __error__=\"\" [7d]) by (connection)"
	rows := func(by, window string) string {
		return "last_over_time(" + sel + " |= `open issue` | json msg=\"msg\", connection=\"connection\", scan_id=\"scan_id\", repo=\"repo\", number=\"number\", age_days=\"age_days\", forge_rank=\"forge_rank\" | msg=`open issue` | unwrap scan_id | __error__=\"\" [" + window + "]) by (" + by + ")"
	}
	slow := func(by string) string {
		return "last_over_time(" + sel + " |= `slow workflow` | json msg=\"msg\", connection=\"connection\", scan_id=\"scan_id\", repo=\"repo\", workflow=\"workflow\", url=\"url\", runs=\"runs\" | msg=`slow workflow` | unwrap scan_id | __error__=\"\" [7d]) by (" + by + ")"
	}
	count := func(msgs, window string) string {
		return "sum(last_over_time(" + sel + " |= `scan ` | json msg=\"msg\", connection=\"connection\", open_issues=\"open_issues\" | " + msgs + " | unwrap open_issues | __error__=\"\" [" + window + "]) by (connection))"
	}
	stopCount := "last_over_time(" + sel + " |= `scan stopped` | json msg=\"msg\", connection=\"connection\", budget_remaining=\"budget_remaining\" | msg=`scan stopped` | unwrap budget_remaining | __error__=\"\" [7d]) by (connection)"
	state := "last_over_time(" + sel + " |= `scan complete` | json msg=\"msg\", connection=\"connection\", scan_id=\"scan_id\", open_issues_read=\"open_issues_read\" | msg=`scan complete` | unwrap scan_id | __error__=\"\" [7d]) by (connection, open_issues_read)"
	good := map[string]string{
		"joined rows":         rows("connection, repo, number", "7d") + " == on (connection) group_left () " + newest,
		"one series per item": "topk by (connection, forge, repo, number) (1, " + rows("connection, forge, repo, number, title", "7d") + ") == on (connection) group_left () " + newest,
		"whole count":         count("msg=`scan complete`", "7d"),
		"joined read state":   state + " == on (connection) group_left () " + newest,
		"count kept by the rows": "last_over_time(" + sel + " |= `slow workflow` | json msg=\"msg\", connection=\"connection\", repo=\"repo\", workflow=\"workflow\", runs=\"runs\" | msg=`slow workflow` | unwrap runs | __error__=\"\" [7d]) by (connection, repo, workflow)" +
			" and on (connection, repo, workflow) (" + slow("connection, repo, workflow") + " == on (connection) group_left () " + newest + ")",
	}
	for name, expr := range good {
		if problems := checkSnapshotQuery(&dashQuery{expr: expr, instant: true}); len(problems) != 0 {
			t.Errorf("checkSnapshotQuery(%s) = %v, want no problem", name, problems)
		}
	}
	bad := map[string]dashQuery{
		"log query":                    {expr: sel + " |= `open issue` | json msg=\"msg\", scan_id=\"scan_id\" | msg=`open issue`", maxLines: 201},
		"range query":                  {expr: good["joined rows"]},
		"picker window":                {expr: rows("connection, repo, number", "$__range") + " == on (connection) group_left () " + newest, instant: true},
		"not joined":                   {expr: rows("connection, repo, number", "7d"), instant: true},
		"joined on any scan":           {expr: rows("connection, repo, number", "7d") + " == on (connection) group_left () " + strings.ReplaceAll(newest, "msg=`scan complete` |", scanEnds+" |"), instant: true},
		"joined on a stop":             {expr: rows("connection, repo, number", "7d") + " == on (connection) group_left () " + strings.ReplaceAll(newest, "scan complete", "scan stopped"), instant: true},
		"no connection":                {expr: rows("repo, number", "7d") + " == on (repo) group_left () " + newest, instant: true},
		"age as a label":               {expr: rows("connection, repo, number, age_days", "7d") + " == on (connection) group_left () " + newest, instant: true},
		"rank as a label":              {expr: rows("connection, repo, number, forge_rank", "7d") + " == on (connection) group_left () " + newest, instant: true},
		"slowest run's url":            {expr: slow("connection, repo, workflow, url") + " == on (connection) group_left () " + newest, instant: true},
		"run count as a label":         {expr: slow("connection, repo, workflow, runs") + " == on (connection) group_left () " + newest, instant: true},
		"count over any scan":          {expr: count(scanEnds, "7d"), instant: true},
		"count over the picker":        {expr: count("msg=`scan complete`", "$__range"), instant: true},
		"a stop's count":               {expr: count("msg=`scan complete`", "7d") + " or on (connection) " + stopCount, instant: true},
		"unjoined read state":          {expr: state, instant: true},
		"a series per title":           {expr: rows("connection, forge, repo, number, title", "7d") + " == on (connection) group_left () " + newest, instant: true},
		"topk over less than the item": {expr: "topk by (connection) (1, " + rows("connection, forge, repo, number, title", "7d") + ") == on (connection) group_left () " + newest, instant: true},
	}
	for name, q := range bad {
		t.Run(name, func(t *testing.T) {
			if problems := checkSnapshotQuery(&q); len(problems) == 0 {
				t.Errorf("checkSnapshotQuery(%s) reported nothing, want a violation\nexpr: %s", name, q.expr)
			}
		})
	}
}

// snapshotWorld is every line four connections log over a week of scans at
// 15m: gh and gh2 on GitHub, gl on GitLab and gt on Gitea. gh's scans stop
// at the budget reserve for the last 3 days. Each connection opens pull
// request 4 and code scanning alert 4 four days in, after gh's last
// complete scan, and pull request 3's last activity moves every day.
type snapshotWorld struct {
	// complete is each connection's scan_ids that logged scan complete.
	complete map[string][]int64
	// end is the newest scan's start and firstStop the start of gh's first
	// stopped scan.
	end, firstStop time.Time
	lines          []lokiLine
}

// sharedWorld is built once: its scans are a pure function of the code
// under test, and a week of them takes seconds.
var sharedWorld struct {
	once  sync.Once
	world *snapshotWorld
}

func theSnapshotWorld(t *testing.T) *snapshotWorld {
	t.Helper()
	sharedWorld.once.Do(func() { sharedWorld.world = buildSnapshotWorld(t) })
	if sharedWorld.world == nil {
		t.Fatal("Setup: the snapshot world failed to build in an earlier test")
	}
	return sharedWorld.world
}

func gitlabEndpoint(name string) *endpoint {
	return &endpoint{
		cfg: connCfg(name, "o"),
		conn: &fakeConn{
			product: forge.ProductGitLab, login: "me", budget: forge.Budget{Remaining: -1},
			repos: map[string][]forge.Repo{"o": {{Path: "o/r", DefaultBranch: "main"}}},
		},
	}
}

const (
	worldScans    = 7 * 24 * 4
	worldInterval = 15 * time.Minute
	worldStops    = 3 * 24 * 4
)

func buildSnapshotWorld(t *testing.T) *snapshotWorld {
	t.Helper()
	gh, gh2, gl, gt := githubEndpoint("gh"), githubEndpoint("gh2"), gitlabEndpoint("gl"), giteaEndpoint("gt")
	eps := []*endpoint{gh, gh2, gl, gt}
	h := newHarness(t, eps...)
	w := &snapshotWorld{complete: map[string][]int64{}}
	start := now
	w.firstStop = start.Add(time.Duration(worldScans-worldStops) * worldInterval)
	for i := range worldScans {
		at := start.Add(time.Duration(i) * worldInterval)
		*h.clock = at
		for _, ep := range eps {
			worldItems(ep, start, at)
		}
		gh.conn.errs = nil
		if i >= worldScans-worldStops {
			gh.conn.errs = map[string]error{"runs o/r": forge.ErrReadDeferred}
		}
		h.scan(t)
		w.add(t, at, loggedLines(h.rec))
		w.end = at
	}
	if len(w.complete["gh"]) == 0 || w.complete["gh"][len(w.complete["gh"])-1] >= w.firstStop.UnixMilli() {
		t.Fatalf("Setup: gh's complete scans %d, want its last one before its stops", len(w.complete["gh"]))
	}
	return w
}

// add records one scan's lines at the scan's start, in the order logged.
func (w *snapshotWorld) add(t *testing.T, at time.Time, lines []map[string]string) {
	t.Helper()
	for i, l := range lines {
		w.lines = append(w.lines, newLokiLine(t, at.UnixMilli()+int64(i), l))
		if l["msg"] == "scan complete" {
			id, err := strconv.ParseInt(l["scan_id"], 10, 64)
			if err != nil {
				t.Fatalf("Setup: scan complete without scan_id: %v", l)
			}
			w.complete[l["connection"]] = append(w.complete[l["connection"]], id)
		}
	}
}

// worldItems sets what ep's forge answers at at.
func worldItems(ep *endpoint, start, at time.Time) {
	day := at.Truncate(24 * time.Hour)
	late := at.After(start.Add(4 * 24 * time.Hour))
	prs := []forge.PullRequest{
		{Repo: "o/r", Number: 1, Ref: "#1", Title: "first", Author: "me", HeadSHA: "h1", CreatedAt: start.Add(-40 * 24 * time.Hour), UpdatedAt: start.Add(-35 * 24 * time.Hour)},
		{Repo: "o/r", Number: 2, Ref: "#2", Title: "second", Author: "stranger", HeadSHA: "h2", CreatedAt: start.Add(-3 * 24 * time.Hour), UpdatedAt: start.Add(-time.Hour)},
		{Repo: "o/r", Number: 3, Ref: "#3", Title: "busy", Author: "renovate[bot]", HeadSHA: "h3", CreatedAt: start.Add(-time.Hour), UpdatedAt: day},
	}
	if late {
		prs = append(prs, forge.PullRequest{Repo: "o/r", Number: 4, Ref: "#4", Title: "new", Author: "me", HeadSHA: "h4", CreatedAt: start.Add(4 * 24 * time.Hour), UpdatedAt: start.Add(4 * 24 * time.Hour)})
	}
	ep.conn.prs = map[string][]forge.PullRequest{"o": prs}
	ep.conn.checks = map[string]forge.CheckResult{"h1": {State: forge.CheckPassing}, "h2": {State: forge.CheckFailing}, "h3": {State: forge.CheckPending}, "h4": {State: forge.CheckNone}}
	ep.conn.issues = map[string][]forge.Issue{"o": {
		{Repo: "o/r", Number: 10, Ref: "#10", Title: "bug", Author: "stranger", CreatedAt: start.Add(-10 * 24 * time.Hour), UpdatedAt: start.Add(-9 * 24 * time.Hour)},
		{Repo: "o/r", Number: 11, Ref: "#11", Title: "idea", Author: "me", CreatedAt: start.Add(-100 * 24 * time.Hour), UpdatedAt: start.Add(-50 * 24 * time.Hour)},
	}}
	var runs []forge.Run
	for k := range 4 {
		created := at.Truncate(time.Hour).Add(-time.Duration(k) * time.Hour)
		id := created.Unix() / 60
		failing := run(id, forge.RunFailing, created)
		passing := run(id+5, forge.RunPassing, created.Add(time.Minute))
		passing.Workflow, passing.UpdatedAt = "Build", passing.StartedAt.Add(time.Duration(4+k)*time.Minute)
		runs = append(runs, failing, passing)
	}
	ep.conn.runs = map[string]listing{"o/r": {Runs: runs}}
	if q := ep.conn.quota; q != nil {
		q.remaining, q.reset = 4000, at.Add(time.Hour)
	}
	if ep.gh == nil {
		return
	}
	alerts := []forge.Alert{
		{Repo: "o/r", Number: 1, Rule: "go/sql-injection", SecuritySeverity: "critical", Tool: "CodeQL", Source: "code_scanning", CreatedAt: start.Add(-20 * 24 * time.Hour)},
		{Repo: "o/r", Number: 2, Rule: "G104", SecuritySeverity: "low", Tool: "gosec", Source: "code_scanning", CreatedAt: start.Add(-2 * 24 * time.Hour)},
		{Repo: "o/r", Number: 3, Rule: "CVE-1", SecuritySeverity: "high", Tool: "Trivy", Source: "code_scanning", CreatedAt: start.Add(-time.Hour)},
	}
	if late {
		alerts = append(alerts, forge.Alert{Repo: "o/r", Number: 4, Rule: "CVE-2", SecuritySeverity: "high", Tool: "Trivy", Source: "code_scanning", CreatedAt: start.Add(4 * 24 * time.Hour)})
	}
	ep.gh.alerts = map[string][]forge.Alert{"o/r": alerts}
	ep.gh.workflows = map[string][]forge.Workflow{"o/r": {
		{Repo: "o/r", Name: "CI", State: "active"},
		{Repo: "o/r", Name: "Build", State: "active"},
		{Repo: "o/r", Name: "nightly", Path: ".github/workflows/nightly.yaml", State: "disabled_inactivity", URL: "https://gh.test/o/r/actions/workflows/nightly.yaml"},
	}}
}

// newestCompleteAt is each connection's newest scan complete at or before
// at, within the lookback.
func (w *snapshotWorld) newestCompleteAt(at int64) map[string]int64 {
	out := map[string]int64{}
	for c, ids := range w.complete {
		for _, id := range ids {
			if id <= at && id > at-7*24*time.Hour.Milliseconds() {
				out[c] = max(out[c], id)
			}
		}
	}
	return out
}

func (w *snapshotWorld) model(at int64) *lokiModel {
	return &lokiModel{lines: w.lines, at: at, rangeWindow: time.Hour, maxSeries: lokiMaxSeries, vars: map[string]string{"connections": "gh|gh2|gl|gt"}}
}

// joinedRead is the row read of query expr that its newest-complete join
// keeps, the left side of the join.
func joinedRead(t *testing.T, expr string) rangeNode {
	t.Helper()
	n, err := parseLogQL(expr)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, r := range rangeNodes(n) {
		if len(r.path) > 0 && r.path[0].left && newestJoin(&r.path[0].bin) {
			return r.node
		}
	}
	t.Fatalf("Setup: no newest-complete join in %s", expr)
	return rangeNode{}
}

// truthRows is what read r gives over only each connection's newest
// complete scan's lines: the rows that scan logged, as the panel labels them.
func (w *snapshotWorld) truthRows(t *testing.T, r rangeNode, at int64) map[string]float64 {
	t.Helper()
	newest := w.newestCompleteAt(at)
	var lines []lokiLine
	for _, l := range w.lines {
		if id, err := strconv.ParseInt(l.fields["scan_id"], 10, 64); err == nil && id == newest[l.fields["connection"]] {
			lines = append(lines, l)
		}
	}
	m := w.model(at)
	m.lines = lines
	out, err := m.rangeAgg(&r)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return byLabels(out)
}

func byLabels(samples []sample) map[string]float64 {
	out := map[string]float64{}
	for _, s := range samples {
		out[labelKey(s.labels)] = s.value
	}
	return out
}

func TestDashboard_snapshot_tables_list_each_connections_newest_complete_rows(t *testing.T) {
	w := theSnapshotWorld(t)
	d := loadDashboard(t)
	tabs := panelTabs(d)
	for _, at := range []time.Time{w.end.Add(time.Minute), w.firstStop.Add(12*time.Hour + time.Minute)} {
		newest := w.newestCompleteAt(at.UnixMilli())
		if newest["gh"] >= w.firstStop.UnixMilli() || newest["gh2"] < at.Add(-time.Hour).UnixMilli() {
			t.Fatalf("Setup: newest complete scans at %v = %v, want gh's before its stops and gh2's within the hour", at, newest)
		}
		tables := 0
		for _, p := range dashboardPanels(d) {
			if p.viz != "table" || !snapshotPanel(&p, tabs[p.name]) || !slices.ContainsFunc(p.queries, func(q dashQuery) bool {
				return slices.ContainsFunc(snapshotMessages, func(msg string) bool { return strings.Contains(q.expr, "msg=`"+msg+"`") })
			}) {
				continue
			}
			tables++
			for _, q := range p.queries {
				got, err := w.model(at.UnixMilli()).eval(q.expr)
				if err != nil {
					t.Errorf("panel %q query %s at %v: %v", p.title, q.refID, at, err)
					continue
				}
				if q.refID != "A" || strings.HasPrefix(q.expr, "sum by") || strings.HasPrefix(q.expr, "count by") {
					continue
				}
				want := w.truthRows(t, joinedRead(t, w.model(at.UnixMilli()).prepare(q.expr)), at.UnixMilli())
				if g := byLabels(got); !maps.Equal(g, want) {
					t.Errorf("panel %q at %v lists %d rows, want the %d rows of each connection's newest complete scan\ngot  %v\nwant %v", p.title, at, len(g), len(want), slices.Sorted(maps.Keys(g)), slices.Sorted(maps.Keys(want)))
				}
				gh := 0
				for _, s := range got {
					if s.labels["connection"] != "gh" {
						continue
					}
					gh++
					if int64(s.value) != newest["gh"] {
						t.Errorf("panel %q dates gh's row %v at scan %v, want As of its last complete scan %d", p.title, s.labels, s.value, newest["gh"])
					}
					if s.labels["number"] == "4" {
						t.Errorf("panel %q lists gh's %v, which only scans after its last complete one saw", p.title, s.labels)
					}
				}
				if gh == 0 {
					t.Errorf("panel %q at %v lists no row of gh, whose scans stopped after a complete one", p.title, at)
				}
			}
		}
		if tables < 9 {
			t.Fatalf("Setup: %d item tables evaluated, want every one", tables)
		}
	}
}

func TestDashboard_count_tiles_add_each_connections_newest_complete_count(t *testing.T) {
	w := theSnapshotWorld(t)
	d := loadDashboard(t)
	at := w.end.Add(time.Minute).UnixMilli()
	newest := w.newestCompleteAt(at)
	gated := gatedPanels(d)
	tiles := 0
	for _, p := range dashboardPanels(d) {
		if p.title == asOfTitle {
			if got, err := w.model(at).eval(p.queries[0].expr); err != nil || len(got) != 1 || int64(got[0].value) != newest["gh"] {
				t.Errorf("panel %q = %v (%v), want gh's last complete scan %d: gh is the one connection stopped since", p.title, got, err, newest["gh"])
			}
		}
		if !countTile(&p) {
			continue
		}
		tiles++
		_, hides := gated[p.name]
		for _, q := range p.queries {
			got, err := w.model(at).eval(q.expr)
			if err != nil {
				t.Errorf("panel %q query %s: %v", p.title, q.refID, err)
				continue
			}
			want := 0.0
			for _, r := range rangeNodes(mustParse(t, w.model(at).prepare(q.expr))) {
				if slices.Equal(rangeMessages(&r.node), []string{"scan complete"}) {
					for _, v := range w.truthRows(t, r.node, at) {
						if strconv.FormatFloat(v, 'f', -1, 64) != tileDropped {
							want += v
						}
					}
				}
			}
			if hides && want == 0 {
				if len(got) != 0 {
					t.Errorf("panel %q query %s = %v, want no data at a sum of 0, so the tile hides", p.title, q.refID, got)
				}
				continue
			}
			if len(got) != 1 || got[0].value != want {
				t.Errorf("panel %q query %s = %v, want %v, the sum of each connection's newest complete count", p.title, q.refID, got, want)
			}
		}
	}
	if tiles < 20 {
		t.Fatalf("Setup: %d count tiles evaluated, want every one", tiles)
	}
}

// tileCounts is the counts a count tile unwraps and the reads they are
// read through.
func tileCounts(p *dashPanel) (counts, reads []string) {
	for _, q := range p.queries {
		for _, m := range unwrapRe.FindAllStringSubmatch(q.expr, -1) {
			if m[1] != "scan_id" && !slices.Contains(counts, m[1]) {
				counts = append(counts, m[1])
				for _, r := range countReads(m[1]) {
					if !slices.Contains(reads, r) {
						reads = append(reads, r)
					}
				}
			}
		}
	}
	return counts, reads
}

// tileScanLine is a scan line setting every read of p's tile to state and,
// unless count is "", every count it sums to count.
func tileScanLine(msg, connection string, id int64, state, count string, p *dashPanel) map[string]string {
	counts, reads := tileCounts(p)
	l := map[string]string{"msg": msg, "connection": connection, "scan_id": strconv.FormatInt(id, 10)}
	for _, r := range reads {
		l[r] = state
	}
	if count != "" {
		for _, c := range counts {
			l[c] = count
		}
	}
	return l
}

func TestDashboard_count_tiles_add_nothing_of_a_connection_whose_newest_scan_lacks_the_read(t *testing.T) {
	tiles := 0
	for _, p := range dashboardPanels(loadDashboard(t)) {
		if !countTile(&p) {
			continue
		}
		tiles++
		for _, state := range []string{"excluded", "unsupported"} {
			// b counts 2 throughout. a counted 5 and stopped since, then
			// its newest scan, a complete one or a stop, lacks the read.
			b := tileScanLine("scan complete", "b", 2500, "complete", "2", &p)
			worlds := map[string][]map[string]string{
				"newest complete scan": {
					tileScanLine("scan complete", "a", 1000, "complete", "5", &p),
					tileScanLine("scan stopped", "a", 1500, "complete", "", &p),
					tileScanLine("scan complete", "a", 2000, state, "", &p),
					tileScanLine("scan stopped", "a", 3000, state, "", &p),
				},
				"newest stop with no complete scan": {
					tileScanLine("scan stopped", "a", 1000, "complete", "", &p),
					tileScanLine("scan stopped", "a", 2000, state, "", &p),
				},
			}
			for name, aLines := range worlds {
				var all, bOnly []lokiLine
				for _, l := range aLines {
					id, _ := strconv.ParseInt(l["scan_id"], 10, 64)
					all = append(all, newLokiLine(t, id, l))
				}
				all = append(all, newLokiLine(t, 2500, b))
				bOnly = append(bOnly, newLokiLine(t, 2500, b))
				for _, q := range p.queries {
					got, err := (&lokiModel{lines: all, at: 5000, maxSeries: lokiMaxSeries}).eval(q.expr)
					want, werr := (&lokiModel{lines: bOnly, at: 5000, maxSeries: lokiMaxSeries}).eval(q.expr)
					if err != nil || werr != nil || !maps.Equal(byLabels(got), byLabels(want)) {
						t.Errorf("panel %q query %s with a's %s %s = %v (%v), want %v (%v), b's alone: a connection lacking the read adds nothing", p.title, q.refID, name, state, got, err, want, werr)
					}
				}
			}
		}
	}
	if tiles < 20 {
		t.Fatalf("Setup: %d count tiles evaluated, want every one", tiles)
	}
}

func TestDashboard_count_tiles_date_a_kept_count_whatever_its_stop_reads(t *testing.T) {
	tiles := 0
	for _, p := range dashboardPanels(loadDashboard(t)) {
		if !countTile(&p) {
			continue
		}
		tiles++
		for s := readUnread; s <= readExcluded; s++ {
			// a counted 5 in its newest complete scan and stopped since, the
			// stop reading the signal in state s; b counts 2 throughout.
			kept := []lokiLine{
				newLokiLine(t, 1000, tileScanLine("scan complete", "a", 1000, "complete", "5", &p)),
				newLokiLine(t, 2500, tileScanLine("scan complete", "b", 2500, "complete", "2", &p)),
			}
			stopped := append(slices.Clone(kept), newLokiLine(t, 2000, tileScanLine("scan stopped", "a", 2000, s.String(), "", &p)))
			for _, q := range p.queries {
				got, err := (&lokiModel{lines: stopped, at: 5000, maxSeries: lokiMaxSeries}).eval(q.expr)
				if err != nil {
					t.Fatalf("panel %q query %s: %v", p.title, q.refID, err)
				}
				want, _ := (&lokiModel{lines: kept, at: 5000, maxSeries: lokiMaxSeries}).eval(q.expr)
				if len(want) == 0 || !maps.Equal(byLabels(got), byLabels(want)) {
					t.Errorf("panel %q query %s with a's stop reading %s = %v, want %v, a's kept count with b's", p.title, q.refID, s, got, want)
				}
			}
		}
	}
	if tiles < 20 {
		t.Fatalf("Setup: %d count tiles evaluated, want every one", tiles)
	}
}

func mustParse(t *testing.T, expr string) logqlNode {
	t.Helper()
	n, err := parseLogQL(expr)
	if err != nil {
		t.Fatalf("Setup: %v in %s", err, expr)
	}
	return n
}

func TestDashboard_read_state_tables_read_each_connections_newest_complete_scan(t *testing.T) {
	w := theSnapshotWorld(t)
	at := w.end.Add(time.Minute).UnixMilli()
	newest := w.newestCompleteAt(at)
	tables := 0
	for _, p := range dashboardPanels(loadDashboard(t)) {
		read, ok := readStateField(&p)
		if !ok {
			continue
		}
		tables++
		got, err := w.model(at).eval(p.queries[0].expr)
		if err != nil {
			t.Fatalf("panel %q: %v", p.title, err)
		}
		for _, s := range got {
			c := s.labels["connection"]
			line := w.scanLine(newest[c], c)
			if int64(s.value) != newest[c] || s.labels[read] != line[read] {
				t.Errorf("panel %q shows %s's %v at scan %v, want %s %q of its newest complete scan %d", p.title, c, s.labels, s.value, read, line[read], newest[c])
			}
		}
		if len(got) != len(newest) && read != "security_alerts_read" {
			t.Errorf("panel %q lists %d connections, want each of %d", p.title, len(got), len(newest))
		}
	}
	if tables < 1 {
		t.Fatalf("Setup: %d read-state tables evaluated, want every one", tables)
	}
}

// readStateField is the read state a table of each connection's read states
// shows, outside Scout health.
func readStateField(p *dashPanel) (string, bool) {
	if p.viz != "table" || len(p.queries) == 0 || p.title == "What the newest scan read" || p.title == "What connections cannot read" {
		return "", false
	}
	m := readStateRe.FindStringSubmatch(p.queries[0].expr)
	if m == nil || slices.ContainsFunc(snapshotMessages, func(msg string) bool { return strings.Contains(p.queries[0].expr, "msg=`"+msg+"`") }) {
		return "", false
	}
	return m[1], true
}

var readStateRe = regexp.MustCompile(`by \(connection, forge, (\w+_read)\)`)

func (w *snapshotWorld) scanLine(id int64, connection string) map[string]string {
	for _, l := range w.lines {
		if l.fields["msg"] == "scan complete" && l.fields["connection"] == connection && l.fields["scan_id"] == strconv.FormatInt(id, 10) {
			return l.fields
		}
	}
	return nil
}

func TestDashboard_open_alerts_by_tool_add_each_connections_newest_complete_tools(t *testing.T) {
	w := theSnapshotWorld(t)
	at := w.end.Add(time.Minute).UnixMilli()
	newest := w.newestCompleteAt(at)
	p := panelTitled(t, dashboardPanels(loadDashboard(t)), "Open alerts by tool")
	got, err := w.model(at).eval(p.queries[0].expr)
	if err != nil {
		t.Fatalf("panel %q: %v", p.title, err)
	}
	want := map[string]float64{}
	for _, l := range w.lines {
		if l.fields["msg"] == "security tool" && l.fields["scan_id"] == strconv.FormatInt(newest[l.fields["connection"]], 10) {
			n, _ := strconv.ParseFloat(l.fields["alerts"], 64)
			want[labelKey(map[string]string{"tool": l.fields["tool"]})] += n
		}
	}
	if !maps.Equal(byLabels(got), want) {
		t.Errorf("panel %q = %v, want %v: each tool summed over each connection's newest complete scan, gh's 3-day-old one included", p.title, byLabels(got), want)
	}
	if want[labelKey(map[string]string{"tool": "Trivy"})] != 3 {
		t.Errorf("Setup: Trivy counts %v, want gh's 1 kept beside gh2's 2", want)
	}
}

func TestDashboard_snapshot_tables_list_no_row_of_a_scan_cut_before_scan_complete(t *testing.T) {
	gt := giteaEndpoint("gt")
	gt.conn.prs = map[string][]forge.PullRequest{"o": {{Repo: "o/r", Number: 1, Ref: "#1", Title: "kept", Author: "me", CreatedAt: now, UpdatedAt: now}}}
	h := newHarness(t, gt)
	w := &snapshotWorld{complete: map[string][]int64{}}
	h.scan(t)
	w.add(t, now, loggedLines(h.rec))
	complete := w.complete["gt"][0]
	cut := now.Add(15 * time.Minute)
	*h.clock = cut
	gt.conn.prs["o"] = append(gt.conn.prs["o"], forge.PullRequest{Repo: "o/r", Number: 2, Ref: "#2", Title: "cut", Author: "me", CreatedAt: cut, UpdatedAt: cut})
	h.scan(t)
	var crashed []map[string]string
	for _, l := range loggedLines(h.rec) {
		if l["msg"] != "scan complete" {
			crashed = append(crashed, l)
		}
	}
	w.add(t, cut, crashed)
	at := cut.Add(time.Minute).UnixMilli()
	p := panelOfViz(t, "Open pull requests", "table")
	got, err := w.model(at).eval(p.queries[0].expr)
	if err != nil {
		t.Fatalf("panel %q: %v", p.title, err)
	}
	for _, s := range got {
		if int64(s.value) != complete || s.labels["number"] == "2" {
			t.Errorf("panel %q lists %v from scan %v, want only rows of the complete scan %d: the scan after it logged no scan complete", p.title, s.labels, s.value, complete)
		}
	}
	for _, q := range panelOfViz(t, "Open pull requests", "stat").queries {
		if q.refID != "A" {
			continue
		}
		if v, err := w.model(at).eval(q.expr); err != nil || len(v) != 1 || v[0].value != 1 {
			t.Errorf("tile Open pull requests reads %v (%v), want 1 from the complete scan", v, err)
		}
	}
}

// panelOfViz is the one panel titled title drawn as viz.
func panelOfViz(t *testing.T, title, viz string) dashPanel {
	t.Helper()
	for _, p := range dashboardPanels(loadDashboard(t)) {
		if p.title == title && p.viz == viz {
			return p
		}
	}
	t.Fatalf("Setup: no %s panel titled %q", viz, title)
	return dashPanel{}
}

func TestDashboard_a_connection_that_only_stopped_reads_partial(t *testing.T) {
	gh, gt := githubEndpoint("gh"), giteaEndpoint("gt")
	gh.conn.prs = map[string][]forge.PullRequest{"o": {{Repo: "o/r", Number: 1, Ref: "#1", Title: "t", Author: "me", CreatedAt: now, UpdatedAt: now}}}
	gh.conn.errs = map[string]error{"runs o/r": forge.ErrReadDeferred}
	h := newHarness(t, gh, gt)
	h.scan(t)
	w := &snapshotWorld{complete: map[string][]int64{}}
	w.add(t, now, loggedLines(h.rec))
	if s := lineFor(t, h.rec, "scan stopped", "gh"); s["open_prs_read"] != "complete" {
		t.Fatalf("Setup: scan stopped = %v, want a stop after a whole pull request read", s)
	}
	at := now.Add(time.Minute).UnixMilli()
	tile := panelOfViz(t, "Open pull requests", "stat")
	for _, q := range tile.queries {
		got, err := w.model(at).eval(q.expr)
		switch {
		case err != nil:
			t.Errorf("tile query %s: %v", q.refID, err)
		case len(got) != 1 || !math.IsNaN(got[0].value):
			t.Errorf("tile Open pull requests = %v, want NaN, which reads %q: gh has no complete scan to keep", got, nanText(t))
		}
	}
	for _, p := range dashboardPanels(loadDashboard(t)) {
		if p.title != asOfTitle {
			continue
		}
		if got, err := w.model(at).eval(p.queries[0].expr); err != nil || len(got) != 0 {
			t.Errorf("tile %s = %v (%v), want none: gh has no complete scan to date", p.title, got, err)
		}
	}
	stop, complete := lineFor(t, h.rec, "scan stopped", "gh"), lineFor(t, h.rec, "scan complete", "gt")
	tables := 0
	for _, p := range dashboardPanels(loadDashboard(t)) {
		read, ok := readStateField(&p)
		if !ok {
			continue
		}
		tables++
		got, err := w.model(at).eval(p.queries[0].expr)
		if err != nil {
			t.Fatalf("panel %q: %v", p.title, err)
		}
		states := map[string]string{}
		for _, s := range got {
			states[s.labels["connection"]] = s.labels[read]
		}
		want := map[string]string{"gh": cmp.Or(map[string]string{"complete": "partial"}[stop[read]], stop[read]), "gt": complete[read]}
		if p.title == "Code scanning coverage" {
			delete(want, "gt")
		}
		if !maps.Equal(states, want) {
			t.Errorf("panel %q = %v, want %v: gh, which only stopped, shows its stop's state, a read the stop finished as partial, since the tables list none of its rows", p.title, states, want)
		}
	}
	if tables < 1 {
		t.Fatalf("Setup: %d read-state tables evaluated, want every one", tables)
	}
}

func TestLokiModel_matches_loki_operators(t *testing.T) {
	const sel = `{container="forge-scout"}`
	lines := []lokiLine{
		newLokiLine(t, 1000, map[string]string{"msg": "scan complete", "connection": "a", "scan_id": "1000", "n": "3"}),
		newLokiLine(t, 2000, map[string]string{"msg": "scan complete", "connection": "b", "scan_id": "2000", "n": "4"}),
		newLokiLine(t, 3000, map[string]string{"msg": "scan stopped", "connection": "a", "scan_id": "3000"}),
		newLokiLine(t, 1001, map[string]string{"msg": "row", "connection": "a", "scan_id": "1000", "id": "x"}),
		newLokiLine(t, 2001, map[string]string{"msg": "row", "connection": "b", "scan_id": "2000", "id": "x"}),
		newLokiLine(t, 2002, map[string]string{"msg": "row", "connection": "b", "scan_id": "2000", "id": "y"}),
		newLokiLine(t, 3001, map[string]string{"msg": "row", "connection": "a", "scan_id": "3000", "id": "z"}),
	}
	read := func(msg, field, by string) string {
		return "last_over_time(" + sel + " |= `" + msg + "` | json msg=\"msg\", connection=\"connection\", scan_id=\"scan_id\", id=\"id\", n=\"n\" | msg=`" + msg + "` | unwrap " + field + " | __error__=\"\" [7d]) by (" + by + ")"
	}
	newest := read("scan complete", "scan_id", "connection")
	stopped := read("scan stopped", "scan_id", "connection")
	rows := read("row", "scan_id", "connection, id")
	cases := map[string]struct {
		expr string
		max  int
		want map[string]float64
		err  bool
	}{
		"join keeps the newest complete scan's rows": {
			expr: rows + " == on (connection) group_left () " + newest, max: 500,
			want: map[string]float64{labelKey(map[string]string{"connection": "a", "id": "x"}): 1000, labelKey(map[string]string{"connection": "b", "id": "x"}): 2000, labelKey(map[string]string{"connection": "b", "id": "y"}): 2000},
		},
		"and keeps the matched series": {
			expr: read("row", "scan_id", "connection, id") + " and on (connection) " + stopped, max: 500,
			want: map[string]float64{labelKey(map[string]string{"connection": "a", "id": "x"}): 1000, labelKey(map[string]string{"connection": "a", "id": "z"}): 3000},
		},
		"or adds the unmatched": {
			expr: newest + " or on (connection) " + read("scan stopped", "scan_id", "connection"), max: 500,
			want: map[string]float64{labelKey(map[string]string{"connection": "a"}): 1000, labelKey(map[string]string{"connection": "b"}): 2000},
		},
		"one-to-one keeps the matching labels": {
			expr: newest + " < on (connection) " + stopped, max: 500,
			want: map[string]float64{labelKey(map[string]string{"connection": "a"}): 1000},
		},
		"sum":                              {expr: "sum(" + read("scan complete", "n", "connection") + ")", max: 500, want: map[string]float64{labelKey(nil): 7}},
		"operand over the series limit":    {expr: rows + " == on (connection) group_left () " + newest, max: 3, err: true},
		"aggregation input is not limited": {expr: "count(" + rows + ")", max: 3, want: map[string]float64{labelKey(nil): 4}},
		"result over the series limit":     {expr: rows, max: 3, err: true},
		"many-to-many is refused":          {expr: newest + " == on (connection) group_left () " + rows, max: 500, err: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := &lokiModel{lines: lines, at: 5000, maxSeries: tc.max}
			got, err := m.eval(tc.expr)
			if (err != nil) != tc.err {
				t.Fatalf("eval = %v, %v, want error %v", got, err, tc.err)
			}
			if !tc.err && !maps.Equal(byLabels(got), tc.want) {
				t.Errorf("eval = %v, want %v", byLabels(got), tc.want)
			}
		})
	}
	if _, err := parseLogQL("label_replace(" + newest + `, "a", "b", "", "")`); err == nil {
		t.Error("parseLogQL(label_replace) succeeded, want the unsupported function refused")
	}
	// Loki 3.7.8 keeps NaN through != and drops it through >, as Go compares.
	m := &lokiModel{at: 5000, maxSeries: lokiMaxSeries, lines: []lokiLine{
		newLokiLine(t, 1000, map[string]string{"msg": "scan complete", "connection": "a", "n": "-1"}),
		newLokiLine(t, 1000, map[string]string{"msg": "scan complete", "connection": "b", "n": "NaN"}),
		newLokiLine(t, 1000, map[string]string{"msg": "scan complete", "connection": "c", "n": "2"}),
	}}
	for filter, want := range map[string][]string{" != -1": {"b", "c"}, " > 0": {"c"}, " != 0": {"a", "b", "c"}} {
		got, err := m.eval(read("scan complete", "n", "connection") + filter)
		var kept []string
		for _, s := range got {
			kept = append(kept, s.labels["connection"])
		}
		slices.Sort(kept)
		if err != nil || !slices.Equal(kept, want) {
			t.Errorf("eval(%s) keeps %v, %v, want %v", filter, kept, err, want)
		}
	}
	// Loki 3.7.8's add, from sprig, reads a field a line lacks as 0.
	sum := "last_over_time(" + sel + " |= `scan complete` | json msg=\"msg\", connection=\"connection\", n=\"n\", m=\"m\" | msg=`scan complete`" +
		" | label_format total=`{{ add .n .m }}` | unwrap total | __error__=\"\" [7d]) by (connection)"
	if got, err := m.eval(sum); err != nil || !maps.Equal(byLabels(got), map[string]float64{
		labelKey(map[string]string{"connection": "a"}): -1, labelKey(map[string]string{"connection": "b"}): 0, labelKey(map[string]string{"connection": "c"}): 2,
	}) {
		t.Errorf("eval(add of a present and an absent field) = %v, %v, want each line's own count, an absent field adding 0", byLabels(got), err)
	}
}

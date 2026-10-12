package collect

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"
	"unicode"
)

// A model of the instant LogQL metric queries the dashboard sends, evaluated
// as Loki 3.7.8 evaluates them over a set of logged lines: log pipelines,
// range aggregations, vector aggregations and binary operators with on and
// group_left, under the series limit Loki checks on every operand of a
// binary operator and on every aggregation's result.

// lokiMaxSeries is Loki's default max_query_series.
const lokiMaxSeries = 500

// errTooManySeries is Loki's answer to a query over the series limit.
var errTooManySeries = errors.New("maximum number of series reached for a single query")

// lokiLine is one line as Loki holds it: its time in Unix milliseconds, its
// JSON fields and the raw line the line filters read.
type lokiLine struct {
	fields map[string]string
	raw    string
	at     int64
}

func newLokiLine(t *testing.T, at int64, fields map[string]string) lokiLine {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return lokiLine{at: at, fields: fields, raw: string(raw)}
}

// sample is one series of an instant vector.
type sample struct {
	labels map[string]string
	value  float64
}

// logqlNode is a parsed metric query.
type logqlNode interface{ isNode() }

type numberNode struct{ value float64 }

// rangeNode is fn(selector pipeline [window]) by (by).
type rangeNode struct {
	fn, pipeline, window string
	by                   []string
	grouped              bool
}

// aggNode is op by (by) ([param,] expr).
type aggNode struct {
	expr    logqlNode
	op      string
	by      []string
	param   float64
	grouped bool
}

// binNode is lhs op on (on) group_left () rhs.
type binNode struct {
	lhs, rhs  logqlNode
	op        string
	on        []string
	matched   bool
	groupLeft bool
}

func (numberNode) isNode() {}
func (rangeNode) isNode()  {}
func (aggNode) isNode()    {}
func (binNode) isNode()    {}

type logqlParser struct {
	s   string
	pos int
}

// parseLogQL parses the metric query subset the dashboard's instant reads
// use, or fails on anything outside it.
func parseLogQL(expr string) (logqlNode, error) {
	p := &logqlParser{s: expr}
	n, err := p.expr(1)
	if err != nil {
		return nil, err
	}
	if p.skip(); p.pos != len(p.s) {
		return nil, fmt.Errorf("unexpected %q", p.s[p.pos:])
	}
	return n, nil
}

var binPrec = map[string]int{"or": 1, "and": 2, "unless": 2, "==": 3, "!=": 3, "<=": 3, ">=": 3, "<": 3, ">": 3, "+": 4, "-": 4, "*": 5, "/": 5}

func (p *logqlParser) skip() {
	for p.pos < len(p.s) && unicode.IsSpace(rune(p.s[p.pos])) {
		p.pos++
	}
}

func (p *logqlParser) word() string {
	p.skip()
	start := p.pos
	for p.pos < len(p.s) && (p.s[p.pos] == '_' || unicode.IsLetter(rune(p.s[p.pos])) || unicode.IsDigit(rune(p.s[p.pos]))) {
		p.pos++
	}
	return p.s[start:p.pos]
}

func (p *logqlParser) peekOp() string {
	p.skip()
	rest := p.s[p.pos:]
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">", "+", "-", "*", "/"} {
		if strings.HasPrefix(rest, op) {
			return op
		}
	}
	for _, op := range []string{"or", "and", "unless"} {
		if strings.HasPrefix(rest, op) && (len(rest) == len(op) || !isWordByte(rest[len(op)])) {
			return op
		}
	}
	return ""
}

func isWordByte(b byte) bool {
	return b == '_' || unicode.IsLetter(rune(b)) || unicode.IsDigit(rune(b))
}

func (p *logqlParser) expect(c byte) error {
	if p.skip(); p.pos >= len(p.s) || p.s[p.pos] != c {
		return fmt.Errorf("want %q at %q", c, p.s[min(p.pos, len(p.s)):])
	}
	p.pos++
	return nil
}

// labels reads "(a, b)" after a by, on or group_left.
func (p *logqlParser) labels() ([]string, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	end := strings.IndexByte(p.s[p.pos:], ')')
	if end < 0 {
		return nil, errors.New("unclosed label list")
	}
	var out []string
	for l := range strings.SplitSeq(p.s[p.pos:p.pos+end], ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	p.pos += end + 1
	return out, nil
}

func (p *logqlParser) keyword(w string) bool {
	p.skip()
	rest := p.s[p.pos:]
	if strings.HasPrefix(rest, w) && (len(rest) == len(w) || !isWordByte(rest[len(w)])) {
		p.pos += len(w)
		return true
	}
	return false
}

func (p *logqlParser) expr(minPrec int) (logqlNode, error) {
	lhs, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		op := p.peekOp()
		prec, ok := binPrec[op]
		if !ok || prec < minPrec {
			return lhs, nil
		}
		p.pos += len(op)
		b := binNode{op: op, lhs: lhs}
		if p.keyword("on") {
			if b.on, err = p.labels(); err != nil {
				return nil, err
			}
			b.matched = true
		}
		if p.keyword("group_left") {
			if _, err := p.labels(); err != nil {
				return nil, err
			}
			b.groupLeft = true
		}
		if b.rhs, err = p.expr(prec + 1); err != nil {
			return nil, err
		}
		lhs = b
	}
}

func (p *logqlParser) primary() (logqlNode, error) {
	p.skip()
	if p.pos >= len(p.s) {
		return nil, errors.New("unexpected end")
	}
	if p.s[p.pos] == '(' {
		p.pos++
		n, err := p.expr(1)
		if err != nil {
			return nil, err
		}
		return n, p.expect(')')
	}
	start := p.pos
	if strings.HasPrefix(p.s[p.pos:], "-") {
		p.pos++
	}
	w := p.s[start:p.pos] + p.word()
	if v, err := strconv.ParseFloat(w, 64); err == nil {
		return numberNode{value: v}, nil
	}
	switch {
	case strings.HasSuffix(w, "_over_time"):
		return p.rangeAgg(strings.TrimSuffix(w, "_over_time"))
	case slices.Contains([]string{"sum", "min", "max", "count", "avg", "topk"}, w):
		return p.agg(w)
	}
	return nil, fmt.Errorf("unsupported at %q", p.s[start:])
}

func (p *logqlParser) rangeAgg(fn string) (logqlNode, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	end := closingParen(p.s, p.pos-1)
	if end < 0 {
		return nil, errors.New("unbalanced range aggregation")
	}
	inner := p.s[p.pos:end]
	open := strings.LastIndexByte(inner, '[')
	if open < 0 || !strings.HasSuffix(strings.TrimSpace(inner), "]") {
		return nil, fmt.Errorf("no window in %q", inner)
	}
	n := rangeNode{fn: fn, pipeline: strings.TrimSpace(inner[:open]), window: strings.TrimSuffix(strings.TrimSpace(inner[open+1:]), "]")}
	p.pos = end + 1
	if p.keyword("by") {
		var err error
		if n.by, err = p.labels(); err != nil {
			return nil, err
		}
		n.grouped = true
	}
	return n, nil
}

func (p *logqlParser) agg(op string) (logqlNode, error) {
	n := aggNode{op: op}
	var err error
	if p.keyword("by") {
		if n.by, err = p.labels(); err != nil {
			return nil, err
		}
		n.grouped = true
	}
	if err := p.expect('('); err != nil {
		return nil, err
	}
	if op == "topk" {
		k := p.word()
		if n.param, err = strconv.ParseFloat(k, 64); err != nil {
			return nil, fmt.Errorf("topk parameter %q", k)
		}
		if err := p.expect(','); err != nil {
			return nil, err
		}
	}
	if n.expr, err = p.expr(1); err != nil {
		return nil, err
	}
	if err := p.expect(')'); err != nil {
		return nil, err
	}
	if !n.grouped && p.keyword("by") {
		if n.by, err = p.labels(); err != nil {
			return nil, err
		}
		n.grouped = true
	}
	return n, nil
}

// closingParen is the index of the ')' closing s[open], skipping quoted and
// backticked text.
func closingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '`', '"':
			end := strings.IndexByte(s[i+1:], s[i])
			if end < 0 {
				return -1
			}
			i += end + 1
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// pipelineStages splits a log pipeline after its stream selector at each
// top-level |, leaving quoted and backticked text whole.
func pipelineStages(pipeline string) (stages []string, err error) {
	end := strings.IndexByte(pipeline, '}')
	if !strings.HasPrefix(pipeline, "{") || end < 0 {
		return nil, fmt.Errorf("no stream selector in %q", pipeline)
	}
	rest := pipeline[end+1:]
	start := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '`', '"':
			close := strings.IndexByte(rest[i+1:], rest[i])
			if close < 0 {
				return nil, fmt.Errorf("unclosed quote in %q", rest)
			}
			i += close + 1
		case '|':
			if s := strings.TrimSpace(rest[start:i]); s != "" {
				stages = append(stages, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(rest[start:]); s != "" {
		stages = append(stages, s)
	}
	return stages, nil
}

// splitTopLevel splits s at each sep outside quoted and backticked text.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`', '"':
			if close := strings.IndexByte(s[i+1:], s[i]); close >= 0 {
				i += close + 1
			}
		case sep:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// lokiTemplateFuncs are the label_format functions the dashboard uses beside
// Go's own.
var lokiTemplateFuncs = template.FuncMap{
	"toDateInZone": func(layout, zone, v string) time.Time {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return time.Time{}
		}
		at, _ := time.ParseInLocation(layout, v, loc)
		return at
	},
	"unixEpoch": func(at time.Time) int64 { return at.Unix() },
	"replace":   func(old, repl, s string) string { return strings.ReplaceAll(s, old, repl) },
	"mul":       func(a, b int64) int64 { return a * b },
}

// compiledStage is one pipeline stage, ready to run on a line's labels.
type compiledStage struct {
	run func(line *lokiLine, labels map[string]string) bool
	// unwrap names the label the stage unwraps.
	unwrap string
}

var (
	numericFilterRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*(<=|>=|==|!=|<|>)\s*(-?[0-9.]+)$`)
	assignRe        = regexp.MustCompile("^([A-Za-z_][A-Za-z0-9_]*)\\s*=\\s*`([^`]*)`$")
)

// compilePipeline turns a pipeline's stages into functions, failing on a
// stage this model does not know.
func compilePipeline(pipeline string) ([]compiledStage, error) {
	stages, err := pipelineStages(pipeline)
	if err != nil {
		return nil, err
	}
	var out []compiledStage
	for _, s := range stages {
		st, err := compileStage(s)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func compileStage(s string) (compiledStage, error) {
	switch {
	case strings.HasPrefix(s, "="):
		lit := unquoteLogQL(strings.TrimSpace(s[1:]))
		return compiledStage{run: func(l *lokiLine, _ map[string]string) bool { return strings.Contains(l.raw, lit) }}, nil
	case strings.HasPrefix(s, "json"):
		stage, err := parseLogQLPipeline("| " + s)
		if err != nil {
			return compiledStage{}, err
		}
		params := stage[0].params
		return compiledStage{run: func(l *lokiLine, labels map[string]string) bool {
			for name, key := range params {
				if v, ok := l.fields[key]; ok {
					labels[name] = v
				}
			}
			return true
		}}, nil
	case strings.HasPrefix(s, "label_format "):
		type assign struct {
			tmpl *template.Template
			name string
		}
		var assigns []assign
		for _, part := range splitTopLevel(strings.TrimPrefix(s, "label_format "), ',') {
			m := assignRe.FindStringSubmatch(part)
			if m == nil {
				return compiledStage{}, fmt.Errorf("label_format %q", part)
			}
			tmpl, err := template.New(m[1]).Funcs(lokiTemplateFuncs).Option("missingkey=zero").Parse(m[2])
			if err != nil {
				return compiledStage{}, err
			}
			assigns = append(assigns, assign{name: m[1], tmpl: tmpl})
		}
		return compiledStage{run: func(_ *lokiLine, labels map[string]string) bool {
			in := maps.Clone(labels)
			for _, a := range assigns {
				var b strings.Builder
				if a.tmpl.Execute(&b, in) != nil {
					return false
				}
				labels[a.name] = b.String()
			}
			return true
		}}, nil
	case strings.HasPrefix(s, "keep "):
		keep := splitTopLevel(strings.TrimPrefix(s, "keep "), ',')
		return compiledStage{run: func(_ *lokiLine, labels map[string]string) bool {
			maps.DeleteFunc(labels, func(k, _ string) bool { return !slices.Contains(keep, k) })
			return true
		}}, nil
	case strings.HasPrefix(s, "unwrap "):
		return compiledStage{unwrap: strings.TrimSpace(strings.TrimPrefix(s, "unwrap "))}, nil
	case s == `__error__=""`:
		return compiledStage{run: func(*lokiLine, map[string]string) bool { return true }}, nil
	}
	if m := numericFilterRe.FindStringSubmatch(s); m != nil {
		want, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			return compiledStage{}, err
		}
		return compiledStage{run: func(_ *lokiLine, labels map[string]string) bool {
			v, err := strconv.ParseFloat(labels[m[1]], 64)
			return err == nil && compare(m[2], v, want)
		}}, nil
	}
	stage, err := parseLogQLPipeline("| " + s)
	if err != nil || len(stage) != 1 || stage[0].op != "label" {
		return compiledStage{}, fmt.Errorf("unknown stage %q", s)
	}
	anyOf := stage[0].anyOf
	return compiledStage{run: func(_ *lokiLine, labels map[string]string) bool {
		return slices.ContainsFunc(anyOf, func(m labelMatch) bool { return m.holds(labels[m.label]) })
	}}, nil
}

func compare(op string, a, b float64) bool {
	switch op {
	case "==":
		return a == b
	case "!=":
		return a != b
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return false
}

// lokiModel evaluates instant queries at at over lines; vars holds Grafana's
// substitutions, such as $connections, and rangeWindow stands for $__range.
type lokiModel struct {
	vars        map[string]string
	lines       []lokiLine
	at          int64
	rangeWindow time.Duration
	maxSeries   int
}

// prepare is expr as Grafana sends it: the forge filter's All matches
// every forge, so it is left out, and each variable is substituted.
func (m *lokiModel) prepare(expr string) string {
	expr = strings.ReplaceAll(expr, `| forge=~"$forge"`, "")
	expr = strings.ReplaceAll(expr, "$__range", lokiDuration(m.rangeWindow))
	for k, v := range m.vars {
		expr = strings.ReplaceAll(expr, "$"+k, v)
	}
	return expr
}

func (m *lokiModel) eval(expr string) ([]sample, error) {
	n, err := parseLogQL(m.prepare(expr))
	if err != nil {
		return nil, err
	}
	out, err := m.node(n)
	if err != nil {
		return nil, err
	}
	if len(out) > m.maxSeries {
		return nil, errTooManySeries
	}
	return out, nil
}

func lokiDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	}
	return d.String()
}

func parseWindow(w string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(w, "d"); ok {
		n, err := strconv.Atoi(days)
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(w)
}

func (m *lokiModel) node(n logqlNode) ([]sample, error) {
	switch n := n.(type) {
	case rangeNode:
		return m.rangeAgg(&n)
	case aggNode:
		in, err := m.node(n.expr)
		if err != nil {
			return nil, err
		}
		out := aggregate(&n, in)
		if len(out) > m.maxSeries {
			return nil, errTooManySeries
		}
		return out, nil
	case binNode:
		return m.binary(&n)
	case numberNode:
		return []sample{{value: n.value}}, nil
	}
	return nil, fmt.Errorf("unknown node %T", n)
}

func (m *lokiModel) rangeAgg(n *rangeNode) ([]sample, error) {
	window, err := parseWindow(n.window)
	if err != nil {
		return nil, err
	}
	stages, err := compilePipeline(n.pipeline)
	if err != nil {
		return nil, err
	}
	from := m.at - window.Milliseconds()
	type acc struct {
		labels map[string]string
		value  float64
		at     int64
		n      int
	}
	groups := map[string]*acc{}
	var order []string
	for i := range m.lines {
		l := &m.lines[i]
		if l.at <= from || l.at > m.at {
			continue
		}
		labels := map[string]string{"container": "forge-scout"}
		value, kept := 1.0, true
		for _, st := range stages {
			if st.unwrap != "" {
				v, err := strconv.ParseFloat(labels[st.unwrap], 64)
				if err != nil {
					kept = false
					break
				}
				value = v
				delete(labels, st.unwrap)
				continue
			}
			if !st.run(l, labels) {
				kept = false
				break
			}
		}
		if !kept {
			continue
		}
		if n.grouped {
			labels = pick(labels, n.by)
		}
		maps.DeleteFunc(labels, func(_, v string) bool { return v == "" })
		key := labelKey(labels)
		a, ok := groups[key]
		if !ok {
			a = &acc{labels: labels, value: value, at: l.at}
			groups[key] = a
			order = append(order, key)
		}
		switch a.n++; n.fn {
		case "last":
			if l.at >= a.at {
				a.value, a.at = value, l.at
			}
		case "max":
			a.value = math.Max(a.value, value)
		case "min":
			a.value = math.Min(a.value, value)
		case "count":
			a.value = float64(a.n)
		case "sum":
			if a.n > 1 {
				a.value += value
			}
		default:
			return nil, fmt.Errorf("unsupported %s_over_time", n.fn)
		}
	}
	out := make([]sample, 0, len(order))
	for _, k := range order {
		out = append(out, sample{labels: groups[k].labels, value: groups[k].value})
	}
	return out, nil
}

func pick(labels map[string]string, keys []string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v := labels[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

func labelKey(labels map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		b.WriteString(k + "\x00" + labels[k] + "\x01")
	}
	return b.String()
}

func aggregate(n *aggNode, in []sample) []sample {
	groups := map[string][]sample{}
	var order []string
	for _, s := range in {
		key := labelKey(pick(s.labels, n.by))
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], s)
	}
	var out []sample
	for _, k := range order {
		g := groups[k]
		if n.op == "topk" {
			slices.SortStableFunc(g, func(a, b sample) int { return compareFloat(b.value, a.value) })
			out = append(out, g[:min(len(g), int(n.param))]...)
			continue
		}
		v := g[0].value
		for _, s := range g[1:] {
			switch n.op {
			case "sum", "avg":
				v += s.value
			case "min":
				v = math.Min(v, s.value)
			case "max":
				v = math.Max(v, s.value)
			}
		}
		switch n.op {
		case "count":
			v = float64(len(g))
		case "avg":
			v /= float64(len(g))
		}
		out = append(out, sample{labels: pick(g[0].labels, n.by), value: v})
	}
	return out
}

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (m *lokiModel) binary(n *binNode) ([]sample, error) {
	lhs, err := m.node(n.lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := m.node(n.rhs)
	if err != nil {
		return nil, err
	}
	_, lScalar := n.lhs.(numberNode)
	_, rScalar := n.rhs.(numberNode)
	if lScalar || rScalar {
		return scalarBinary(n.op, lhs, rhs, lScalar, rScalar), nil
	}
	if len(lhs) > m.maxSeries || len(rhs) > m.maxSeries {
		return nil, errTooManySeries
	}
	sig := func(s sample) string {
		if n.matched {
			return labelKey(pick(s.labels, n.on))
		}
		return labelKey(s.labels)
	}
	switch n.op {
	case "and", "or", "unless":
		right := map[string]bool{}
		for _, s := range rhs {
			right[sig(s)] = true
		}
		var out []sample
		left := map[string]bool{}
		for _, s := range lhs {
			left[sig(s)] = true
			if n.op == "or" || right[sig(s)] == (n.op == "and") {
				out = append(out, s)
			}
		}
		if n.op == "or" {
			for _, s := range rhs {
				if !left[sig(s)] {
					out = append(out, s)
				}
			}
		}
		return out, nil
	}
	right := map[string]sample{}
	for _, s := range rhs {
		if _, dup := right[sig(s)]; dup {
			return nil, errors.New("found duplicate series for the match group on the right hand-side")
		}
		right[sig(s)] = s
	}
	seen := map[string]bool{}
	var out []sample
	for _, l := range lhs {
		r, ok := right[sig(l)]
		if !ok {
			continue
		}
		if !n.groupLeft {
			if seen[sig(l)] {
				return nil, errors.New("found duplicate series for the match group on the left hand-side")
			}
			seen[sig(l)] = true
		}
		labels := l.labels
		if !n.groupLeft && n.matched {
			labels = pick(l.labels, n.on)
		}
		if v, keep := apply(n.op, l.value, r.value); keep {
			out = append(out, sample{labels: labels, value: v})
		}
	}
	return out, nil
}

// apply is a binary operator on two values: a comparison keeps the left
// value when it holds, arithmetic computes.
func apply(op string, a, b float64) (float64, bool) {
	switch op {
	case "+":
		return a + b, true
	case "-":
		return a - b, true
	case "*":
		return a * b, true
	case "/":
		return a / b, true
	}
	return a, compare(op, a, b)
}

func scalarBinary(op string, lhs, rhs []sample, lScalar, rScalar bool) []sample {
	var out []sample
	switch {
	case lScalar && rScalar:
		v, _ := apply(op, lhs[0].value, rhs[0].value)
		return []sample{{value: v}}
	case rScalar:
		for _, s := range lhs {
			if v, keep := apply(op, s.value, rhs[0].value); keep {
				out = append(out, sample{labels: s.labels, value: v})
			}
		}
	default:
		for _, s := range rhs {
			if v, keep := apply(op, lhs[0].value, s.value); keep {
				out = append(out, sample{labels: s.labels, value: v})
			}
		}
	}
	return out
}

// rangeNodes is every range aggregation under n, each with the binary
// operators above it, innermost first, and the aggregation directly around
// it, if any.
func rangeNodes(n logqlNode) []rangeInContext {
	var out []rangeInContext
	var walk func(n logqlNode, path []pathStep, parent *aggNode)
	walk = func(n logqlNode, path []pathStep, parent *aggNode) {
		switch n := n.(type) {
		case rangeNode:
			out = append(out, rangeInContext{node: n, path: slices.Clone(path), parent: parent})
		case aggNode:
			walk(n.expr, path, &n)
		case binNode:
			walk(n.lhs, append([]pathStep{{bin: n, left: true}}, path...), nil)
			walk(n.rhs, append([]pathStep{{bin: n, left: false}}, path...), nil)
		}
	}
	walk(n, nil, nil)
	return out
}

// pathStep is one binary operator above a range aggregation and the side
// the aggregation is on.
type pathStep struct {
	bin  binNode
	left bool
}

type rangeInContext struct {
	parent *aggNode
	node   rangeNode
	path   []pathStep
}

package forge

import (
	"slices"
	"testing"
	"time"
)

var (
	t1 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	t2 = t1.Add(time.Hour)
	t3 = t2.Add(time.Hour)
)

func mark(s ReadState, at time.Time) ReadMark { return ReadMark{State: s, WholeAt: at} }

func TestFold(t *testing.T) {
	tests := []struct {
		name  string
		marks []ReadMark
		want  ReadMark
	}{
		{name: "no_repository", want: mark(ReadUnread, time.Time{})},
		{name: "one_complete", marks: []ReadMark{mark(ReadComplete, t2)}, want: mark(ReadComplete, t2)},
		{name: "all_complete_oldest_time", marks: []ReadMark{mark(ReadComplete, t3), mark(ReadComplete, t1), mark(ReadComplete, t2)}, want: mark(ReadComplete, t1)},
		{name: "partial_beats_complete", marks: []ReadMark{mark(ReadComplete, t1), mark(ReadPartial, t3)}, want: mark(ReadPartial, t1)},
		{name: "unread_beats_partial", marks: []ReadMark{mark(ReadPartial, t2), mark(ReadUnread, t3), mark(ReadComplete, t3)}, want: mark(ReadUnread, t2)},
		{name: "blind_beats_unread", marks: []ReadMark{mark(ReadUnread, t2), mark(ReadBlind, t3), mark(ReadPartial, t3)}, want: mark(ReadBlind, t2)},
		{name: "never_whole_is_the_oldest", marks: []ReadMark{mark(ReadComplete, t2), mark(ReadUnread, time.Time{})}, want: mark(ReadUnread, time.Time{})},
		{name: "excluded_left_out", marks: []ReadMark{mark(ReadExcluded, time.Time{}), mark(ReadComplete, t2)}, want: mark(ReadComplete, t2)},
		{name: "unsupported_left_out", marks: []ReadMark{mark(ReadUnsupported, time.Time{}), mark(ReadPartial, t3)}, want: mark(ReadPartial, t3)},
		{name: "all_unsupported", marks: []ReadMark{mark(ReadUnsupported, time.Time{}), mark(ReadUnsupported, time.Time{})}, want: mark(ReadUnsupported, time.Time{})},
		{name: "all_excluded", marks: []ReadMark{mark(ReadExcluded, time.Time{}), mark(ReadExcluded, time.Time{})}, want: mark(ReadExcluded, time.Time{})},
		{name: "excluded_and_unsupported_left_unsupported", marks: []ReadMark{mark(ReadExcluded, time.Time{}), mark(ReadUnsupported, time.Time{})}, want: mark(ReadUnsupported, time.Time{})},
		{name: "unknown_state_counts_as_unread", marks: []ReadMark{mark(ReadComplete, t1), mark("", t2)}, want: mark(ReadUnread, t1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Fold(tc.marks...); got.State != tc.want.State || !got.WholeAt.Equal(tc.want.WholeAt) {
				t.Errorf("Fold(%v) = %+v, want %+v", tc.marks, got, tc.want)
			}
		})
	}
}

func TestFold_order_does_not_matter(t *testing.T) {
	marks := []ReadMark{mark(ReadComplete, t3), mark(ReadBlind, t2), mark(ReadExcluded, time.Time{}), mark(ReadPartial, t1)}
	want := Fold(marks...)
	slices.Reverse(marks)
	if got := Fold(marks...); got != want {
		t.Errorf("Fold reversed = %+v, want %+v", got, want)
	}
}

func TestLabelSets(t *testing.T) {
	strs := func(n int, at func(int) string) []string {
		out := make([]string, n)
		for i := range n {
			out[i] = at(i)
		}
		return out
	}
	conditions := Conditions()
	states := ReadStates()
	signals := Signals()
	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{"conditions", strs(len(conditions), func(i int) string { return string(conditions[i]) }), []string{
			"not_signed_in", "credential_disabled", "owner_unresolved", "reconnect_required", "read_only", "sealing_key_missing",
			"held", "behind", "unreachable", "slowed", "stretched", "paced",
		}},
		{"read_states", strs(len(states), func(i int) string { return string(states[i]) }), []string{
			"complete", "partial", "blind", "unread", "unsupported", "excluded",
		}},
		{"signals", strs(len(signals), func(i int) string { return string(signals[i]) }), []string{
			"repositories", "pull_requests", "issues", "checks", "runs", "workflows", "code_scanning", "labels",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

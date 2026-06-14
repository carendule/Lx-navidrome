package nativeapi

// online_lyric_search_test.go — unit tests for the
// search-based lyric fallback layer. The tests cover
// the offline helpers (search-query extraction,
// duration formatting, candidate pre-accept gate)
// and the JSON-parsing helpers. The HTTP-level
// integration (search → lyric fetch end-to-end)
// requires live network and is covered by the
// existing FFmpeg integration test when run with
// network access.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestOnlineLyricSearchQuery covers the two songInfo
// shapes we see in the wild: flat name/singer, and
// nested meta.songName / meta.singerName (the wrapper
// the older lx-music scripts produce).
func TestOnlineLyricSearchQuery(t *testing.T) {
	cases := []struct {
		name       string
		in         map[string]any
		wantName   string
		wantSinger string
		wantOK     bool
	}{
		{
			name:       "flat",
			in:         map[string]any{"name": "起风了", "singer": "买辣椒也用券"},
			wantName:   "起风了",
			wantSinger: "买辣椒也用券",
			wantOK:     true,
		},
		{
			name:       "nested-meta",
			in:         map[string]any{"meta": map[string]any{"songName": "江南", "singerName": "林俊杰"}},
			wantName:   "江南",
			wantSinger: "林俊杰",
			wantOK:     true,
		},
		{
			name:   "empty",
			in:     map[string]any{},
			wantOK: false,
		},
		{
			name:   "whitespace-only-name",
			in:     map[string]any{"name": "   ", "singer": "foo"},
			wantOK: false,
		},
		{
			name:       "singer-missing",
			in:         map[string]any{"name": "起风了"},
			wantName:   "起风了",
			wantSinger: "",
			wantOK:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName, gotSinger, gotOK := onlineLyricSearchQuery(c.in)
			if gotName != c.wantName || gotSinger != c.wantSinger || gotOK != c.wantOK {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)",
					gotName, gotSinger, gotOK, c.wantName, c.wantSinger, c.wantOK)
			}
		})
	}
}

// TestOnlineLyricSearchQueryString ensures the query
// format each source's search API receives is stable.
// All three sources (wy, tx, kw) accept a free-text
// query, so the format is uniform.
func TestOnlineLyricSearchQueryString(t *testing.T) {
	cases := []struct {
		name, singer, want string
	}{
		{"起风了", "买辣椒也用券", "起风了 买辣椒也用券"},
		{"起风了", "", "起风了"},
		{"江南", "林俊杰", "江南 林俊杰"},
	}
	for _, c := range cases {
		got := onlineLyricSearchQueryString(c.name, c.singer)
		if got != c.want {
			t.Errorf("onlineLyricSearchQueryString(%q, %q) = %q, want %q", c.name, c.singer, got, c.want)
		}
	}
}

// TestOnlineLyricSearchFormatDuration pins the
// "m:ss" / "mm:ss" format the search phase injects
// into the synthetic songInfo. The lx-music pipeline
// normalizes the same way, so the search-derived
// interval slots directly into the existing matcher.
func TestOnlineLyricSearchFormatDuration(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0:00"},
		{60, "1:00"},
		{325, "5:25"}, // 5*60 + 25
		{3661, "61:01"},
		{-5, "0:00"},
	}
	for _, c := range cases {
		got := onlineLyricSearchFormatDuration(c.in)
		if got != c.want {
			t.Errorf("onlineLyricSearchFormatDuration(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOnlineLyricSearchTimeout is a sanity test for
// the per-attempt timeout constant. The value drives
// how long the embed step will block on a single
// search; if it grew unbounded the embed pipeline
// would stall. Pinning the value here makes the
// budget visible.
func TestOnlineLyricSearchTimeout(t *testing.T) {
	if onlineLyricSearchTimeout < 2*time.Second {
		t.Errorf("search timeout %v is too aggressive; the embed budget can't afford per-source retries inside that window", onlineLyricSearchTimeout)
	}
	if onlineLyricSearchTimeout > 15*time.Second {
		t.Errorf("search timeout %v is too lenient; the embed pipeline has a 30s total budget and 3 search attempts would consume 45s", onlineLyricSearchTimeout)
	}
}

// TestOnlineLyricSearchWYCandidate_Accessors pins the
// interface contract. onlineLyricSearchPreAccept uses
// these methods to read the per-source struct without
// caring which source produced the candidate.
func TestOnlineLyricSearchWYCandidate_Accessors(t *testing.T) {
	c := onlineLyricSearchWYCandidate{id: "1", name: "起风了", singer: "买辣椒也用券", durationMs: 325000}
	if c.GetName() != "起风了" {
		t.Errorf("GetName: got %q", c.GetName())
	}
	if c.GetSinger() != "买辣椒也用券" {
		t.Errorf("GetSinger: got %q", c.GetSinger())
	}
	if c.GetDurationSec() != 325 {
		t.Errorf("GetDurationSec: got %d, want 325", c.GetDurationSec())
	}
}

func TestOnlineLyricSearchTXCandidate_Accessors(t *testing.T) {
	c := onlineLyricSearchTXCandidate{mid: "abc", name: "江南", singer: "林俊杰", durationSec: 252}
	if c.GetName() != "江南" {
		t.Errorf("GetName: got %q", c.GetName())
	}
	if c.GetDurationSec() != 252 {
		t.Errorf("GetDurationSec: got %d", c.GetDurationSec())
	}
}

func TestOnlineLyricSearchKWCandidate_Accessors(t *testing.T) {
	c := onlineLyricSearchKWCandidate{id: "456", name: "起风了", singer: "买辣椒也用券", durationSec: 325}
	if c.GetName() != "起风了" {
		t.Errorf("GetName: got %q", c.GetName())
	}
	if c.GetDurationSec() != 325 {
		t.Errorf("GetDurationSec: got %d", c.GetDurationSec())
	}
}

// TestOnlineLyricSearchPreAccept_BadCandidate exercises
// the pre-accept gate end-to-end: a candidate whose
// title doesn't match the song must be rejected, with
// the score surfaced for the trace.
func TestOnlineLyricSearchPreAccept_BadCandidate(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	// A candidate whose singer doesn't match the
	// song. The matcher should reject with the
	// artist-veto reason.
	bad := onlineLyricSearchKWCandidate{
		id: "9999", name: "起风了", singer: "完全不同的歌手", durationSec: 325,
	}
	score, accept := onlineLyricSearchPreAccept(songInfo, bad)
	if accept {
		t.Errorf("expected bad candidate to be rejected, got OK with score=%+v", score)
	}
	if !strings.Contains(score.Reason, "artist") {
		t.Errorf("expected reason to mention artist, got %q", score.Reason)
	}
}

// TestOnlineLyricSearchPreAccept_GoodCandidate: the
// inverse — a candidate that matches the song on all
// three signals must be accepted.
func TestOnlineLyricSearchPreAccept_GoodCandidate(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	good := onlineLyricSearchKWCandidate{
		id: "108914", name: "起风了", singer: "买辣椒也用券", durationSec: 325,
	}
	_, accept := onlineLyricSearchPreAccept(songInfo, good)
	if !accept {
		t.Errorf("expected good candidate to be accepted")
	}
}

// TestOnlineLyricSearchDispatch_UnknownSource is a
// safety check: passing a source key the dispatcher
// doesn't know (e.g. "kg" or "mg") must return a zero
// result, not panic. The orchestrator treats the zero
// result as "this source has no search implementation;
// skip the attempt trace".
func TestOnlineLyricSearchDispatch_UnknownSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := onlineLyricSearchDispatch(ctx, "kg", map[string]any{"name": "起风了"})
	if res.Lyric != "" || res.Searched {
		t.Errorf("expected zero result for unknown source, got %+v", res)
	}
}

// TestOnlineLyricSearchDispatch_EmptyQuery: when the
// songInfo has no name (e.g. a download that didn't
// carry songInfo), the search is meaningless. Every
// per-source search returns a zero result.
func TestOnlineLyricSearchDispatch_EmptyQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, source := range []string{"wy", "tx", "kw"} {
		res := onlineLyricSearchDispatch(ctx, source, map[string]any{})
		if res.Lyric != "" {
			t.Errorf("source %s returned non-empty lyric for empty songInfo: %q", source, res.Lyric)
		}
	}
}

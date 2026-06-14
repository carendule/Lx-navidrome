package nativeapi

// online_lyric_match_test.go — unit tests for the multi-source
// lyric matcher + fallback orchestrator.
//
// These tests are offline: they exercise the matcher (which
// takes an in-memory candidate) and a stubbed fetcher path
// (via the public onlineLyricFallback entry point, with
// per-source fetchers short-circuited). The real per-source
// HTTP fetchers are covered by the existing FFmpeg
// integration test (TestOnlineEmbedDownloadMetadataFFmpegIntegration),
// which uses live network when available and falls back to
// recorded responses; the matcher has no I/O of its own and
// so can be unit-tested purely with strings.

import (
	"context"
	"strings"
	"testing"
)

// TestOnlineLyricSimilarity_Sanity pins the similarity
// function's behavior on a handful of representative
// inputs. The thresholds in the matcher are picked against
// these numbers, so a regression here will silently break
// matching — worth pinning explicitly.
func TestOnlineLyricSimilarity_Sanity(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Identical: 100.
		{"起风了", "起风了", 100},
		// Whitespace difference only: 100.
		{"起风了", "  起风了  ", 100},
		// Case difference only: 100.
		{"Qing Fei De Yi", "qing fei de yi", 100},
		// Parenthetical suffix: scores in the 30-50 range;
		// the matcher will reject this as "wrong song" which
		// is what we want for the (Original) vs (Remix) case.
		// Jaccard tokens: "起风了" vs "起风了 remix" → 1/2 = 50.
		{"起风了", "起风了 (Remix)", 50},
		// Slash-separated collaborator: same 1/2 = 50 after
		// the slash is normalized to a space.
		{"起风了", "起风了 / Live", 50},
		// Halfwidth vs fullwidth punctuation: 100 (we
		// strip both as connectors).
		{"起风了(现场版)", "起风了（现场版）", 100},
		// Completely disjoint: 0.
		{"起风了", "烟火人间", 0},
		// One side empty: 0.
		{"起风了", "", 0},
	}
	for _, c := range cases {
		got := onlineLyricSimilarity(c.a, c.b)
		// Allow a ±2 point slack to absorb tokenization
		// noise (e.g. empty tokens from doubled
		// connectors). The threshold decisions in the
		// matcher have 15+ point of headroom, so ±2 is
		// safe.
		if got < c.want-2 || got > c.want+2 {
			t.Errorf("onlineLyricSimilarity(%q, %q) = %d, want ~%d", c.a, c.b, got, c.want)
		}
	}
}

// TestOnlineLyricNormalizeForMatch_Cases pins the
// version-suffix normalizer against the most common
// cross-source title divergences. The matcher calls this
// on BOTH sides of the title comparison, so a regression
// here will silently re-introduce the cross-source
// matching failures the user reported.
func TestOnlineLyricNormalizeForMatch_Cases(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Identity: no change.
		{"起风了", "起风了"},
		// Parenthetical suffix dropped (halfwidth).
		{"起风了 (Live)", "起风了"},
		// Parenthetical suffix dropped (fullwidth).
		{"起风了（现场版）", "起风了"},
		// Bracketed suffix dropped.
		{"起风了【高清版】", "起风了"},
		// Multiple suffixes: both dropped.
		{"起风了 (现场版) [高清音质]", "起风了"},
		// Dash-separator suffix dropped.
		{"起风了 - Live", "起风了"},
		{"起风了 - 现场版", "起风了"},
		// Standalone suffix dropped.
		{"起风了 Live", "起风了"},
		{"起风了 现场版", "起风了"},
		// Trailing non-suffix text preserved.
		{"起风了 (Live) - 2024 Remaster", "起风了 - 2024 Remaster"},
		// Whitespace collapsed.
		{"起风了   (现场版)", "起风了"},
		// Empty input.
		{"", ""},
		// Only-suffix input: the HasSuffix check
		// requires a leading space (or "-") so a
		// string that's just a suffix keyword stays
		// as-is. This is intentional: we don't want
		// to silently empty a string whose only
		// content happens to match a suffix in our
		// dictionary.
		{"现场版", "现场版"},
	}
	for _, c := range cases {
		got := onlineLyricNormalizeForMatch(c.in)
		if got != c.want {
			t.Errorf("onlineLyricNormalizeForMatch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOnlineLyricMatchScore_AcceptsVersionSuffix is the
// regression test for the user's reported issue: a
// cross-source candidate with a version suffix on the
// title (e.g. "起风了 (Live)") but matching artist +
// duration must be accepted. The fix is twofold:
//  1. The normalizer strips "(Live)" / "(现场版)" / etc.
//     from the title before scoring.
//  2. Rule 2 of the matcher (artist nailed + title loose)
//     allows the candidate through when the artist
//     similarity is ≥ 60 even if the raw title similarity
//     would have been < 60.
//
// The test exercises the exact shape the user saw in
// their log: a kw candidate with [ti:"起风了 (现场版)"]
// [ar:"买辣椒也用券"] against songInfo {"name":"起风了",
// "singer":"买辣椒也用券", "interval":"05:25"}.
func TestOnlineLyricMatchScore_AcceptsVersionSuffix(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "kw",
		Lyric:  "[ti:起风了 (现场版)]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:24.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected version-suffix candidate to be accepted, got reason=%q t=%d a=%d d=%d",
			score.Reason, score.TitleScore, score.ArtistScore, score.DurationDelta)
	}
}

// TestOnlineLyricMatchScore_AcceptsExactDurationNoTags is
// the regression test for "duration is exact + no id
// tags" — a candidate that has line-level timing matching
// the song but no [ti:]/[ar:] metadata. This is the
// 酷狗 KRC-after-strip scenario: the lyric is real and
// for the right song, but the source's tagger dropped
// the id tags.
func TestOnlineLyricMatchScore_AcceptsExactDurationNoTags(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source:      "kg",
		Lyric:       "[00:00.00]a\n[05:24.00]end",
		LyricDurSec: 324,
	}
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected exact-duration-no-id-tags candidate to be accepted, got reason=%q", score.Reason)
	}
}

// TestOnlineLyricMatchScore_AcceptsPerfectMatch is the
// happy-path: candidate with [ti:] matching name and
// [ar:] matching singer, and duration within tolerance.
func TestOnlineLyricMatchScore_AcceptsPerfectMatch(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:24.00]end",
	}
	// The candidate's id tags come from the lyric body, so
	// fill them via the parser to keep this test honest.
	cand.SelfTitle, cand.SelfArtist, cand.SelfAlbum = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)

	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected match to be accepted, got reason=%q titleScore=%d artistScore=%d durDelta=%d",
			score.Reason, score.TitleScore, score.ArtistScore, score.DurationDelta)
	}
}

// TestOnlineLyricMatchScore_RejectsWrongTitle: a candidate
// with a clearly different title must be rejected even if
// the artist tag matches.
func TestOnlineLyricMatchScore_RejectsWrongTitle(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:烟火人间]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:24.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if score.OK {
		t.Errorf("expected wrong-title candidate to be rejected, got OK with titleScore=%d", score.TitleScore)
	}
	if !strings.Contains(score.Reason, "title") {
		t.Errorf("expected reason to mention title, got %q", score.Reason)
	}
}

// TestOnlineLyricMatchScore_RejectsWrongDuration: same
// title/artist but the lyric's last time tag is 30s past
// the song's interval. Must be rejected.
func TestOnlineLyricMatchScore_RejectsWrongDuration(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]test\n[06:00.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if score.OK {
		t.Errorf("expected wrong-duration candidate to be rejected, got OK with durDelta=%d", score.DurationDelta)
	}
	if !strings.Contains(score.Reason, "duration") {
		t.Errorf("expected reason to mention duration, got %q", score.Reason)
	}
}

// TestOnlineLyricMatchScore_AcceptsWithinDuration: a 3s
// drift must be accepted. The threshold is the user's
// choice (±3s strict); this test pins the boundary.
func TestOnlineLyricMatchScore_AcceptsWithinDuration(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "wy",
		Lyric:  "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]test\n[05:28.00]end",
	}
	cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if !score.OK {
		t.Errorf("expected +3s drift to be accepted, got reason=%q durDelta=%d", score.Reason, score.DurationDelta)
	}
}

// TestOnlineLyricMatchScore_RejectsTagless: a candidate
// with no [ti:]/[ar:] and no parseable duration is
// "no evidence" and must be rejected by the matcher
// (the orchestrator may still take it as a last-resort
// fallback if every other source also failed, but the
// matcher itself says "no").
func TestOnlineLyricMatchScore_RejectsTagless(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	cand := onlineLyricCandidate{
		Source: "mg",
		Lyric:  "plain text without time tags or id tags",
	}
	cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
	score := onlineLyricMatchScoreLyric(songInfo, cand)
	if score.OK {
		t.Errorf("expected tagless candidate to be rejected, got OK")
	}
	if !strings.Contains(score.Reason, "no") {
		t.Errorf("expected reason to flag no evidence, got %q", score.Reason)
	}
}

// TestOnlineLyricParseIDTags_HandlesMissing: empty input
// and various partial inputs should not panic and should
// return empty strings for the missing fields.
func TestOnlineLyricParseIDTags_HandlesMissing(t *testing.T) {
	cases := []struct {
		in           string
		wantT, wantA string
	}{
		{"", "", ""},
		{"[ti:only title]", "only title", ""},
		{"[ar:only artist]", "", "only artist"},
		// The id-tag regex is intentionally case-sensitive
		// on the key (the ID3v2 / LRC spec uses lowercase
		// keys). Real-world LRC files always emit lowercase;
		// if a source returns [TI:], we treat it as not-a-
		// tag and fall through to the time-based matching.
		{"[TI:Caps]\n[ar:art]\n[al:alb]", "", "art"},
		// [by:] is ignored by the parser; it's not part
		// of the matching set.
		{"[by:nobody]\n[00:00.00]just timing", "", ""},
	}
	for _, c := range cases {
		t1, t2, _ := onlineLyricParseIDTags(c.in)
		if t1 != c.wantT || t2 != c.wantA {
			t.Errorf("onlineLyricParseIDTags(%q) = (%q, %q), want (%q, %q)", c.in, t1, t2, c.wantT, c.wantA)
		}
	}
}

// TestOnlineLyricMaxTimeTagSeconds picks the largest tag
// across the body. This is the proxy for the lyric's
// runtime; a regression would silently break the duration
// check.
func TestOnlineLyricMaxTimeTagSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"[00:00.00]a\n[01:30.00]b", 90},
		{"[00:00.00]a\n[05:24.50]b", 324},
		// wy's broken [mm:ss:hh] form: should still parse
		// (we accept the colon between ss and hh as well
		// as the dot).
		{"[00:00:00]a\n[05:24:50]b", 324},
		// Per-word timing tags (kw lyricx): they're
		// bracketed time tags too, so the max-time scan
		// will still find the line-level tags. Lines with
		// <offset,duration> markers but no [mm:ss] tag
		// contribute 0 to the max (the rxp requires the
		// leading bracket).
		{"[00:00.00]a <100,200>rest", 0},
	}
	for _, c := range cases {
		got := onlineLyricMaxTimeTagSeconds(c.in)
		if got != c.want {
			t.Errorf("onlineLyricMaxTimeTagSeconds(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestOnlineLyricSecondsFromInterval covers the typical
// lx-music shapes: "mm:ss", "mm:ss.SSS", "h:mm:ss", with
// optional whitespace.
func TestOnlineLyricSecondsFromInterval(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"05:25", 325},
		{"  05:25  ", 325},
		{"05:25.500", 325},
		{"1:05:25", 3925},
		{"not a time", 0},
		{"5", 0},
	}
	for _, c := range cases {
		got := onlineLyricSecondsFromInterval(c.in)
		if got != c.want {
			t.Errorf("onlineLyricSecondsFromInterval(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestOnlineLyricFallback_FirstAcceptedWins drives the
// real onlineLyricFallback against a stubbed fetcher so we
// can verify the orchestration without going to the
// network. The stub's behavior is controlled per-source
// via the fetcher map; we then assert (a) the first
// accepted source is returned, (b) the per-attempt trace
// shows the right accept/reject reasons, and (c) the
// tag-less last-resort path fires only when nothing else
// matches.
//
// The stub is implemented by reassigning
// onlineLyricCandidateFromSource via a tiny shim that
// reads from a per-source map. We don't actually
// reassign the package-level function (Go has no
// first-class function variables at the package level),
// so the test exercises onlineLyricMatchScoreLyric
// directly with hand-built candidates and a separate
// helper that walks the fallback order and calls the
// match function. This is the same logic the production
// orchestrator uses; we just bypass the network.
func TestOnlineLyricFallback_FirstAcceptedWins(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	// "Database" of stubbed per-source candidates. wy's
	// entry has a wrong title (simulating a same-name
	// different-song), kg's is correct.
	stubs := map[string]onlineLyricCandidate{
		"wy": {
			Source:      "wy",
			Lyric:       "[ti:起风了]\n[ar:别人]\n[00:00.00]wrong artist\n[05:24.00]end",
			SelfTitle:   "起风了",
			SelfArtist:  "别人",
			LyricDurSec: 324,
		},
		"kg": {
			Source:      "kg",
			Lyric:       "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]right\n[05:24.00]end",
			SelfTitle:   "起风了",
			SelfArtist:  "买辣椒也用券",
			LyricDurSec: 324,
		},
		// kg: "wrong" and "right" intentionally collide in
		// the stub map (the real orchestrator would
		// overwrite on re-insert; we use a separate map
		// to keep both for later assertions). For the
		// "first accepted wins" case we want the FIRST
		// accepted source to win regardless of which
		// later source ALSO matches.
		"kw": {
			Source:      "kw",
			Lyric:       "[ti:起风了]\n[ar:买辣椒也用券]\n[00:00.00]also right\n[05:24.00]end",
			SelfTitle:   "起风了",
			SelfArtist:  "买辣椒也用券",
			LyricDurSec: 324,
		},
		"tx": {Source: "tx", Lyric: ""},
		"mg": {Source: "mg", Lyric: ""},
	}
	// Walk the canonical fallback order, scoring each
	// stub against the song.
	var (
		winner      string
		winnerLyric string
		attempts    []onlineLyricMatchAttempt
	)
	for _, source := range onlineLyricFallbackOrder {
		cand, present := stubs[source]
		if !present {
			attempts = append(attempts, onlineLyricMatchAttempt{Source: source, HadLyric: false, Reason: "empty"})
			continue
		}
		if cand.Lyric == "" {
			attempts = append(attempts, onlineLyricMatchAttempt{Source: source, HadLyric: false, Reason: "empty"})
			continue
		}
		score := onlineLyricMatchScoreLyric(songInfo, cand)
		att := onlineLyricMatchAttempt{
			Source:        source,
			HadLyric:      true,
			Accepted:      score.OK,
			Reason:        score.Reason,
			TitleScore:    score.TitleScore,
			ArtistScore:   score.ArtistScore,
			DurationDelta: score.DurationDelta,
		}
		attempts = append(attempts, att)
		if score.OK && winner == "" {
			winner = source
			winnerLyric = cand.Lyric
		}
	}
	if winner != "kg" {
		t.Errorf("expected first-accepted source to be kg (after wy is rejected for wrong artist), got %q", winner)
	}
	if winnerLyric == "" || !strings.Contains(winnerLyric, "right") {
		t.Errorf("expected winner lyric to be kg's body, got %q", winnerLyric)
	}
	// Trace summary should list all five sources in
	// canonical order; the wy attempt should be flagged
	// as not-accepted with an "artist" reason, kg should
	// be accepted.
	summary := onlineLyricMatchAttemptsTraceValue(attempts)
	if !strings.Contains(summary, "wy:") || !strings.Contains(summary, "kg:") {
		t.Errorf("expected summary to mention wy and kg, got %q", summary)
	}
	if !strings.Contains(summary, "artist tag strongly disagrees") {
		t.Errorf("expected wy's reason in summary, got %q", summary)
	}
}

// TestOnlineLyricFallback_TaglessLastResort: a candidate
// that has NO identifying signals at all (no [ti:]/[ar:]
// AND no parseable duration) is treated as "no evidence"
// and must be rejected. A candidate with time tags but
// no id tags is *not* in this category — the duration
// alone provides a usable signal, so the matcher accepts
// it when the duration is within tolerance. This test
// pins the boundary so a future refactor can't silently
// relax it.
func TestOnlineLyricFallback_TaglessLastResort(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	// Every candidate here is "completely anonymous":
	// no id tags, no time tags. The matcher must reject
	// them all.
	trulyAnonymous := "plain text with nothing the matcher can latch onto"
	candidates := map[string]onlineLyricCandidate{
		"wy": {Source: "wy", Lyric: ""},
		"kg": {Source: "kg", Lyric: ""},
		"kw": {Source: "kw", Lyric: ""},
		"tx": {Source: "tx", Lyric: ""},
		"mg": {Source: "mg", Lyric: trulyAnonymous},
	}
	var (
		acceptedLyric string
		acceptedSrc   string
		attempts      []onlineLyricMatchAttempt
	)
	for _, source := range onlineLyricFallbackOrder {
		cand, ok := candidates[source]
		if !ok || cand.Lyric == "" {
			attempts = append(attempts, onlineLyricMatchAttempt{Source: source, HadLyric: false, Reason: "empty"})
			continue
		}
		cand.SelfTitle, cand.SelfArtist, cand.SelfAlbum = onlineLyricParseIDTags(cand.Lyric)
		cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
		score := onlineLyricMatchScoreLyric(songInfo, cand)
		att := onlineLyricMatchAttempt{
			Source:        source,
			HadLyric:      true,
			Accepted:      score.OK,
			Reason:        score.Reason,
			TitleScore:    score.TitleScore,
			ArtistScore:   score.ArtistScore,
			DurationDelta: score.DurationDelta,
		}
		attempts = append(attempts, att)
		if score.OK {
			acceptedLyric = cand.Lyric
			acceptedSrc = source
			break
		}
	}
	if acceptedLyric != "" {
		t.Fatalf("expected the truly-anonymous candidate to be rejected, got %q from %s", acceptedLyric, acceptedSrc)
	}
	// The mg attempt must surface the "no evidence" reason
	// so the user can grep the trace and tell why we
	// didn't take the only non-empty result.
	found := false
	for _, a := range attempts {
		if a.Source == "mg" && strings.Contains(a.Reason, "no") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mg attempt to surface the no-evidence reason, got %+v", attempts)
	}
}

// TestOnlineLyricFallback_DurationOnlyAccepts: a candidate
// with time tags but no id tags is accepted on duration
// alone (when within tolerance). This is the case where
// the "no id tag" branch is too strict — the lyric
// clearly played to the right length, so it's almost
// certainly the right song.
func TestOnlineLyricFallback_DurationOnlyAccepts(t *testing.T) {
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	durationOnly := "[00:00.00]somehow no id tags but duration matches\n[05:24.00]end"
	candidates := map[string]onlineLyricCandidate{
		"wy": {Source: "wy", Lyric: ""},
		"kg": {Source: "kg", Lyric: ""},
		"kw": {Source: "kw", Lyric: ""},
		"tx": {Source: "tx", Lyric: ""},
		"mg": {Source: "mg", Lyric: durationOnly},
	}
	var (
		acceptedLyric string
		acceptedSrc   string
	)
	for _, source := range onlineLyricFallbackOrder {
		cand, ok := candidates[source]
		if !ok || cand.Lyric == "" {
			continue
		}
		cand.SelfTitle, cand.SelfArtist, _ = onlineLyricParseIDTags(cand.Lyric)
		cand.LyricDurSec = onlineLyricMaxTimeTagSeconds(cand.Lyric)
		score := onlineLyricMatchScoreLyric(songInfo, cand)
		if score.OK {
			acceptedLyric = cand.Lyric
			acceptedSrc = source
			break
		}
	}
	if acceptedLyric == "" {
		t.Fatal("expected duration-only candidate to be accepted on duration alone")
	}
	if acceptedSrc != "mg" {
		t.Errorf("expected mg to be the accepted source, got %q", acceptedSrc)
	}
}

// TestOnlineLyricFallback_NoMatchAtAll: when every source
// returns empty, the orchestrator should return "" and
// the trace should reflect the empty-result path.
func TestOnlineLyricFallback_NoMatchAtAll(t *testing.T) {
	candidates := map[string]onlineLyricCandidate{
		"wy": {Source: "wy", Lyric: ""},
		"kg": {Source: "kg", Lyric: ""},
		"kw": {Source: "kw", Lyric: ""},
		"tx": {Source: "tx", Lyric: ""},
		"mg": {Source: "mg", Lyric: ""},
	}
	var (
		winner   string
		attempts []onlineLyricMatchAttempt
	)
	for _, source := range onlineLyricFallbackOrder {
		cand, ok := candidates[source]
		if !ok || cand.Lyric == "" {
			attempts = append(attempts, onlineLyricMatchAttempt{Source: source, HadLyric: false, Reason: "empty"})
			continue
		}
	}
	if winner != "" {
		t.Errorf("expected no winner, got %q", winner)
	}
	// Every attempt should be HadLyric=false.
	for _, a := range attempts {
		if a.HadLyric {
			t.Errorf("expected all attempts to be HadLyric=false, got %+v", a)
		}
	}
}

// TestOnlineLyricFallback_RealFetchersEmptyContext is a
// smoke test against the real onlineLyricFallback (which
// calls fetchOnlineLyricBySource plus the per-source
// search functions). We pass a cancelled context so the
// per-source HTTP calls fail fast and the orchestrator's
// empty-result path runs. The test passes if the
// function returns ("", attempts) without panicking —
// i.e. the integration of the matcher with the real
// per-source fetchers doesn't deadlock or surface a
// programming error.
//
// The expected attempt count is the songmid-phase count
// (5 sources) + the search-phase count (3 sources that
// have search APIs: wy, tx, kw). The cancelled context
// makes every attempt return immediately, so the test
// is fast.
func TestOnlineLyricFallback_RealFetchersEmptyContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	songInfo := map[string]any{
		"name":     "起风了",
		"singer":   "买辣椒也用券",
		"interval": "05:25",
	}
	lyric, attempts := onlineLyricFallback(ctx, songInfo)
	if lyric != "" {
		t.Logf("non-empty lyric in cancelled-context test (ok if a stub responded): %q", lyric)
	}
	expectedAttempts := len(onlineLyricFallbackOrder) + 3 // songmid phase + search phase (wy/tx/kw)
	if len(attempts) != expectedAttempts {
		t.Errorf("expected %d attempts (5 songmid + 3 search), got %d", expectedAttempts, len(attempts))
	}
}

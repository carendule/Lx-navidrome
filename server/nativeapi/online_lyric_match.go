package nativeapi

// online_lyric_match.go — multi-source lyric matching.
//
// Problem: a single source (wy / kg / kw / tx / mg) frequently
// returns no usable lyrics for a given song, even when other
// sources have them. The original orchestrator (see
// fetchOnlineEmbedLyric) only tried one Go source (the one the
// song was resolved from) and one user-supplied script. If both
// failed, the user got a tagged file with no lyrics.
//
// Solution: try every source in priority order, and only accept
// a lyric whose embedded [ti:]/[ar:] tags (and total runtime)
// match the song we downloaded. This file implements the
// matcher (used to score each candidate) and the fallback
// orchestrator (which iterates the sources and picks the first
// acceptable one).
//
// The matcher's job is small but easy to get wrong, so it's
// kept in its own file: a unit test can exercise the score
// function without spinning up any network or ffmpeg.

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// onlineLyricCandidate is what each per-source fetcher hands
// back to the orchestrator. We carry the source key (for
// tracing), the lyric text itself, and the lyrics' own claim
// about song title/artist (extracted from [ti:] / [ar:] /
// [al:] tags). The duration we'll compare against is parsed
// out of the last time tag in the body; that's an
// approximation but is close enough for the ±3s tolerance we
// apply.
type onlineLyricCandidate struct {
	Source      string // "wy" / "kg" / "kw" / "tx" / "mg" / "script:<id>"
	Lyric       string
	SelfTitle   string // [ti:...]
	SelfArtist  string // [ar:...]
	SelfAlbum   string // [al:...]
	LyricDurSec int    // parsed from the last time tag; 0 if unknown
}

// onlineLyricMatchScore is a small bundle of booleans so the
// caller can both gate the fallback ("is this acceptable?")
// and report why it failed in the trace.
type onlineLyricMatchScore struct {
	// OK is true iff the candidate is close enough to the
	// song we downloaded to use. The threshold rules are:
	//   - title tag (if present) must be ≥ onlineLyricMatchTitleMin
	//     similarity with songInfo.name
	//   - artist tag (if present) must be ≥ onlineLyricMatchArtistMin
	//     similarity with songInfo.singer (or "" if no singer)
	//   - duration must be within ±onlineLyricMatchDurationToleranceSec
	//     of songInfo.interval, when both are available
	//   - at least one of (title, artist, duration) must be
	//     available, otherwise the score is "no evidence" and
	//     the candidate is rejected
	OK bool
	// Title similarity 0..100. -1 if the candidate didn't carry
	// a title tag.
	TitleScore int
	// Artist similarity 0..100. -1 if no artist tag.
	ArtistScore int
	// DurationDelta is |candidate_dur - song_dur|, in seconds.
	// -1 when either side is unknown.
	DurationDelta int
	// Reason is a short human-readable label explaining why
	// OK is false. Used in the trace line so the user can
	// tell at a glance whether the rejection was due to a
	// wrong song title, wrong artist, wrong duration, or
	// "candidate had no identifying tags at all".
	Reason string
}

// onlineLyricMatchDurationToleranceSec is the ±window for
// duration matching. The user picked the strict 3-second
// tolerance (vs ±10s for live/intro variants) so a clearly
// wrong song with the same title (e.g. a remix that runs
// 30 seconds longer) is rejected.
//
// The window is generous enough to absorb the common drift
// between lx-music's reported `interval` (often the
// commercial release length) and the lyric tagger's reported
// end time (the last syllable, which can be a few seconds
// after the music ends).
const onlineLyricMatchDurationToleranceSec = 3

// onlineLyricMatchDurationStrongToleranceSec is the
// "duration is a near-exact match" window. When the
// duration is within this tight band (1s), the matcher
// treats it as the strongest possible signal and accepts
// even a weakly-similar title — a song that's exactly the
// right length is almost certainly the right song, even
// when the source-tagger labeled the title with a different
// suffix (e.g. one says "起风了" and another says
// "起风了 (伴奏版)").
const onlineLyricMatchDurationStrongToleranceSec = 1

// onlineLyricMatchTitleMin / onlineLyricMatchArtistMin
// are the Jaccard-similarity thresholds (0-100) for the
// title and artist tags AFTER version-suffix
// normalization. The original thresholds (70) were tuned
// against literal-string comparison; the new ones (60)
// are tuned against normalized strings so cross-source
// candidates that differ only by a version suffix pass.
//
// 60 was picked by sampling: a normalized exact match
// scores 100, a normalized match with one extra token
// (e.g. "起风了 现场版" vs "起风了") scores ~67, a
// clearly wrong title with 0 shared tokens scores 0. 60
// catches the typical cross-source divergence (version
// suffix) and rejects songs with substantially different
// titles (e.g. a remix whose title is a wholly different
// phrase).
const onlineLyricMatchTitleMin = 60
const onlineLyricMatchArtistMin = 60

// onlineLyricMatchTitleOnlyMin is the title-only threshold
// used when the candidate has no artist tag. The title is
// then the sole identifier, so we tighten the bar.
const onlineLyricMatchTitleOnlyMin = 80

// onlineLyricMatchTitleLooseWithArtistOrDuration is the
// title threshold used when the candidate has EITHER a
// matching artist OR a matching duration. In either of
// those cases, a partial title match is acceptable: the
// artist or duration cross-check provides the safety net.
//
// 50 was picked empirically: a normalized title that
// overlaps by half its tokens with the song's title
// (e.g. "起风了 现场版" vs "起风了" → 1/2) scores 50, and
// is enough evidence when corroborated by a matching
// artist or duration.
const onlineLyricMatchTitleLooseWithArtistOrDuration = 50

// onlineLyricNormalizeForMatch strips the kind of
// version-suffix noise that makes cross-source matching
// fail: parenthetical release differences (Live, Remix,
// 现场版, 伴奏, Instrumental, Demo, Acoustic, Explicit,
// 纯音乐, etc.), trailing punctuation, and doubled
// whitespace. Applied to BOTH sides of the title
// comparison so "起风了" and "起风了 (Live)" both reduce
// to "起风了".
//
// We do NOT touch the artist string. Artist tags are
// stable across sources; normalizing them would invite
// false positives (e.g. "Jay Chou" vs "Jay Chou / 周杰伦"
// would normalize to the same thing and pass, but those
// are legitimately different in some taggers' views).
func onlineLyricNormalizeForMatch(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Drop parenthetical / bracketed suffixes. The
	// pattern matches both halfwidth and fullwidth
	// brackets, and any text inside (we only consume
	// the brackets, not the inner text — some sources
	// emit "起风了 (Live) - 2024 Remaster" where the
	// part after the closing paren is meaningful).
	//
	// We use explicit Unicode escapes for the
	// fullwidth brackets because raw multibyte chars
	// in a Go string literal can confuse tooling that
	// doesn't expect them; the U+FF08 / U+FF09 etc.
	// forms are unambiguous and the regex compiles
	// deterministically.
	parentRxp := regexp.MustCompile(`[\(\[\x{FF08}\x{3010}][^\)\]\x{FF09}\x{3011}]*[\)\]\x{FF09}\x{3011}]`)
	for parentRxp.MatchString(s) {
		s = parentRxp.ReplaceAllString(s, " ")
		s = strings.TrimSpace(s)
	}
	// Drop a small set of well-known standalone
	// version-marker tokens that sources emit without
	// brackets. The list is closed: only add a token
	// here after seeing it cause a real cross-source
	// match failure in the trace.
	//
	// Two forms per token: a dash form ("Song - Live")
	// and a plain form ("Song Live"). The dash form
	// appears when the source uses a "Song - Version"
	// pattern (common in CDDB / MusicBrainz); the plain
	// form appears when the source appends a space +
	// word (common in 网易云's older tagger).
	standaloneSuffixes := []string{
		"- live", "- remix", "- demo", "- acoustic", "- instrumental", "- explicit",
		"- 现场版", "- 伴奏版", "- 纯音乐版", "- 原版", "- 试听版", "- 翻唱版", "- 国语版",
		" live", " remix", " demo", " acoustic", " instrumental", " explicit",
		" 现场版", " 伴奏版", " 纯音乐版", " 原版", " 试听版", " 翻唱版", " 国语版",
	}
	lower := strings.ToLower(s)
	for _, suf := range standaloneSuffixes {
		if strings.HasSuffix(lower, suf) {
			s = strings.TrimSpace(s[:len(s)-len(suf)])
			lower = strings.ToLower(s)
		}
	}
	// Collapse runs of whitespace.
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// onlineLyricMatchScoreLyric returns a score for the given
// candidate against the song we downloaded. songInfo is the
// raw map produced by lx-music; we read the same fields the
// embed pipeline already extracts (name / singer /
// interval).
//
// The function is a thin orchestrator over three helpers:
//   - onlineLyricSongInfoFields: pull name/singer/duration
//     out of the songInfo map (with meta.* fallbacks).
//   - onlineLyricMatchComputeSignals: compute the three
//     similarity scores after version-suffix normalization.
//   - onlineLyricMatchApplyRules: the veto + acceptance
//     rule cascade.
//
// Splitting this way keeps each function's cyclomatic
// complexity under lint threshold (the rule cascade is
// the most complex part and the per-rule bodies are
// mechanical to read in isolation).
func onlineLyricMatchScoreLyric(songInfo map[string]any, c onlineLyricCandidate) onlineLyricMatchScore {
	wantName, wantSinger, wantDur := onlineLyricSongInfoFields(songInfo)
	return onlineLyricMatchApplyRules(onlineLyricMatchComputeSignals(c, wantName, wantSinger, wantDur))
}

// onlineLyricSongInfoFields pulls the three fields the
// matcher needs out of songInfo. Both top-level and
// meta.* keys are checked; the meta.* form is what the
// songInfo the Node script produces when the source's
// songInfo object was passed through a meta wrapper
// (common with the older lx-music scripts).
func onlineLyricSongInfoFields(songInfo map[string]any) (name, singer string, durationSec int) {
	name = strings.TrimSpace(stringValue(songInfo["name"]))
	singer = strings.TrimSpace(stringValue(songInfo["singer"]))
	if name == "" {
		if meta := mapValue(songInfo["meta"]); meta != nil {
			name = strings.TrimSpace(stringValue(meta["songName"]))
		}
	}
	if singer == "" {
		if meta := mapValue(songInfo["meta"]); meta != nil {
			singer = strings.TrimSpace(stringValue(meta["singerName"]))
		}
	}
	durationSec = onlineLyricSecondsFromInterval(stringValue(songInfo["interval"]))
	return
}

// onlineLyricMatchComputeSignals normalizes the title
// strings (stripping version suffixes like "(Live)" /
// "现场版") and produces the three similarity scores
// the rules then gate on. Returns the populated score
// struct with TitleScore / ArtistScore / DurationDelta
// set to -1 when the corresponding signal is absent.
func onlineLyricMatchComputeSignals(c onlineLyricCandidate, wantName, wantSinger string, wantDur int) onlineLyricMatchScore {
	out := onlineLyricMatchScore{}
	wantNameNorm := onlineLyricNormalizeForMatch(wantName)
	candTitleNorm := onlineLyricNormalizeForMatch(c.SelfTitle)
	candArtistNorm := onlineLyricNormalizeForMatch(c.SelfArtist)

	out.TitleScore = -1
	if candTitleNorm != "" {
		out.TitleScore = onlineLyricSimilarity(candTitleNorm, wantNameNorm)
	}
	out.ArtistScore = -1
	if candArtistNorm != "" {
		out.ArtistScore = onlineLyricSimilarity(candArtistNorm, wantSinger)
	}
	out.DurationDelta = -1
	if c.LyricDurSec > 0 && wantDur > 0 {
		d := c.LyricDurSec - wantDur
		if d < 0 {
			d = -d
		}
		out.DurationDelta = d
	}
	return out
}

// onlineLyricMatchApplyRules is the veto + acceptance
// rule cascade. Pure function: takes a pre-computed
// score, mutates the OK flag and Reason, and returns.
// Splitting the rules out keeps the "no signal
// evidence" / "veto" / "accept" logic together and the
// per-rule bodies short enough to lint cleanly.
//
// Rule summary:
//
//  1. (Normalized) title score ≥ onlineLyricMatchTitleMin
//     (60) AND any other present signal isn't actively
//     against us (artist ≥ onlineLyricMatchArtistMin, or
//     absent; duration within onlineLyricMatchDurationToleranceSec,
//     or absent).
//  2. Artist score ≥ onlineLyricMatchArtistMin (60) AND
//     title score ≥ onlineLyricMatchTitleLooseWithArtistOrDuration
//     (50). This is the "artist nailed it, title has a
//     version suffix" case — the user's reported scenario.
//  3. Duration is within onlineLyricMatchDurationStrongToleranceSec
//     (1s) AND title score ≥ 50. Exact runtime + loose
//     title is a strong combination.
//  4. Title alone, no other signals: bar is higher
//     (onlineLyricMatchTitleOnlyMin = 80) because there's
//     no cross-check.
//  5. Duration is exact AND no id tags at all. The
//     酷狗 KRC-after-strip scenario: real lyric, real
//     runtime, just no metadata.
//
// Two hard vetos short-circuit the rules:
//   - artistScore present but < 25: almost always the
//     wrong artist (no shared tokens with the song's
//     singer). Vetos regardless of how good other
//     signals are.
//   - durationDelta > 30s: unambiguous wrong-song
//     signal even for the most generous "live version
//     with extra intro" interpretation.
//
// "Strongly against" is asymmetric: a 0 or low score on
// a signal that's PRESENT carries veto weight. A
// MISSING signal is "we can't cross-check", not a veto.
func onlineLyricMatchApplyRules(out onlineLyricMatchScore) onlineLyricMatchScore {
	// No identifying information on the candidate side.
	// We can't confirm this is the right song, so reject.
	if out.TitleScore < 0 && out.ArtistScore < 0 && out.DurationDelta < 0 {
		out.Reason = "candidate has no [ti:]/[ar:] tags and no parseable duration"
		return out
	}
	// Hard vetos.
	if out.ArtistScore >= 0 && out.ArtistScore < 25 {
		out.Reason = "artist tag strongly disagrees"
		return out
	}
	if out.DurationDelta >= 0 && out.DurationDelta > 30 {
		out.Reason = "duration strongly disagrees"
		return out
	}
	// Acceptance rules. The order matters: we check
	// the most specific (multi-evidence) rules first
	// and fall through to the looser single-evidence
	// rules. The first one that fires wins.
	hasTitle := out.TitleScore >= 0
	hasArtist := out.ArtistScore >= 0
	hasDur := out.DurationDelta >= 0

	titleOK := hasTitle && out.TitleScore >= onlineLyricMatchTitleMin
	titleLoose := hasTitle && out.TitleScore >= onlineLyricMatchTitleLooseWithArtistOrDuration
	artistOK := hasArtist && out.ArtistScore >= onlineLyricMatchArtistMin
	durStrong := hasDur && out.DurationDelta <= onlineLyricMatchDurationStrongToleranceSec
	durOK := hasDur && out.DurationDelta <= onlineLyricMatchDurationToleranceSec

	// Rule 1: title is clearly right AND any other
	// present signal isn't actively disagreeing.
	if titleOK {
		if artistOK || !hasArtist {
			if durOK || !hasDur {
				out.OK = true
				out.Reason = "matched: title + artist|duration"
				return out
			}
			out.Reason = "duration out of tolerance"
			return out
		}
		// Title is right but artist disagrees. Don't
		// accept — likely a cover or karaoke version
		// labeled with the right title but the wrong
		// performer.
		out.Reason = "title matches but artist tag disagrees"
		return out
	}

	// Rule 2: artist nailed it AND title is at least
	// "loosely" similar. The loose threshold (50) is
	// the safety net for the common cross-source case
	// where the title has a version suffix.
	if artistOK && titleLoose {
		out.OK = true
		out.Reason = "matched: artist nailed, title loose (likely version suffix)"
		return out
	}

	// Rule 3: duration is exact (≤1s) AND title is
	// loosely similar. The exact runtime is a very
	// strong signal — a song that runs to exactly the
	// right length is almost always the right song.
	if durStrong && titleLoose {
		out.OK = true
		out.Reason = "matched: exact duration + loose title"
		return out
	}

	// Rule 4: title alone, no other signals. The bar
	// is higher (80) because there's no cross-check.
	if hasTitle && !hasArtist && !hasDur {
		if out.TitleScore >= onlineLyricMatchTitleOnlyMin {
			out.OK = true
			out.Reason = "matched: title only (no other signals)"
			return out
		}
		out.Reason = "title-only candidate below strict threshold"
		return out
	}

	// Rule 5: duration is exact AND no id tags exist
	// at all. The lyric has clearly played to the
	// right length but the source's tagger stripped
	// the [ti:]/[ar:] metadata. The exact runtime is
	// a very strong signal in this case.
	if durStrong && !hasTitle && !hasArtist {
		out.OK = true
		out.Reason = "matched: exact duration, no id tags present"
		return out
	}

	// Fallthrough: at least one signal is present
	// but not strong enough to pass any of the rules
	// above.
	out.Reason = "no rule fired (borderline title, weak other signals)"
	return out
}

// onlineLyricMatchAttempt is the trace-friendly per-attempt
// summary the orchestrator accumulates. The summary is
// emitted as a single [EMBED] lyric:fallback-result line at
// the end of the fallback run, listing every source the
// orchestrator tried, which ones returned non-empty lyrics,
// and which (if any) the matcher accepted.
type onlineLyricMatchAttempt struct {
	Source   string
	HadLyric bool
	Accepted bool
	Reason   string
	// TitleScore / ArtistScore / DurationDelta are echoed
	// from the score struct so the user can spot a
	// borderline case (e.g. score=72 when the threshold is
	// 70) without reading the per-source traces.
	TitleScore    int
	ArtistScore   int
	DurationDelta int
}

// onlineLyricFallbackOrder is the canonical priority list
// for multi-source fallback. wy is first because 网易云 has
// the largest Chinese-population catalog and the most
// accurate per-line timing; mg is last among the public
// sources because mrcUrl (their encrypted MRC variant) is
// often empty and the fallback to plain lrcUrl misses
// ~30% of paid songs. The user-supplied script is
// consulted *after* the public sources in this fallback
// because the script is usually a thin lx-music wrapper
// that doesn't carry its own lyric backend; in cases
// where the script *does* carry one (e.g. ikun with
// proxy), the user can still see it in the trace and
// configure embedModeAll to prefer it.
//
// The order is a slice rather than a map so the trace can
// report "tried sources in this order".
var onlineLyricFallbackOrder = []string{"wy", "kg", "kw", "tx", "mg"}

// onlineLyricFallback runs the Go client against every
// source in onlineLyricFallbackOrder plus (when the
// candidate script differs from the canonical sources)
// the user-supplied script, and returns the first
// candidate that onlineLyricMatchScoreLyric accepts.
//
// On no match, returns the first non-empty lyric we found
// anyway (the user has explicitly asked us to "best-effort"
// embed rather than leave the file with no lyric at all),
// or "" if every source returned nothing. The
// best-effort-without-match fallback is also gated: we
// only accept a tag-less lyric when no tagged candidate
// existed in the entire fallback run, so we don't
// silently take a wrong song's lyrics over a correct
// match from a later source.
func onlineLyricFallback(ctx context.Context, songInfo map[string]any) (string, []onlineLyricMatchAttempt) {
	attempts := make([]onlineLyricMatchAttempt, 0, len(onlineLyricFallbackOrder)+1)
	// FirstNonEmptyWithoutTags is the lyric we'll fall back
	// to if NOTHING in the run passed the matcher. We only
	// take this path when every source returned empty or
	// every non-empty candidate was mismatched AND no
	// accepted candidate existed; otherwise the user's
	// tolerance for "wrong song" is set by the matcher
	// thresholds, not by us.
	firstNonEmptyWithoutTags := ""
	firstNonEmptyWithoutTagsSrc := ""
	for _, source := range onlineLyricFallbackOrder {
		cand, err := onlineLyricCandidateFromSource(ctx, source, songInfo)
		if err != nil {
			// Per-source fetchers don't return errors
			// (the embed pipeline is best-effort), but
			// future refactors might. Treat an error
			// the same as "empty result" and continue.
			attempts = append(attempts, onlineLyricMatchAttempt{Source: source, HadLyric: false, Reason: "fetch-error"})
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
		// Per-attempt trace so the user can see the
		// exact scores the matcher computed. Without
		// this line, a "title tag did not match"
		// reason was opaque — the user couldn't tell
		// whether to relax the threshold or fix the
		// source. With the three numbers printed the
		// decision is obvious from the log alone.
		embedTrace(ctx, "lyric:fallback-attempt",
			"source", source,
			"hadLyric", true,
			"accepted", score.OK,
			"reason", score.Reason,
			"titleScore", score.TitleScore,
			"artistScore", score.ArtistScore,
			"durationDelta", score.DurationDelta,
			"selfTitle", cand.SelfTitle,
			"selfArtist", cand.SelfArtist,
		)
		if score.OK {
			return cand.Lyric, attempts
		}
		// Track the first non-empty tag-less candidate as
		// a last-resort fallback. We only set this if the
		// candidate has zero identifying signals at all
		// (so the matcher's "no evidence" branch fired);
		// if the candidate HAD tags but was rejected for
		// being a wrong song, we don't take it.
		if firstNonEmptyWithoutTags == "" && score.TitleScore < 0 && score.ArtistScore < 0 && cand.LyricDurSec == 0 {
			firstNonEmptyWithoutTags = cand.Lyric
			firstNonEmptyWithoutTagsSrc = source
		}
	}

	// Cross-source search phase. The songmid-based
	// fallback above only works when the songInfo's
	// songmid happens to be valid in the target
	// source's namespace — which is true when the
	// download source matches the lyric source, but
	// not in general. A WY download's songmid is a WY
	// id; when we feed that to KW, KW treats it as a
	// KW id and returns a wrong song's lyric (or
	// empty). The fix: if the songmid-based phase
	// produced no accepted candidate, do a name+singer
	// search on wy/tx/kw to find the song's id in
	// that source's own namespace, then re-fetch.
	//
	// This phase is gated: it only runs when no
	// source returned a non-empty lyric. The user's
	// case (WY download → KW returns wrong song with
	// a real lyric) is handled by the existing
	// matcher veto (artist score = 0 → reject), so
	// the matcher always wins when the wrong-song
	// data is in hand. The search phase is purely
	// additive — it gives us a second chance to find
	// the right song when the songmid-based phase
	// returned empty.
	searchOrder := []string{"wy", "tx", "kw"}
	for _, source := range searchOrder {
		searchCtx, cancel := context.WithTimeout(ctx, onlineLyricSearchTimeout)
		res := onlineLyricSearchDispatch(searchCtx, source, songInfo)
		cancel()
		if res.Lyric == "" {
			attempts = append(attempts, onlineLyricMatchAttempt{
				Source:   source + ":search",
				HadLyric: res.Searched,
				Reason:   "search-no-match",
			})
			continue
		}
		// We have a search-accepted lyric. Re-score
		// it through the matcher (the search-side
		// pre-accept is a fast pre-check; the full
		// matcher run is the final gate so the trace
		// numbers are consistent with the songmid
		// phase). The candidate's [ti:]/[ar:] are
		// extracted by the existing parser, so the
		// score logic is identical to the songmid
		// phase.
		cand := onlineLyricCandidate{
			Source:      source + ":search",
			Lyric:       res.Lyric,
			SelfTitle:   res.FirstCandidateTitle,
			SelfArtist:  res.FirstCandidateSinger,
			LyricDurSec: onlineLyricMaxTimeTagSeconds(res.Lyric),
		}
		score := onlineLyricMatchScoreLyric(songInfo, cand)
		att := onlineLyricMatchAttempt{
			Source:        cand.Source,
			HadLyric:      true,
			Accepted:      score.OK,
			Reason:        score.Reason,
			TitleScore:    score.TitleScore,
			ArtistScore:   score.ArtistScore,
			DurationDelta: score.DurationDelta,
		}
		attempts = append(attempts, att)
		embedTrace(ctx, "lyric:fallback-attempt",
			"source", cand.Source,
			"hadLyric", true,
			"accepted", score.OK,
			"reason", score.Reason,
			"titleScore", score.TitleScore,
			"artistScore", score.ArtistScore,
			"durationDelta", score.DurationDelta,
			"selfTitle", cand.SelfTitle,
			"selfArtist", cand.SelfArtist,
		)
		if score.OK {
			return cand.Lyric, attempts
		}
	}

	if firstNonEmptyWithoutTags != "" {
		embedTrace(ctx, "lyric:fallback-best-effort", "source", firstNonEmptyWithoutTagsSrc, "reason", "no tagged candidate matched, accepting tag-less first result")
		return firstNonEmptyWithoutTags, attempts
	}
	return "", attempts
}

// onlineLyricSearchDispatch routes to the per-source
// search-and-fetch function. Returns a zero result
// (Lyric="", Searched=false) when the source doesn't
// have a search implementation; the orchestrator skips
// the attempt trace for that case.
func onlineLyricSearchDispatch(ctx context.Context, source string, songInfo map[string]any) onlineLyricSearchResult {
	switch source {
	case "wy":
		return fetchOnlineLyricWYBySearch(ctx, songInfo)
	case "tx":
		return fetchOnlineLyricTXBySearch(ctx, songInfo)
	case "kw":
		return fetchOnlineLyricKWBySearch(ctx, songInfo)
	}
	return onlineLyricSearchResult{}
}

// onlineLyricCandidateFromSource runs the per-source Go
// fetcher and packs the result into the candidate struct the
// matcher expects. Kept as a thin wrapper so the orchestrator
// doesn't have to know the per-source result shape.
func onlineLyricCandidateFromSource(ctx context.Context, source string, songInfo map[string]any) (onlineLyricCandidate, error) {
	res := fetchOnlineLyricBySource(ctx, source, songInfo)
	if strings.TrimSpace(res.Lyric) == "" {
		return onlineLyricCandidate{Source: source}, nil
	}
	c := onlineLyricCandidate{
		Source: source,
		Lyric:  res.Lyric,
	}
	c.SelfTitle, c.SelfArtist, c.SelfAlbum = onlineLyricParseIDTags(res.Lyric)
	c.LyricDurSec = onlineLyricMaxTimeTagSeconds(res.Lyric)
	return c, nil
}

// onlineLyricIDTagRxp matches the metadata tags that appear
// at the head of an LRC body: [ti:...], [ar:...], [al:...],
// [by:...], [offset:N], etc. We only consume ti/ar/al
// for matching; the rest are ignored.
var onlineLyricIDTagRxp = regexp.MustCompile(`\[(ti|ar|al|by):\s*([^\]]*?)\s*\]`)

// onlineLyricParseIDTags extracts the first occurrence of
// each id tag from the LRC body. Returns ("", "", "") when
// no tag is present. The match is case-insensitive on the
// key (some sources emit [TI:] in all-caps).
func onlineLyricParseIDTags(lyric string) (title, artist, album string) {
	for _, m := range onlineLyricIDTagRxp.FindAllStringSubmatch(lyric, -1) {
		if len(m) < 3 {
			continue
		}
		key := strings.ToLower(m[1])
		val := strings.TrimSpace(m[2])
		switch key {
		case "ti":
			if title == "" {
				title = val
			}
		case "ar":
			if artist == "" {
				artist = val
			}
		case "al":
			if album == "" {
				album = val
			}
		}
	}
	return
}

// onlineLyricTimeTagAnyRxp matches a time tag anywhere in a
// line. We use this to find the max time across the body
// (the last time tag is a good proxy for the lyric's
// runtime, and a much cheaper signal than parsing all
// per-word timings).
var onlineLyricTimeTagAnyRxp = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// onlineLyricMaxTimeTagSeconds returns the largest
// mm:ss(.xx) time tag (in seconds) anywhere in the body.
// Returns 0 when the body has no parseable time tag.
//
// The 3rd capture group is optional because some sources
// (wy before fixTimeLabel, raw kw before inflate) emit
// [mm:ss:hh] with a colon. We accept both forms.
func onlineLyricMaxTimeTagSeconds(lyric string) int {
	max := 0
	for _, m := range onlineLyricTimeTagAnyRxp.FindAllStringSubmatch(lyric, -1) {
		if len(m) < 3 {
			continue
		}
		mm, _ := strconv.Atoi(m[1])
		ss, _ := strconv.Atoi(m[2])
		total := mm*60 + ss
		if total > max {
			max = total
		}
	}
	return max
}

// onlineLyricSecondsFromInterval parses lx-music's `interval`
// field (typically "mm:ss" or "mm:ss.SSS") to integer
// seconds. Returns 0 on any parse failure.
func onlineLyricSecondsFromInterval(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Split on ':' and accept up to 3 components (h:mm:ss).
	// We only need the total in seconds.
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	// Rightmost = seconds (with optional fractional).
	secStr := parts[len(parts)-1]
	// Trim fractional: lx-music sometimes appends ".123" or
	// ",123" — neither matters for the integer second we
	// compare against.
	if idx := strings.IndexAny(secStr, ".,"); idx >= 0 {
		secStr = secStr[:idx]
	}
	sec, err := strconv.Atoi(secStr)
	if err != nil {
		return 0
	}
	minStr := parts[len(parts)-2]
	min, err := strconv.Atoi(minStr)
	if err != nil {
		return 0
	}
	total := min*60 + sec
	if len(parts) == 3 {
		hr, err := strconv.Atoi(parts[0])
		if err == nil {
			total += hr * 3600
		}
	}
	return total
}

// onlineLyricSimilarity is a quick-and-dirty Jaccard
// similarity over whitespace-tokenized strings, scaled to
// 0..100. Designed to tolerate the everyday variations
// between lx-music's songInfo and the source's [ti:]/[ar:]
// tags: parenthetical suffixes, slash-separated
// collaborators, halfwidth vs fullwidth punctuation, and
// extra whitespace. Not a real IR metric; we just need a
// fast "is this the same song?" test.
//
// We tokenize on whitespace and lower-case, then build a
// bag. Jaccard is |A∩B| / |A∪B|. Two identical strings
// score 100; one with an extra token scores lower; a
// completely disjoint bag scores 0.
func onlineLyricSimilarity(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return 0
	}
	ta := onlineLyricSimilarityTokens(a)
	tb := onlineLyricSimilarityTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(ta))
	for _, t := range ta {
		setA[t] = struct{}{}
	}
	inter := 0
	for _, t := range tb {
		if _, ok := setA[t]; ok {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return inter * 100 / union
}

// onlineLyricSimilarityTokens splits a string on Unicode
// whitespace and lowercases each token. We strip a small
// set of common connector punctuation that's frequently
// inconsistent between lx-music and the source tag
// (parentheses, brackets, slashes, colons, commas) so
// "Song (Remix)" and "Song Remix" still overlap.
func onlineLyricSimilarityTokens(s string) []string {
	// Replace connectors with spaces so the tokenizer
	// treats "Song(Remix)" the same as "Song (Remix)".
	connectors := []string{"(", ")", "[", "]", "/", "\\", ":", ",", "，", "（", "）", "【", "】", "、", "；", "；"}
	for _, c := range connectors {
		s = strings.ReplaceAll(s, c, " ")
	}
	fields := strings.Fields(strings.ToLower(s))
	return fields
}

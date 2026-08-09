# Auto-select audit - parsing / filtering / ranking / file selection (2026-08-09)

Scope: `internal/torrents/autoselect/` (comparison.go, autoselect.go, search.go, file_selection.go +
tests), `internal/util/release_name.go`, `internal/util/comparison/`, and the two consumers
`internal/debrid/client/finder.go`, `internal/torrentstream/finder.go`.

Investigation only - no code changed. The four already-fixed defects (subtitle flags as audio, MULTi as
English dub, season match as sort key, IsBestRelease seeder guard) are NOT re-reported; all four were
re-verified as still holding.

## Method

Claims about habari were verified empirically, not inferred: a throwaway module pinned to
github.com/5rahim/habari v0.1.12 (same version as go.mod) ran habari.Parse(CleanReleaseName(...)) over
real AIOStreams multi-line names and scene names. Parse outputs quoted below are actual program output.

---

## HIGH

### H1 - Subtitle languages are credited as audio through parsed.Language (the flag fix only closed the emoji path)
comparison.go:361 (matchesGroup) and the same list at comparison.go:566 (RequireLanguage filter).

Measured parses:

- `[Erai-raws] Show - 07 [1080p][Multiple Subtitle] [ENG][POR-BR][SPA-LA]` -> Lang=[ENG POR-BR SPA-LA]
- AIOStreams `... S01 E04 / Multi Subs|English Subs / (JP audio flag, EN sub flag)` -> Lang=[English]

Both are Japanese-audio releases whose SUBTITLE language list habari puts into Metadata.Language.
matchesGroup(0) matches "eng"/"english" case-insensitively -> +scoreEnglishDub (2000) -> band 3.
Every Erai-raws release (the most common multi-sub JP-audio group) is therefore ranked as an English dub -
the same production symptom the flag fix addressed, reached through a different input path.

Mirror image: `Show.S02E03.VOSTFR.1080p...` -> Lang=[VOSTFR] (French subs, Japanese audio) -> not in any
preferred group, no JP flag, not dual -> -scoreForeignAudio (-2000), i.e. a correct JP release demoted
below the neutral tier.

Fix: require audio-scoped evidence - ignore parsed.Language entries that the name / parsed.Subtitles marks
as a subtitle list, mirroring audioFlagSegment.

### H2 - Year guard buries correctly-season-labelled singles that carry the franchise premiere year
comparison.go:1048-1058.

Measured: `[TB] SeaDex 1080p (Best) / Fruits Basket (2019) S03 - E05` -> Season=[03] Year="2019".
For the AniList S3 entry (mediaYear 2021) diff = 2 > 1 -> priority -= 100000 -> bandGated, despite
c.seasonExact == true being set 30 lines earlier. Aggregator titles come from Stremio series metadata,
which routinely carries the PREMIERE year on every season. Packs are exempt (!isUnlabeledSeasonPack),
singles are not. When only some candidates carry the year, the correct ones sink.

Fix: skip the year guard when c.seasonExact (an explicit season label beats the year as a cour signal).

### H3 - The season gate is a hard drop with no empty-result fallback
comparison.go:527-531 -> filterAndSort -> selectFile.

If ResolveExpectedSeason (autoselect.go:283) returns a season the releases do not use - animap/TMDB
seasonNumberFromMetadata (internal/library/anime/franchise.go:357) can report 2+ for an entry whose
torrents are labelled S01 - every candidate declaring S01 is dropped. filterAndSort returns an empty
slice, selectFile runs limit = 0 and returns ErrNoFileFound. Nothing falls back to the unfiltered list.
Same failure shape for RequireCodec / RequireSource / RequireLanguage / MinSeeders.

Fix: if filterCandidates empties the list, fall back to the unfiltered candidates (score-only demotion).

### H4 - MinSeeders drops every aggregator/debrid result (Seeders == 0)
comparison.go:589. Sibling of the just-fixed isCuratedBestRelease guard: AIOStreams reports seeders ?? 0,
so any profile with MinSeeders > 0 filters the whole list -> ErrNoFileFound. Default profiles set 0, so
this is latent until a user edits the profile.

Fix: treat Seeders <= 0 as unknown and skip the filter, as isCuratedBestRelease now does.

### H5 - Season gate and seasonExact are disabled for season-1 requests
comparison.go:527 (c.expectedSeason >= 2) and comparison.go:1014.

For a season-1 entry (expectedSeason 1 or -1) a candidate that explicitly declares S03 is neither dropped
nor penalised, and episodeCovered passes because the relative episode matches. The aggregator is queried
per SERIES id, so later-season files are in the result set. Requesting S1E05 can select "S03 - E05".

Fix: run the gate/demotion whenever expectedSeason >= 1.

### H6 - No absolute-episode awareness in the episode gate, while the sibling search layer has it
comparison.go:193-212 (episodeCovered) vs internal/torrents/torrent/search.go:726
(isProbablySameEpisode(parsed, ep, animeMetadata.GetOffset())).

Measured: `Boku no Hero Academia - E 53` -> Season=[] Ep=[53]. Requesting S3 E1 gives
episodeCovered([53], 1) == false -> -100000 -> bandGated, while a season-less relative "Show - 01" (very
likely the S1 file) passes every gate and wins. The absolute offset is already available via
metadata.AnimeMetadata.GetOffset().

Fix: pass the absolute offset into buildCandidates and accept requested+offset in episodeCovered.

### H7 - IsBatch (a load-bearing ranking input) is populated only on the preview path, and the search cache is shared
internal/torrents/torrent/search.go:600-607 sets IsBatch = IsBestRelease || ... || len(EpisodeNumber)==0
inside createAnimeTorrentPreview, which is skipped for auto-select (autoselect/search.go:410
SkipPreviews: true, honoured at search.go:391). The provider search cache key (search.go:166) does NOT
include SkipPreviews, and preview generation mutates the shared *AnimeTorrent structs.

So: open the manual list first -> every SeaDex "(Best)" entry (and everything whose episode did not parse)
becomes IsBatch = true; press Play without opening it -> IsBatch stays as the provider set it. IsBatch
feeds isUnlabeledSeasonPack (-3000 sequel demotion + year-guard exemption), sizeTieBreak, BatchPreference
(a Never preference filters every SeaDex release out) and scoreBatch. Auto-select results are therefore
not reproducible and depend on UI history.

---

## MEDIUM

### M1 - Release-group scoring is dead for aggregator names
comparison.go:915-921. Measured: "[TB(cloud)(bolt)] SeaDex 1080p (Best) ... (tag) smol" -> Group="TB"
(the debrid service tag from the leading bracket); the real group "smol" is dropped entirely. Unlike
resolution/codec/source this branch has no containsBoundedTerm(c.lowerName, ...) fallback, so the 50-point
preferred-group weight never applies to the only configured provider.

### M2 - Two different sort ladders decide the same list
comparison.go:663-700 (sortCandidates: band -> seasonExact -> band -> PRIORITY -> BONUS) vs
comparison.go:809-844 (smartCachedPrioritization: band -> seasonExact -> band -> resTier -> best ->
cached -> TOTAL SCORE).
(a) scoreRemux / scoreSeadexBest / HDR / 10-bit live in bonus, so in the torrentstream path any candidate
with +40 more codec/source priority beats them, contradicting the comment at comparison.go:70-75; the test
at langflag_diag_test.go:55 asserts on calculateScore, not on the ladder, so it does not cover this.
(b) resolutionTier (the quality floor) and the SeaDex best rung exist ONLY when postSearchSort != nil -
torrentstream (torrentstream/finder.go:60 passes nil) never gets either.

### M3 - Exclude terms are unbounded substrings
comparison.go:537 uses strings.Contains(c.lowerName, term) while every sibling matcher uses
containsBoundedTerm. Excluding "raw" removes Erai-raws, Beatrice-Raws, ohys-raws.

### M4 - RequireLanguage is stricter than the scorer it claims to mirror
comparison.go:578: the name fallback only runs for len(lang) > 3, so "en"/"eng" never name-match, while
matchesGroup calls containsBoundedTerm for every token length (comparison.go:363). A release the scorer
puts in the dub band can be dropped by the filter.

### M5 - RequireCodec / RequireSource match spellings, not codecs
comparison.go:602-639. "HEVC" does not match "x265", "BluRay" does not match "Blu-Ray"/"BDRip"
(containsBoundedTerm is literal). A profile listing one spelling silently drops equivalent releases; the
shipped test profiles work around it by enumerating variants.

### M6 - RankTorrentsForDisplay discards the pre-sort
internal/debrid/client/finder.go:215-217: the closure ignores its argument and returns statuses built in
the ORIGINAL provider order, so the sortCandidates result inside Rank is thrown away and stable-sort ties
resolve to raw provider order instead of size/seeders.

### M7 - Auto-select debrid file pick has no multi-cour re-resolution
internal/torrents/autoselect/file_selection.go:301 takes the first GetFileByAniDBEpisode hit under
ForceMatch: true. The manual path (debrid/client/finder.go:314-329) detects CountByAniDBEpisode > 1 and
re-analyses with ForceMatch: false + GetFileByMediaIdAndAniDBEpisode. For an auto-selected multi-season
pack the wrong cour episode N can be returned.

### M8 - Specials/OVAs auto-select on the plain episode number
internal/torrentstream/finder.go:37 takes aniDbEpisode and never uses it; autoselect always looks up
strconv.Itoa(episodeNumber) (file_selection.go:198 and :301). AniDB special keys ("S1") cannot be
expressed, so a special resolves to main episode N.

### M9 - Single-file torrents are accepted unverified
file_selection.go:156-164: len(Files()) == 1 -> DownloadAll() and return, with no check that the file is
the requested episode. The episode gate is score-only (never a filter), so a wrong-episode single file
that reaches position 1 is played.

---

## LOW

- L1 comparison.go:781 keys candidateMap by InfoHash while dedup (autoselect/search.go:220) and the debrid
  cache map (debrid/client/finder.go:85) key by Identity(). Currently safe ONLY because
  torrent/search.go:325 backfills InfoHash = Identity(); if that stops, every URL-only result collapses
  onto one candidate score/resTier/seasonExact. Same first-match-wins O(n^2) lookup at comparison.go:151.
- L2 episodeCovered treats a list as a span: measured "Show S01 - E01 & E10" -> Ep=[01 10] -> claims to
  cover 2..9. util.StringToInt truncates E05.5 -> 5, so .5 recaps compete with episode 5.
- L3 declaredSeasons (comparison.go:227) runs ExtractSeasonNumber on the RAW name while habari parsed the
  cleaned one. Measured false positive from the trailing-number rule (util/comparison/filtering.go:134):
  "Sword Art Online: Alicization - War of Underworld 2" -> season 2. The same rule feeds
  GetPossibleSeasonNumber -> ResolveExpectedSeason.
- L4 autoselect/search.go:427 validateBatchResults needs maxSeeders >= 15 || nbFound > 2; with
  seeders-less aggregators a 1-2 result batch search is always discarded.
- L5 resolutionTier (comparison.go:713) and the quality bonuses (comparison.go:1075-1091) use unbounded
  strings.Contains over the whole name ("4k"/"2160" anywhere wins the tier).
- L6 autoselect/search.go:211 collects provider results in goroutine-completion order -> tie order varies
  between runs once more than one provider is configured.
- L7 MaxAnalyzedTorrents (file_selection.go:35) can never trigger: the loop is already capped at
  MaxTorrentCandidatesToCheck = 3. autoselect_test.go:576 still names a "70% threshold" that no longer
  exists in the code.
- L8 the filter/sort shims (comparison.go:295-326) build lowerName from the RAW name while buildCandidates
  uses the cleaned one, so shim-based tests exercise a different matcher.

---

## Verified as NOT broken (looked suspicious, is handled)

- parsed.VideoResolution is empty for most aggregator names (measured: "" whenever an episode number is
  present) - both calculateScoreBreakdown (comparison.go:897) and resolutionTier (:713) fall back to the
  name, so no resolution weight is lost.
- Size filters already guard t.Size > 0 (comparison.go:594, :597), so Size == 0 is not filtered out.
- "MULTi" earns no English credit: measured "Nisekoi.S01E04.MULTi..." -> Language=[], EpisodeTitle="MULTi",
  AudioTerm=[]; the AIOStreams form yields AudioTerm=[Multi Audio], which isDual (comparison.go:389)
  deliberately does not match.
- Subtitle FLAGS are excluded via audioFlagSegment (release_name.go:70).
- seasonCovered spans S1 - S4 correctly (measured Season=[1 4], isPack -> inclusive range).
- scoreBand boundaries: only priority terms (+/-2000, -100000, -3000) can cross the +/-1000 and -50000
  thresholds; format bonuses cap around +150, so no band can be crossed by format alone.
- infoHashFromMagnet is exercised by file_selection_test.go and bounds-checks its cut.
- CleanReleaseName strips the size tokens before parsing, so no "833 MB" is read as episode 833 and no
  "GB" is read as a language (measured on the full 6-line AIOStreams layout).

## Open questions

1. Is AnimeTorrent.Size from AIOStreams the per-file size (1.59 GB) or the pack total (33.6 GB)? It drives
   MaxSize filtering and sizeTieBreak; the name carries both.
2. Does the AIOStreams extension ever set IsBatch / IsBestRelease itself, or is IsBestRelease purely the
   SeaDex "(Best)" tag? The H7 blast radius depends on this.
3. Can animap/TMDB return a season number HIGHER than what releases label (the H3 drop-everything case),
   or only lower/equal?
4. Are the torrents handed to RankTorrentsForDisplay the preview-mutated objects (M6/H7 interaction)?
5. Does the AIOStreams "filename" result format (as opposed to "formatter") ever omit the language flags
   entirely? If so H1 is the only remaining language signal for those entries.

---

## Disposition (2026-08-09, after the fix pass)

Fixed in `afd14e44`, `19b782b0`, `5e722a33` and the commit that follows this note. Each fix
carries a regression test in `autoselect_test.go` built from verbatim production names.

| # | Finding | Status |
|---|---------|--------|
| 1 | Subtitle languages in `parsed.Language` credited as an English dub | Fixed — audio languages derived once in `deriveAudioLanguages`; free-text name fallback disabled when a better source exists |
| 2 | Premiere-year guard buries correctly season-labelled singles | Fixed — releases naming the requested season are exempt |
| 3 | Season gate empties the list, request fails with "no file found" | Fixed — retries without the gate when it removed everything |
| 4 | `MinSeeders` drops every aggregator result (`Seeders == 0`) | Fixed — 0/-1 treated as unknown |
| 8 | Release-group weight dead on aggregator results | Fixed — bounded name fallback |
| 10 | Exclude terms match as bare substrings | Fixed — token-boundary match |
| 11 | `RequireLanguage` stricter than the scorer | Fixed — both read `c.audioLangs` |
| 13 | `finder.go` prioritizer discards `Rank`'s order | Fixed — returns statuses in the requested order, keyed by pointer |
| — | `candidateMap` keyed by `InfoHash`, empty for infohash-less debrid streams | Fixed — keyed by torrent pointer |
| 9 | Resolution floor and curated-best rung skipped when no prioritizer | Fixed — both paths run one ladder |

### Deferred, with reasons

| # | Finding | Why not now |
|---|---------|-------------|
| 5 | Season gate/`seasonExact` only run for `expectedSeason >= 2` | Extending to season 1 would drop correct sequel releases whenever the metadata provider defaults an unmapped cour to season 1 — the exact case finding 3 exists for. Needs the season source to report its own confidence first. |
| 6 | `episodeCovered` has no absolute-offset awareness (`E 53` for S3E1) | Needs `GetOffset()` plumbed from the metadata provider through `filterAndSort`/`Rank`/`buildCandidates` (24 call sites). Loosening the episode gate is the riskiest change in the file and no production log shows it firing. |
| 7 | `IsBatch` backfilled only in `createAnimeTorrentPreview`; search cache key omits `SkipPreviews` | Cross-package fix in `torrent/search.go` with shared mutable state; ordering only shifts when the manual list was opened first, and `BatchPreference` is `neutral` here. |
| 12 | `HEVC` != `x265`, `BluRay` != `Blu-Ray` under `Require*` | Both `RequireCodec` and `RequireSource` are off in the active profile; wants a shared alias table rather than another ad-hoc matcher. |
| 14 | Auto debrid path lacks the manual path's multi-cour re-resolution | Real, but in `file_selection.go`, downstream of everything fixed here — deserves its own pass with a reproduction. |
| 15 | `torrentstream/finder.go` accepts `aniDbEpisode` and never uses it (specials) | Same: needs a specials reproduction before changing episode identity. |
| 16 | Single-file torrent accepted without verifying the episode | Same pass as 14. |
| — | All LOW findings | Recorded above; none observed in production logs. |

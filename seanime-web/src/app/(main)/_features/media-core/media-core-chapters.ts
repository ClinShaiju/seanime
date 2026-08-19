export type MediaCoreChapter = {
    label: string | null
    start: number
    end: number
}

export function getChapterType(name: string | null | undefined) {
    if (!name) return false
    if (/opening$|^opening\s|^op$|^op\s*\d+$/mi.test(name)) return "Opening"
    if (/ending$|^ending\s|^ed$|^ed\s*\d+$|^credits|end credits$|closing credits$/mi.test(name)) return "Ending"
    if (/^intro$/mi.test(name)) return "Intro"
    if (/^outro$/mi.test(name)) return "Outro"
    if (/recap/mi.test(name)) return "Recap"
    return false
}

export function introIsOpening(chapters: Array<{ label: string | null }>) {
    const types = chapters.map(chapter => getChapterType(chapter.label)).filter(Boolean)
    return types.includes("Intro") && !types.includes("Opening")
}

type SkipOptions = {
    guardIntro?: boolean
    /**
     * Fork (19bed7eb): promote a plausibly-sized Intro/Outro, and an unlabeled ~90s chapter near
     * the head/tail, to opening/ending. Upstream detects literal Opening/Ending labels only, so
     * Intro/Outro-labeled and unlabeled muxes never auto-skipped. Opt-in: default keeps upstream's
     * label-only contract (and its tests) exactly as-is.
     */
    heuristics?: boolean
    /** Runtime duration in seconds. Only used by the `heuristics` pass. */
    duration?: number
}

// OP/ED are ~90s in practice (Stremio Kai scores candidates against the same ideal).
const SKIP_IDEAL_LENGTH = 90
const SKIP_MIN_LENGTH = 60
const SKIP_MAX_LENGTH = 150
// Upper bound for the unlabeled-chapter pass. Covers double-length premieres (~47min) while still
// excluding movies, where a ~90s early chapter is usually just a chapter.
const SKIP_MAX_RUNTIME = 3600

function inSkipWindow(chapter: { start?: number, end?: number }) {
    const len = (chapter.end ?? 0) - (chapter.start ?? 0)
    return len >= SKIP_MIN_LENGTH && len <= SKIP_MAX_LENGTH
}

// Best candidate = length closest to 90s. Near-ties (within 15s) are broken by position, and the
// right direction depends on which end of the file we are looking at:
//   head - two ~90s segments at the start usually means [recap/prologue][OP], so the LATER wins.
//   tail - two ~90s segments at the end usually means [ED][next-episode preview], so the EARLIER wins.
// Candidates arrive in start order, so "prefer earlier" is simply the absence of the tiebreak.
function pickSkipCandidate<T extends { start?: number, end?: number }>(candidates: T[], preferLater: boolean): T | null {
    let best: T | null = null
    let bestScore = Infinity
    for (const c of candidates) {
        const score = Math.abs(((c.end ?? 0) - (c.start ?? 0)) - SKIP_IDEAL_LENGTH)
        if (score < bestScore - 15 || (preferLater && score <= bestScore + 15 && (!best || (c.start ?? 0) > (best.start ?? 0)))) {
            best = c
            bestScore = Math.min(score, bestScore)
        }
    }
    return best
}

export function getDefaultSkipChapters<T extends { label: string | null, start?: number, end?: number }>(
    chapters: T[],
    options: SkipOptions = {},
) {
    let opening: T | null = null
    let ending: T | null = null
    const usesIntro = options.guardIntro !== false && introIsOpening(chapters)
    const heuristics = options.heuristics === true

    // Pass 1a: literal Opening/Ending labels are trusted as-is and outrank every heuristic below.
    for (const chapter of chapters) {
        const type = getChapterType(chapter.label)
        if (!opening && !usesIntro && type === "Opening") opening = chapter
        if (!ending && !usesIntro && type === "Ending") ending = chapter
        if (opening && ending) break
    }

    // Pass 1b: Intro/Outro (common in anime muxes for the actual OP/ED) are only a fallback for when
    // no literal label exists - an "Intro" sitting next to an "Opening" is the cold open, not the OP.
    // They also need a plausible ~90s length, so a long cold open labeled "Intro" isn't auto-skipped.
    if (heuristics && !usesIntro && (!opening || !ending)) {
        for (const chapter of chapters) {
            const type = getChapterType(chapter.label)
            if (!opening && type === "Intro" && inSkipWindow(chapter)) opening = chapter
            if (!ending && type === "Outro" && inSkipWindow(chapter)) ending = chapter
        }
    }

    // Pass 2: duration heuristic for generically-labeled chapters ("Part A", "Chapter 2", ""):
    // a ~90s chapter near the head/tail of the file is almost certainly the OP/ED.
    // Episode-length files only - in a movie a ~90s early chapter is usually just a chapter.
    const duration = options.duration ?? 0
    if (heuristics && duration > SKIP_MAX_LENGTH * 2 && duration < SKIP_MAX_RUNTIME && (!opening || !ending)) {
        // Head/tail window: 20% of the runtime, floored at 5min so an OP behind a long cold open is
        // still in range, capped at 8min so a double-length episode doesn't turn this into a search
        // of the whole first act.
        const edge = Math.min(Math.max(duration * 0.2, 300), 480)
        const candidates = chapters.filter(c => !getChapterType(c.label) && inSkipWindow(c))
        if (!opening) {
            opening = pickSkipCandidate(candidates.filter(c => (c.start ?? 0) < edge), true)
        }
        if (!ending) {
            ending = pickSkipCandidate(candidates.filter(c => (c.end ?? 0) > duration - edge && c !== opening), false)
        }
    }

    return { opening, ending }
}

function getPatterns(value: string) {
    return value
        .split(",")
        .map(pattern => pattern.trim())
        .filter(Boolean)
}

function getRegexes(value: string) {
    return getPatterns(value).flatMap(pattern => {
        try {
            return [new RegExp(pattern, "i")]
        }
        catch {
            return []
        }
    })
}

export function getSkipPatternError(value: string) {
    for (const pattern of getPatterns(value)) {
        try {
            new RegExp(pattern, "i")
        }
        catch {
            return `Invalid regex: ${pattern}`
        }
    }
    return ""
}

export function getSkipChapters<T extends { label: string | null, start?: number, end?: number }>(chapters: T[], patterns: string, options: SkipOptions = {}) {
    const defaults = getDefaultSkipChapters(chapters, options)
    const regexes = getRegexes(patterns)

    return chapters.filter(chapter => {
        if (chapter === defaults.opening || chapter === defaults.ending) return true
        const label = chapter.label
        if (!label) return false
        return regexes.some(regex => regex.test(label))
    })
}

export function getSkipLabel(name: string | null) {
    const type = getChapterType(name)
    if (type === "Opening" || type === "Ending") return type
    return name?.trim() || "Chapter"
}

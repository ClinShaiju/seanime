import { describe, expect, it } from "vitest"
import { getDefaultSkipChapters, getSkipChapters, getSkipPatternError } from "./media-core-chapters"

function chapters(...labels: string[]) {
    return labels.map((label, index) => ({
        label,
        start: index * 60,
        end: (index + 1) * 60,
    }))
}

describe("chapter skipping", () => {
    it("keeps the first default opening and ending", () => {
        const list = chapters("Opening", "Episode", "Opening 2", "Credits")

        expect(getSkipChapters(list, "")).toEqual([list[0], list[3]])
    })

    it("keeps the existing intro chapter rule", () => {
        const list = chapters("Intro", "Episode", "Ending")

        expect(getSkipChapters(list, "")).toEqual([])
        expect(getSkipChapters(list, "", { guardIntro: false })).toEqual([list[2]])
    })

    it("adds all chapters matching custom regexes", () => {
        const list = chapters("Intro", "Episode", "Next Episode Preview", "Outro")

        expect(getSkipChapters(list, "^intro$,preview,^outro$")).toEqual([list[0], list[2], list[3]])
    })

    it("matches custom regexes case insensitively", () => {
        const list = chapters("PREVIEW")

        expect(getSkipChapters(list, "^preview$")).toEqual(list)
    })

    it("reports invalid regexes", () => {
        expect(getSkipPatternError("^Preview$,(")).toBe("Invalid regex: (")
        expect(getSkipPatternError("^Preview$")).toBe("")
    })
})

// Fork (19bed7eb): OP/ED auto-skip must also work for Intro/Outro-labeled and unlabeled muxes.
// Upstream detects literal Opening/Ending labels only, so these releases never auto-skipped.
describe("fork skip heuristics", () => {
    const fork = { guardIntro: false, heuristics: true, duration: 1440 } as const

    it("treats a ~90s Intro/Outro as the opening/ending", () => {
        const list = [
            { label: "Intro", start: 0, end: 90 },
            { label: "Part A", start: 90, end: 700 },
            { label: "Outro", start: 700, end: 790 },
        ]
        expect(getSkipChapters(list, "", fork)).toEqual([list[0], list[2]])
    })

    it("does not skip an over-long chapter merely labeled Intro", () => {
        const list = [
            { label: "Intro", start: 0, end: 400 },
            { label: "Part A", start: 400, end: 1400 },
        ]
        expect(getSkipChapters(list, "", fork)).toEqual([])
    })

    it("promotes an unlabeled ~90s chapter near the head/tail", () => {
        const list = [
            { label: "Part A", start: 0, end: 90 },
            { label: "Part B", start: 90, end: 1300 },
            { label: "Part C", start: 1300, end: 1390 },
        ]
        expect(getSkipChapters(list, "", fork)).toEqual([list[0], list[2]])
    })

    it("stays label-only when heuristics are off (upstream default)", () => {
        const list = [
            { label: "Intro", start: 0, end: 90 },
            { label: "Part A", start: 90, end: 700 },
        ]
        expect(getSkipChapters(list, "", { guardIntro: false })).toEqual([])
    })

    // A literal Opening/Ending always wins over a same-length Intro/Outro, whatever the order.
    // Regression: the Intro/Outro heuristic used to share pass 1 with the literal labels, so the
    // first-match-wins loop skipped the cold open and left the real OP playing.
    it("prefers a literal Opening over an earlier Intro", () => {
        const list = [
            { label: "Intro", start: 0, end: 90 },
            { label: "Opening", start: 90, end: 180 },
            { label: "Part A", start: 180, end: 1300 },
        ]
        expect(getDefaultSkipChapters(list, fork).opening).toBe(list[1])
    })

    it("prefers a literal Ending over an earlier Outro", () => {
        const list = [
            { label: "Part B", start: 0, end: 1200 },
            { label: "Outro", start: 1200, end: 1290 },
            { label: "Ending", start: 1290, end: 1380 },
        ]
        expect(getDefaultSkipChapters(list, fork).ending).toBe(list[2])
    })

    it("falls back to Intro/Outro when there is no literal label", () => {
        const list = [
            { label: "Intro", start: 0, end: 90 },
            { label: "Part A", start: 90, end: 1300 },
            { label: "Outro", start: 1300, end: 1390 },
        ]
        const { opening, ending } = getDefaultSkipChapters(list, fork)
        expect(opening).toBe(list[0])
        expect(ending).toBe(list[2])
    })

    it("reads two unlabeled ~90s chapters at the head as [recap][OP]", () => {
        const list = [
            { label: "Chapter 1", start: 0, end: 90 },
            { label: "Chapter 2", start: 90, end: 180 },
            { label: "Chapter 3", start: 180, end: 1440 },
        ]
        expect(getDefaultSkipChapters(list, fork).opening).toBe(list[1])
    })

    // Regression: the near-tie tiebreak used to prefer the later chapter at both ends, so a
    // 60-150s next-episode preview stole the pick from the ED just before it.
    it("reads two unlabeled ~90s chapters at the tail as [ED][preview]", () => {
        const list = [
            { label: "Chapter 1", start: 0, end: 1250 },
            { label: "Chapter 2", start: 1250, end: 1340 },
            { label: "Chapter 3", start: 1340, end: 1440 },
        ]
        expect(getDefaultSkipChapters(list, fork).ending).toBe(list[1])
    })

    it("promotes an unlabeled OP sitting behind a long cold open", () => {
        const list = [
            { label: "Chapter 1", start: 0, end: 290 },
            { label: "Chapter 2", start: 290, end: 380 },
            { label: "Chapter 3", start: 380, end: 1440 },
        ]
        expect(getDefaultSkipChapters(list, fork).opening).toBe(list[1])
    })

    it("still applies the heuristics to a double-length episode", () => {
        const list = [
            { label: "Chapter 1", start: 0, end: 90 },
            { label: "Chapter 2", start: 90, end: 2730 },
            { label: "Chapter 3", start: 2730, end: 2820 },
        ]
        const { opening, ending } = getDefaultSkipChapters(list, { ...fork, duration: 2820 })
        expect(opening).toBe(list[0])
        expect(ending).toBe(list[2])
    })

    it("leaves movie-length files alone", () => {
        const list = [
            { label: "Chapter 1", start: 0, end: 90 },
            { label: "Chapter 2", start: 90, end: 7200 },
        ]
        expect(getDefaultSkipChapters(list, { ...fork, duration: 7200 }).opening).toBeNull()
    })
})

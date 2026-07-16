import { describe, expect, it } from "vitest"
import { getSkipChapters, getSkipPatternError } from "./media-core-chapters"

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
})

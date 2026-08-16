import { Habari_Metadata } from "@/api/generated/types"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/components/ui/core/styling"
import { Tooltip } from "@/components/ui/tooltip"
import startCase from "lodash/startCase"
import React from "react"
import { LiaMicrophoneSolid } from "react-icons/lia"
import { LuGauge } from "react-icons/lu"
import {
    PiBatteryFullDuotone,
    PiBatteryHighDuotone,
    PiBatteryLowDuotone,
    PiBatteryMediumDuotone,
    PiChatCircleDotsDuotone,
    PiChatTeardropDuotone,
    PiChatTextDuotone,
    PiSmileyNervousDuotone,
} from "react-icons/pi"

export function TorrentResolutionBadge({ resolution }: { resolution?: string }) {

    if (!resolution) return null

    return (
        <Badge
            data-torrent-item-resolution-badge
            className="rounded-[--radius-md] border-transparent bg-gray-900/50 px-1 text-md"
            intent={resolution?.includes("1080")
                ? "indigo"
                : (resolution?.includes("2160") || resolution?.toLowerCase().includes("4k"))
                    ? "blue"
                    : (resolution?.includes("720")
                        ? "success"
                        : "gray")}
        >
            {resolution}
        </Badge>
    )
}

export function TorrentSeedersBadge({ seeders }: { seeders: number }) {

    let Icon = seeders >= 50 ? PiBatteryFullDuotone : seeders >= 20 ? PiBatteryHighDuotone :
        seeders >= 10 ? PiBatteryMediumDuotone :
            seeders >= 5 ? PiBatteryMediumDuotone :
                seeders > 0 ? PiBatteryLowDuotone :
                    PiSmileyNervousDuotone

    if (seeders === -1) return null

    return (
        <Badge
            data-torrent-item-seeders-badge
            className="rounded-[--radius-md] border-transparent bg-transparent px-0 gap-1 font-normal opacity-80"
            // intent={(seeders) > 4 ? (seeders) > 19 ? "primary" : "success" : "gray"}
            intent={"gray"}
            leftIcon={<Icon
                className={cn(
                    "text-xl mr-0.5",
                    seeders >= 50 ? "text-[--indigo]" : seeders >= 10 ? "text-[--green]" : seeders >= 5 ? "text-orange-300" : "text-[--red]",
                )}
            />}
        >
            <span
                className={cn("text-[.9rem] font-normal",
                    seeders >= 50 ? "text-[--indigo]" : seeders >= 10 ? "text-[--green]" : seeders >= 5 ? "text-orange-300" : "text-[--red]",
                )}
            >{seeders || "No"}</span><span className="text-[--muted] text-[.9rem]">seeder{seeders != 1
            ? "s"
            : ""}</span>
        </Badge>
    )

}


export function TorrentParsedMetadata({ metadata }: { metadata: Habari_Metadata | undefined }) {

    if (!metadata) return null

    const hasDubs = metadata?.subtitles?.some(n => n.toLocaleLowerCase().includes("dub"))
    // const hasSubs = metadata?.subtitles?.some(n => n.toLocaleLowerCase().includes("sub"))
    const hasMultiSubs = metadata?.subtitles?.some(n => n.toLocaleLowerCase().includes("multi"))

    // metadata.language holds AUDIO languages only: the server runs util.DeriveAudioLanguages (the
    // same rule auto-select ranks by) before sending the preview, so subtitle languages stay in
    // metadata.subtitles and can't be badged as audio here. Do NOT infer audio from anything else —
    // habari's raw language list is where subtitle languages live, which is how
    // "[Erai-raws] Show - 07 [Multiple Subtitle] [ENG][POR-BR][SPA-LA]" used to get a "Dubbed"
    // badge on a Japanese-audio release.
    const languages = !!metadata?.language?.length ? [...new Set(metadata?.language)] : []

    const isJp = (l: string) => { const x = l.toLowerCase().trim(); return x === "jp" || x === "jpn" || x === "ja" || x.includes("japan") }
    const isEng = (l: string) => { const x = l.toLowerCase().trim(); return x === "en" || x === "eng" || x.includes("english") }
    // Mirrors util.IsDualAudioRelease: "dual" and "dub" in audio_term both mean the JP original
    // plus an English dub. "multi" does not — in scene naming MULTi is French (VF + original) — so
    // a multi-audio release only reaches the dub badges when the server resolved it to real
    // languages (util.IsServiceMultiAudio, i.e. multi-audio from a Western streaming service).
    const isDualTerm = (t: string) => { const x = t.toLowerCase(); return x.includes("dual") || x.includes("dub") }
    const isMultiTerm = (t: string) => t.toLowerCase().includes("multi")
    const hasTextualDual = !!metadata?.audio_term?.some(isDualTerm)
    // Original + Dub: the Japanese original AND English. Requires English specifically, not merely
    // "not Japanese" — a LoliHouse "🌐 🇯🇵 / 🇨🇳" release is Japanese audio with Chinese subtitles,
    // which auto-select demotes as a foreign-market release (scoreForeignMarketRelease) rather than
    // crediting as a dub. Its two language chips already say what it declares.
    const showFlagDual = !hasTextualDual && languages.some(isJp) && languages.some(isEng)
    // Dubbed: a non-Japanese audio language with no Japanese original at all. For anime the original
    // is always JP, so this is a dub-only release — the foreign-audio tier in ranking.
    const showFlagDubbed = !hasTextualDual && !hasDubs && !languages.some(isJp) && languages.some(l => !!l && !isJp(l))

    const filterHEVC = (n: string) => {
        return !(n.toLocaleLowerCase().includes("265") && metadata?.video_term?.map(n => n.toLocaleLowerCase()).includes("hevc"))
    }

    return (
        <div className="flex flex-row gap-1 flex-wrap justify-end w-full lg:absolute top-0 right-0">
            {!!languages?.length && languages.length == 2 ? languages.slice(0, 2)?.map(term => (
                <Badge
                    key={term}
                    className="rounded-md bg-transparent border-transparent px-1"
                >
                    <PiChatTeardropDuotone className="text-lg text-[--blue]" /> {term}
                </Badge>
            )) : null}
            {metadata?.video_term?.filter(filterHEVC).map(term => (
                <Badge
                    key={term}
                    className="rounded-md border-transparent bg-transparent text-[.8rem] text-[--foreground] px-1"
                >
                    {term}
                </Badge>
            ))}
            {metadata?.audio_term?.filter(term => !isDualTerm(term) && !isMultiTerm(term))
                .map(term => (
                    <Badge
                        key={term}
                        className="rounded-md border-transparent bg-transparent text-[.8rem] text-[--foreground] px-1 opacity-60"
                    >
                        {term}
                    </Badge>
                ))}
            {!!languages?.length && languages.length > 2 ? <Tooltip
                trigger={<Badge
                    className="rounded-md bg-transparent border-transparent px-1"
                >
                    <PiChatTextDuotone className="text-lg text-[--blue]" /> Languages
                </Badge>}
            >
                <span>
                    {languages.join(", ")}
                </span>
            </Tooltip> : null}
            {metadata?.audio_term?.filter(term => isDualTerm(term) || isMultiTerm(term)).map(term => (
                <Badge
                    key={term}
                    className="rounded-md border-transparent bg-[--subtle] text-[.8rem] px-1"
                >
                    {/* <LuAudioWaveform className="text-lg text-[--blue]" /> {term} */}
                    <LiaMicrophoneSolid className="text-lg text-[--rose]" /> {isDualTerm(term)
                    ? "Original + Dub"
                    : startCase(term)}
                </Badge>
            ))}
            {showFlagDual && (
                <Badge
                    className="rounded-md border-transparent bg-[--subtle] text-[.8rem] px-1"
                >
                    <LiaMicrophoneSolid className="text-lg text-[--rose]" /> Original + Dub
                </Badge>
            )}
            {(hasDubs || showFlagDubbed) && (
                <Badge
                    className="rounded-md border-transparent bg-indigo-300 px-1"
                >
                    <LiaMicrophoneSolid className="text-lg text-[--red]" /> Dubbed
                </Badge>
            )}
            {hasMultiSubs && (
                <Badge
                    className="rounded-md border-transparent bg-indigo-300 px-1"
                >
                    <PiChatCircleDotsDuotone className="text-lg text-[--blue]" /> Multi Subs
                </Badge>
            )}
        </div>
    )
}


export function TorrentDebridInstantAvailabilityBadge() {

    return (
        <Tooltip
            trigger={<Badge
                data-torrent-item-debrid-instant-availability-badge
                className="rounded-[--radius-md] bg-transparent border-transparent dark:text-[--indigo] animate-pulse p-0"
                intent="white"
            >
                <LuGauge className="text-xl" />
            </Badge>}
        >
            Instantly available on Debrid service
        </Tooltip>
    )

}

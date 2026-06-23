import { NativePlayer_PlaybackInfo } from "@/api/generated/types"
import { atom } from "jotai"
import { atomWithImmer } from "jotai-immer"

export type NativePlayerState = {
    active: boolean
    playbackInfo: NativePlayer_PlaybackInfo | null
    playbackError: string | null
    loadingState: string | null
}

export const nativePlayer_initialState: NativePlayerState = {
    active: false,
    playbackInfo: null,
    playbackError: null,
    loadingState: null,
}

export const nativePlayer_stateAtom = atomWithImmer<NativePlayerState>(nativePlayer_initialState)

// Bumped to request the player tear down its current stream from outside the player component
// (e.g. a watch-room follower told the controller stopped the episode). The player watches
// this counter and runs its normal terminate path.
export const nativePlayer_terminateRequestedAtom = atom(0)

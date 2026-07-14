// Pure watch-room follower reconciliation — split out of nakama-room-sync.ts so both convergence
// directions are unit-testable without React (see nakama-sync-reconcile.test.ts).
//
// A follower continuously steers its local player toward the driver's reported position. The old
// inline logic converged FORWARD (hard-seek to catch up when behind) but REFUSED to rewind to ANY
// behind-driver — a patch for a stalled iOS driver that kept heartbeating paused:false at a frozen
// position (rewinding to it every heartbeat = rubber-band). That patch also blocked LEGITIMATE
// rewinds, so a follower that got ahead for any reason (differing OP/ED auto-skip, buffer overshoot,
// a missed emit, clock skew) stayed desynced forever (F25). The fix distinguishes the two by whether
// the driver's position is ADVANCING across heartbeats (live → converge, incl. rewind) vs FROZEN
// (stalled → hold, no rubber-band).

export const SEEK_THRESHOLD = 0.75 // discrete: only seek when off by more than this (avoids jitter)
// Smooth-convergence tuning (followers only). Instead of hard-seeking on every bit of drift (which
// stutters), a follower nudges its playbackRate a few percent to GLIDE back into sync:
//   |drift| < DEADBAND        -> normal speed (already in sync)
//   DEADBAND..HARD_SEEK_DRIFT -> rate = 1 + clamp(drift*GAIN, ±MAX); eases in, never jumps
//   > HARD_SEEK_DRIFT         -> hard seek (a real gap from a seek/buffer; snap instantly)
// NUDGE_MAX 0.05 = ±5% ≈ a barely-perceptible pitch shift (a semitone is ~6%); GAIN makes the nudge
// proportional so it shrinks as it converges (no oscillation).
export const SYNC_DEADBAND = 0.08
export const HARD_SEEK_DRIFT = 0.6
export const NUDGE_GAIN = 0.12
export const NUDGE_MAX = 0.05

export type FollowerSyncAction = { seek: boolean, rate: number }

// decideFollowerSync: given the drift (target - local; positive = driver ahead of us, negative = we
// are ahead of the driver), whether this is a heartbeat vs a discrete action, whether the driver's
// reported position is ADVANCING (its feed is live, not frozen), and whether a hard-seek cooldown is
// active, decide the seek/rate the follower should apply.
export function decideFollowerSync(args: {
    isHeartbeat: boolean
    drift: number
    driverAdvancing: boolean
    seekCooldownActive: boolean
}): FollowerSyncAction {
    const { isHeartbeat, drift, driverAdvancing, seekCooldownActive } = args
    const ad = Math.abs(drift)

    // Discrete actions (a real user play/pause/seek) snap precisely, in BOTH directions.
    if (!isHeartbeat) return { seek: ad > SEEK_THRESHOLD, rate: 1 }

    // Heartbeat drift correction. A hard seek is returned ONLY when it will actually fire; when the
    // seek is suppressed (post-seek cooldown), we deliberately FALL THROUGH to the proportional nudge
    // below so a far-off follower keeps GLIDING toward the driver every heartbeat instead of coasting
    // at rate 1.0 between seeks (the regression: the rate latched once and stopped tracking drift).
    if (drift > HARD_SEEK_DRIFT && !seekCooldownActive) {
        // Behind the driver -> snap forward.
        return { seek: true, rate: 1 }
    }
    if (drift < -HARD_SEEK_DRIFT) {
        // Ahead of the driver. Rewind to CONVERGE only when the driver is LIVE (advancing) — a real
        // divergence (e.g. we skipped the OP and it didn't). Otherwise HOLD at normal speed: when the
        // driver is FROZEN (stalled but still heartbeating paused:false) rewinding every heartbeat is
        // the rubber-band, and nudging DOWN toward it would needlessly stall us; during the seek
        // cooldown we also just hold. A deliberate controller rewind arrives as a DISCRETE seek
        // (handled above) so it always snaps regardless.
        if (driverAdvancing && !seekCooldownActive) return { seek: true, rate: 1 }
        return { seek: false, rate: 1 }
    }
    if (ad > SYNC_DEADBAND) {
        // Small drift (either direction) OR a far-behind gap while the seek is cooling down: glide via
        // a proportional playbackRate nudge, clamped to ±NUDGE_MAX. Recomputed every heartbeat so it
        // continuously tracks the live drift rather than latching a single rate.
        const off = Math.max(-NUDGE_MAX, Math.min(NUDGE_MAX, drift * NUDGE_GAIN))
        return { seek: false, rate: 1 + off }
    }
    return { seek: false, rate: 1 } // converged
}

// roomStreamKey: stable identity of a room's CURRENT stream instance. Opt-out is keyed by this, not
// by bare roomId, so declining one episode does not suppress auto-follow for the next one (F18).
export function roomStreamKey(
    roomId: string,
    mediaId: number | undefined | null,
    episode: number | undefined | null,
): string {
    return `${roomId}:${mediaId ?? 0}:${episode ?? 0}`
}

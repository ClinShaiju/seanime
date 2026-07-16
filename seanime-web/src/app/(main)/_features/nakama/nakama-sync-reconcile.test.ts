// Self-checks for the nakama watch-room follower sync decision table.
// Upstream v3.10.0 added vitest ("npm test"); this file used to be a plain top-level assert
// script run via `npx tsx`, which vitest collects but reports as "no test suite found".
// Wrapped in a suite so it runs as part of the normal test run.
import assert from "node:assert"
import { it } from "vitest"
import { decideFollowerSync, roomStreamKey } from "./nakama-sync-reconcile"

it("nakama follower sync reconcile", () => {

    // --- behind a LIVE driver -> snap forward ---
    assert.deepEqual(
        decideFollowerSync({ isHeartbeat: true, drift: 5, driverAdvancing: true, seekCooldownActive: false }),
        { seek: true, rate: 1 },
        "behind driver should seek forward",
    )
    // ...but not while cooling down from a recent seek (let the nudge finish, don't thrash directstream).
    // Regression guard: it must still NUDGE forward (rate > 1) during the cooldown, not coast at 1.0 —
    // a far-behind follower that latched rate 1 between seeks stopped catching up ("applies a speed once").
    const behindCooling = decideFollowerSync({ isHeartbeat: true, drift: 5, driverAdvancing: true, seekCooldownActive: true })
    assert.equal(behindCooling.seek, false, "behind + cooldown should not seek")
    assert.ok(behindCooling.rate > 1 && behindCooling.rate <= 1.05, "behind + cooldown must still nudge forward (not coast at 1.0)")

    // --- F25: AHEAD of a LIVE driver (e.g. we skipped the OP, it didn't) -> rewind back to converge ---
    assert.deepEqual(
        decideFollowerSync({ isHeartbeat: true, drift: -85, driverAdvancing: true, seekCooldownActive: false }),
        { seek: true, rate: 1 },
        "ahead of a LIVE driver should rewind to converge",
    )

    // --- iOS fix preserved: AHEAD of a FROZEN driver (stalled, heartbeating paused:false) -> HOLD ---
    assert.equal(
        decideFollowerSync({ isHeartbeat: true, drift: -85, driverAdvancing: false, seekCooldownActive: false }).seek,
        false,
        "ahead of a FROZEN driver must NOT rewind (rubber-band)",
    )

    // --- discrete actions snap in both directions, ignore cooldown/advancing ---
    assert.equal(
        decideFollowerSync({ isHeartbeat: false, drift: -85, driverAdvancing: false, seekCooldownActive: true }).seek,
        true,
        "discrete seek snaps even when frozen/cooling",
    )
    assert.equal(
        decideFollowerSync({ isHeartbeat: false, drift: 0.1, driverAdvancing: false, seekCooldownActive: false }).seek,
        false,
        "discrete within SEEK_THRESHOLD does not seek",
    )

    // --- small drift glides via nudge (bidirectional), no seek, rate within ±5% ---
    const fwd = decideFollowerSync({ isHeartbeat: true, drift: 0.3, driverAdvancing: true, seekCooldownActive: false })
    assert.equal(fwd.seek, false)
    assert.ok(fwd.rate > 1 && fwd.rate <= 1.05, "small +drift nudges rate up ≤5%")
    const back = decideFollowerSync({ isHeartbeat: true, drift: -0.3, driverAdvancing: false, seekCooldownActive: false })
    assert.equal(back.seek, false)
    assert.ok(back.rate < 1 && back.rate >= 0.95, "small -drift nudges rate down ≤5%")

    // --- converged -> normal speed ---
    assert.deepEqual(
        decideFollowerSync({ isHeartbeat: true, drift: 0.02, driverAdvancing: true, seekCooldownActive: false }),
        { seek: false, rate: 1 },
        "within deadband = normal speed",
    )

    // --- roomStreamKey: new episode => different key => not pre-suppressed by an earlier opt-out ---
    assert.notEqual(roomStreamKey("r1", 100, 1), roomStreamKey("r1", 100, 2), "different episode = different key")
    assert.equal(roomStreamKey("r1", 100, 1), roomStreamKey("r1", 100, 1), "same stream = same key")
    assert.equal(roomStreamKey("r1", null, undefined), "r1:0:0", "nullish media/episode coalesce to 0")
})

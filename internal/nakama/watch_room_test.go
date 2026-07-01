package nakama

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// newTestHub builds a hub with no manager (broadcasts are guarded and no-op), so the
// membership/control/promotion logic can be exercised in isolation.
func newTestHub() *WatchRoomHub {
	l := zerolog.Nop()
	return NewWatchRoomHub(nil, &l)
}

func localUser(name string) PoolUser {
	return PoolUser{Username: name, Source: PoolSourceLocal}
}

func TestWatchRoom_AuthoritativeStateAndStreamInfo(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "client-alice", "Room", "")

	// Host reports a debrid play at t=100.
	h.RelayPlaybackStatus("client-alice", &RoomPlaybackStatusPayload{
		RoomId: room.ID, Paused: false, CurrentTime: 100, MediaId: 42, EpisodeNumber: 5,
		AniDBEpisode: "5", StreamType: WatchPartyStreamTypeDebrid,
	})

	info := h.StreamInfo(room.ID)
	if !info.Active || info.MediaId != 42 || info.EpisodeNumber != 5 || info.StreamType != WatchPartyStreamTypeDebrid {
		t.Fatalf("expected active debrid stream media 42 ep5, got %+v", info)
	}

	// Live position advances while playing; pause freezes it.
	room2, _ := h.GetRoom(room.ID)
	room2.mu.RLock()
	playing := room2.currentPositionLocked()
	room2.mu.RUnlock()
	if playing < 100 {
		t.Fatalf("playing position should be >= 100, got %.2f", playing)
	}

	h.RelayPlaybackStatus("client-alice", &RoomPlaybackStatusPayload{
		RoomId: room.ID, Paused: true, CurrentTime: 150, MediaId: 42, EpisodeNumber: 5, StreamType: WatchPartyStreamTypeDebrid,
	})
	time.Sleep(15 * time.Millisecond)
	room2.mu.RLock()
	paused := room2.currentPositionLocked()
	room2.mu.RUnlock()
	if paused != 150 {
		t.Fatalf("paused position should be frozen at 150, got %.2f", paused)
	}

	// Stop deactivates the room playback.
	h.RelayPlaybackStatus("client-alice", &RoomPlaybackStatusPayload{RoomId: room.ID, Stopped: true})
	if h.StreamInfo(room.ID).Active {
		t.Fatal("stream should be inactive after stop")
	}
}

func TestWatchRoom_CreateJoinLeave(t *testing.T) {
	h := newTestHub()

	host := localUser("alice")
	room, err := h.CreateRoom(host, "client-alice", "Movie Night", "")
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if room.HostKey != host.Key() || room.ControllerKey != host.Key() {
		t.Fatalf("host/controller not set to creator: host=%s ctrl=%s", room.HostKey, room.ControllerKey)
	}
	if len(h.ListRooms()) != 1 {
		t.Fatalf("expected 1 room card, got %d", len(h.ListRooms()))
	}

	// Open room: empty password joins fine.
	if _, err := h.JoinRoom(room.ID, localUser("bob"), "client-bob", ""); err != nil {
		t.Fatalf("JoinRoom bob: %v", err)
	}
	if got := h.ListRooms()[0].MemberCount; got != 2 {
		t.Fatalf("expected 2 members, got %d", got)
	}

	// Bob leaves; room persists with alice.
	if err := h.LeaveRoom(room.ID, localUser("bob").Key()); err != nil {
		t.Fatalf("LeaveRoom bob: %v", err)
	}
	if _, ok := h.GetRoom(room.ID); !ok {
		t.Fatalf("room should still exist after bob leaves")
	}

	// Alice (host) leaves; room empties and is removed.
	if err := h.LeaveRoom(room.ID, host.Key()); err != nil {
		t.Fatalf("LeaveRoom alice: %v", err)
	}
	if _, ok := h.GetRoom(room.ID); ok {
		t.Fatalf("room should be removed when empty")
	}
}

func TestWatchRoom_Password(t *testing.T) {
	h := newTestHub()
	room, _ := h.CreateRoom(localUser("alice"), "c1", "Locked", "hunter2")
	if !room.HasPassword {
		t.Fatal("room should be password-protected")
	}
	if _, err := h.JoinRoom(room.ID, localUser("bob"), "c2", "wrong"); err != ErrRoomWrongPassword {
		t.Fatalf("expected ErrRoomWrongPassword, got %v", err)
	}
	if _, err := h.JoinRoom(room.ID, localUser("bob"), "c2", "hunter2"); err != nil {
		t.Fatalf("correct password should join: %v", err)
	}
	// Reconnect (already a member) must succeed even without re-supplying the password —
	// the new ws connection carries a fresh clientId and no password.
	if _, err := h.JoinRoom(room.ID, localUser("bob"), "c2-reconnect", ""); err != nil {
		t.Fatalf("existing member reconnect should not require the password: %v", err)
	}
}

func TestWatchRoom_PromotionByJoinOrder(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "client-alice", "Room", "")

	// bob joins before carol → bob is next in line.
	if _, err := h.JoinRoom(room.ID, localUser("bob"), "client-bob", ""); err != nil {
		t.Fatal(err)
	}
	// Force a strictly later join time for carol so ordering is deterministic.
	time.Sleep(2 * time.Millisecond)
	if _, err := h.JoinRoom(room.ID, localUser("carol"), "client-carol", ""); err != nil {
		t.Fatal(err)
	}

	// Host's controller client drops → control hands off to the next by join order, which
	// is bob (joined before carol). Promotion picks the earliest OTHER participant.
	h.HandleClientDisconnect("client-alice")
	room, _ = h.GetRoom(room.ID)
	if room == nil {
		t.Fatal("room should survive a host client drop")
	}
	if room.ControllerKey != localUser("bob").Key() {
		t.Fatalf("expected bob promoted (earliest remaining joiner), got %s", room.ControllerKey)
	}
}

func TestWatchRoom_HostDisconnectKeepsRoomAndReclaim(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "client-alice", "Room", "")
	time.Sleep(2 * time.Millisecond)
	h.JoinRoom(room.ID, localUser("bob"), "client-bob", "")

	// Host's client drops (not a leave). Control should hand off to bob; room stays.
	h.HandleClientDisconnect("client-alice")
	room, _ = h.GetRoom(room.ID)
	if room.ControllerKey != localUser("bob").Key() {
		t.Fatalf("expected bob to take control on host drop, got %s", room.ControllerKey)
	}
	if _, ok := room.Participants[host.Key()]; !ok {
		t.Fatal("host should remain a participant on disconnect (may reconnect)")
	}

	// Host reconnects (re-joins) → reclaims control.
	if _, err := h.JoinRoom(room.ID, host, "client-alice-2", ""); err != nil {
		t.Fatal(err)
	}
	room, _ = h.GetRoom(room.ID)
	if room.ControllerKey != host.Key() {
		t.Fatalf("host should reclaim control on reconnect, got %s", room.ControllerKey)
	}
}

func TestWatchRoom_HostLeaveClosesRoom(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "client-alice", "Room", "")
	time.Sleep(2 * time.Millisecond)
	h.JoinRoom(room.ID, localUser("bob"), "client-bob", "")

	// Host leaves intentionally → the whole room is torn down (not promoted to bob).
	if err := h.LeaveRoom(room.ID, host.Key()); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.GetRoom(room.ID); r != nil {
		t.Fatal("host leaving should close the room for everyone")
	}

	// A non-host leaving only removes that member; the room stays for the rest.
	room2, _ := h.CreateRoom(host, "client-alice", "Room2", "")
	h.JoinRoom(room2.ID, localUser("bob"), "client-bob", "")
	if err := h.LeaveRoom(room2.ID, localUser("bob").Key()); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.GetRoom(room2.ID); r == nil {
		t.Fatal("a non-host leaving must not close the room")
	}
}

func TestWatchRoom_RelayEnforcement(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "client-alice", "Room", "")
	h.JoinRoom(room.ID, localUser("bob"), "client-bob", "")
	h.JoinRoom(room.ID, localUser("carol"), "client-carol", "")
	room, _ = h.GetRoom(room.ID)

	// Host may drive; relays to the other two, never echoes the sender.
	targets, ok := room.resolveRelay("client-alice")
	if !ok {
		t.Fatal("host should be allowed to drive")
	}
	if len(targets) != 2 || contains(targets, "client-alice") {
		t.Fatalf("host relay targets wrong: %v", targets)
	}

	// Bob has no control by default → not allowed.
	if _, ok := room.resolveRelay("client-bob"); ok {
		t.Fatal("bob should not be allowed to drive by default")
	}

	// Unknown client → not allowed.
	if _, ok := room.resolveRelay("ghost"); ok {
		t.Fatal("unknown client should not be allowed to drive")
	}

	// Host grants control to everyone → bob may now drive (multi-source control).
	if err := h.SetControl(room.ID, host.Key(), "", true, true); err != nil {
		t.Fatal(err)
	}
	targets, ok = room.resolveRelay("client-bob")
	if !ok {
		t.Fatal("bob should drive after host grants control to all")
	}
	if len(targets) != 2 || contains(targets, "client-bob") {
		t.Fatalf("bob relay targets wrong (should exclude self): %v", targets)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestWatchRoom_AutoSkipVote(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "c1", "Room", "")
	h.JoinRoom(room.ID, localUser("bob"), "c2", "")
	h.JoinRoom(room.ID, localUser("carol"), "c3", "")

	// All default "auto" → no explicit votes → off.
	room, _ = h.GetRoom(room.ID)
	if room.EffectiveAutoSkip || room.AutoSkipVotesOn != 0 || room.AutoSkipVotesOff != 0 {
		t.Fatalf("all-auto should be off/0/0, got eff=%v on=%d off=%d", room.EffectiveAutoSkip, room.AutoSkipVotesOn, room.AutoSkipVotesOff)
	}

	// alice + bob vote on, carol off → majority on.
	h.SetAutoSkipPref(room.ID, host.Key(), "on")
	h.SetAutoSkipPref(room.ID, localUser("bob").Key(), "on")
	h.SetAutoSkipPref(room.ID, localUser("carol").Key(), "off")
	room, _ = h.GetRoom(room.ID)
	if !room.EffectiveAutoSkip || room.AutoSkipVotesOn != 2 || room.AutoSkipVotesOff != 1 {
		t.Fatalf("expected on (2 vs 1), got eff=%v on=%d off=%d", room.EffectiveAutoSkip, room.AutoSkipVotesOn, room.AutoSkipVotesOff)
	}

	// Tie (1 on, 1 off, 1 auto) → off.
	h.SetAutoSkipPref(room.ID, host.Key(), "auto")
	room, _ = h.GetRoom(room.ID)
	if room.EffectiveAutoSkip || room.AutoSkipVotesOn != 1 || room.AutoSkipVotesOff != 1 {
		t.Fatalf("tie should be off (1 vs 1), got eff=%v on=%d off=%d", room.EffectiveAutoSkip, room.AutoSkipVotesOn, room.AutoSkipVotesOff)
	}

	// Invalid pref coerces to "auto".
	if err := h.SetAutoSkipPref(room.ID, localUser("bob").Key(), "nonsense"); err != nil {
		t.Fatal(err)
	}
	room, _ = h.GetRoom(room.ID)
	if room.Participants[localUser("bob").Key()].AutoSkipPref != "auto" {
		t.Fatal("invalid pref should coerce to auto")
	}
}

func TestWatchRoom_SetControl(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "c1", "Room", "")
	h.JoinRoom(room.ID, localUser("bob"), "c2", "")

	bobKey := localUser("bob").Key()
	if h.CanControl(room.ID, bobKey) {
		t.Fatal("bob should not control by default")
	}
	// Non-host cannot grant.
	if err := h.SetControl(room.ID, bobKey, bobKey, true, false); err != ErrNotRoomHost {
		t.Fatalf("expected ErrNotRoomHost, got %v", err)
	}
	// Host grants bob control.
	if err := h.SetControl(room.ID, host.Key(), bobKey, true, false); err != nil {
		t.Fatal(err)
	}
	if !h.CanControl(room.ID, bobKey) {
		t.Fatal("bob should control after grant")
	}
	// Host always controls.
	if !h.CanControl(room.ID, host.Key()) {
		t.Fatal("host should always control")
	}
}

// TestWatchRoom_ConcurrentSnapshotMarshal guards the concurrency fix: the live room (with its
// Participants map) must never be JSON-marshaled while a membership op mutates the map, which the
// Go runtime turns into a fatal "concurrent map read and map write". broadcastRoomState and the
// HTTP handlers now marshal room.Snapshot() instead. Run under -race to prove it's clean.
func TestWatchRoom_ConcurrentSnapshotMarshal(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "client-alice", "Room", "")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Churn membership (mutates room.Participants under room.mu). Host stays, so the room lives.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			u := localUser(fmt.Sprintf("u%d", i%8))
			h.JoinRoom(room.ID, u, fmt.Sprintf("c%d", i%8), "")
			h.LeaveRoom(room.ID, u.Key())
		}
	}()

	// Concurrently marshal snapshots — what broadcastRoomState / the HTTP handlers do. Marshaling
	// the LIVE room here would crash the process; the snapshot must not.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 3000; i++ {
			if _, err := json.Marshal(room.Snapshot()); err != nil {
				t.Errorf("marshal snapshot: %v", err)
				return
			}
		}
	}()

	time.Sleep(60 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Snapshot is a real copy: a later mutation of the live room must not change a prior snapshot.
	snap := room.Snapshot()
	before := len(snap.Participants)
	h.JoinRoom(room.ID, localUser("zoe"), "client-zoe", "")
	if len(snap.Participants) != before {
		t.Fatalf("snapshot participants changed after a later join: %d -> %d", before, len(snap.Participants))
	}
}

// TestWatchRoom_ReapIdleRooms covers the zombie-room reaper: a room is kept while it has a live
// client (and its idle timer refreshes), kept while within roomIdleTTL of its last live client,
// and closed once it has had no live client for longer than the TTL.
func TestWatchRoom_ReapIdleRooms(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "client-alice", "Room", "")

	// Host's client is live → kept, and lastLiveAt is refreshed to `now`.
	h.reapIdleRoomsWith(map[string]struct{}{"client-alice": {}}, time.Now())
	if _, ok := h.GetRoom(room.ID); !ok {
		t.Fatal("room with a live client must not be reaped")
	}

	// No live clients but within the TTL → kept.
	h.reapIdleRoomsWith(map[string]struct{}{}, time.Now())
	if _, ok := h.GetRoom(room.ID); !ok {
		t.Fatal("room within idle TTL must not be reaped")
	}

	// No live clients and past the TTL → reaped.
	h.reapIdleRoomsWith(map[string]struct{}{}, time.Now().Add(roomIdleTTL+time.Second))
	if _, ok := h.GetRoom(room.ID); ok {
		t.Fatal("room idle past the TTL should be reaped")
	}
}

// A bare pause from a controlling member must apply (room pauses) but must NOT transfer the
// controller: a stalled player emitting a pause is indistinguishable from a user pause, and a
// stall stealing control anchored the room to a frozen player (the rubber-band). A seek or a
// play from the same member still hands control over.
func TestWatchRoom_BarePauseDoesNotStealControl(t *testing.T) {
	h := newTestHub()
	host := localUser("alice")
	room, _ := h.CreateRoom(host, "c1", "Room", "")
	h.JoinRoom(room.ID, localUser("bob"), "c2", "")
	_ = h.SetControl(room.ID, host.Key(), localUser("bob").Key(), true, false)

	// Host starts playback at t=100.
	h.RelayPlaybackStatus("c1", &RoomPlaybackStatusPayload{
		RoomId: room.ID, Paused: false, CurrentTime: 100, MediaId: 1, EpisodeNumber: 1, StreamType: WatchPartyStreamTypeDebrid,
	})
	if room.ControllerKey != host.Key() {
		t.Fatalf("host should be controller, got %s", room.ControllerKey)
	}

	// Wait out the cross-echo debounce so bob's action counts as genuine.
	time.Sleep(echoDebounce + 50*time.Millisecond)

	// Bob PAUSES at (about) the current position: applies, but control stays with the host.
	room.mu.RLock()
	pos := room.currentPositionLocked()
	room.mu.RUnlock()
	h.RelayPlaybackStatus("c2", &RoomPlaybackStatusPayload{
		RoomId: room.ID, Paused: true, CurrentTime: pos, MediaId: 1, EpisodeNumber: 1, StreamType: WatchPartyStreamTypeDebrid,
	})
	room.mu.RLock()
	paused, ctrl := room.paused, room.ControllerKey
	room.mu.RUnlock()
	if !paused {
		t.Fatal("bob's pause should apply to the room state")
	}
	if ctrl != host.Key() {
		t.Fatalf("a bare pause must not transfer control, got %s", ctrl)
	}

	time.Sleep(echoDebounce + 50*time.Millisecond)

	// Bob SEEKS: a deliberate act — control hands over.
	h.RelayPlaybackStatus("c2", &RoomPlaybackStatusPayload{
		RoomId: room.ID, Paused: true, CurrentTime: pos + 300, MediaId: 1, EpisodeNumber: 1, StreamType: WatchPartyStreamTypeDebrid,
	})
	room.mu.RLock()
	ctrl = room.ControllerKey
	room.mu.RUnlock()
	if ctrl != localUser("bob").Key() {
		t.Fatalf("a seek should hand control to bob, got %s", ctrl)
	}
}

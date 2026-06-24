package nakama

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"seanime/internal/events"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Same-instance watch rooms.
//
// This is a PARALLEL, lighter-weight system to the host/peer watch-party in
// watch_party*.go. The peer/host party is for federation between separate Seanime
// instances; these rooms are for many users sharing ONE backend (the VPS). The two
// coexist. See nakama-room.md (repo root) for the full design.
//
// Identity comes from the caller (the handler resolves it via the profile/identity
// layer); the hub never touches the DB. Each participant is a PoolUser, namespaced by
// source so external-instance users wire in later without reshaping anything.

type PoolUserSource string

const (
	PoolSourceLocal    PoolUserSource = "local"
	PoolSourceExternal PoolUserSource = "external"
)

// PoolUser identifies a user in the hub's pool. The Key is the stable, collision-safe
// identifier: local users key on their username; external users are namespaced by their
// origin server tag so an external "alice" never collides with a local "alice".
type PoolUser struct {
	UserID    uint           `json:"userId"`   // Seanime user id (0 = local single-user/admin install)
	Username  string         `json:"username"` // display name (bare, un-namespaced)
	Source    PoolUserSource `json:"source"`
	ServerTag string         `json:"serverTag,omitempty"` // external origin; "" for local
}

// Key returns the stable pool key. local -> "local:alice"; external -> "ext:srv1:alice".
func (u PoolUser) Key() string {
	if u.Source == PoolSourceExternal {
		return fmt.Sprintf("ext:%s:%s", u.ServerTag, u.Username)
	}
	return "local:" + u.Username
}

// RoomParticipant is a PoolUser inside a specific room.
type RoomParticipant struct {
	User       PoolUser  `json:"user"`
	ClientID   string    `json:"clientId"`   // the UI ws client id this participant drives from
	IsHost     bool      `json:"isHost"`     // the ORIGINAL host (room creator)
	CanControl bool      `json:"canControl"` // may drive play/pause/seek/episode
	JoinedAt   time.Time `json:"joinedAt"`   // for promotion ordering
	// AutoSkipPref is this member's OP/ED auto-skip vote: "on" | "off" | "auto".
	// "auto" defers to the room majority; "on"/"off" count as votes. Default "auto".
	AutoSkipPref string `json:"autoSkipPref"`
}

// WatchRoom is one same-instance room. Playback sync state is layered on later
// (task: per-room sync); this type is the membership + control spine.
type WatchRoom struct {
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	HostKey       string                      `json:"hostKey"`       // original host's pool key (room owner)
	ControllerKey string                      `json:"controllerKey"` // effective driver (host, or a promoted member)
	HasPassword   bool                        `json:"hasPassword"`
	// ForceHostTracks (default false): when true, the host's audio/subtitle selection is
	// pushed to every member, overriding their own. Off = each member picks their own.
	ForceHostTracks bool                        `json:"forceHostTracks"`
	Participants    map[string]*RoomParticipant `json:"participants"` // keyed by PoolUser.Key()
	// CurrentMediaInfo reuses the watch-party media type; nil until the host starts playback.
	CurrentMediaInfo *WatchPartySessionMediaInfo `json:"currentMediaInfo"`
	// LastPlayback is the most recent relayed control action (position + media). Sent in
	// the room state so a late joiner can start the same media at the current position.
	LastPlayback *RoomPlaybackStatusPayload `json:"lastPlayback"`
	// Auto-skip vote result (recomputed on membership/preference change). EffectiveAutoSkip
	// is what the controller's player acts on; the vote counts drive the x/x UI display.
	EffectiveAutoSkip bool `json:"effectiveAutoSkip"`
	AutoSkipVotesOn   int  `json:"autoSkipVotesOn"`
	AutoSkipVotesOff  int  `json:"autoSkipVotesOff"`
	CreatedAt         time.Time `json:"createdAt"`

	// PlaybackActive: is a stream currently running in the room? Drives the "join stream" UI
	// (a peer who isn't watching it sees a join button). The server is authoritative for the
	// playback state below — it's reported by the controller and broadcast to all members.
	PlaybackActive bool `json:"playbackActive"`

	// Authoritative playback state (server source of truth). Not exported directly; the live
	// position is computed (position + elapsed while playing) into the broadcast payload.
	paused     bool
	position   float64 // seconds, as of positionAt
	positionAt time.Time

	passwordHash string       // sha256 hex of the password; empty = open room
	mu           sync.RWMutex `json:"-"`
}

// currentPositionLocked returns the authoritative live position (caller holds room.mu).
func (room *WatchRoom) currentPositionLocked() float64 {
	if room.paused || room.positionAt.IsZero() {
		return room.position
	}
	return room.position + time.Since(room.positionAt).Seconds()
}

// playbackBroadcastLocked builds the authoritative sync payload with the server-computed live
// position (caller holds room.mu). Returns nil when nothing is playing. heartbeat=true marks
// the periodic broadcast (followers reconcile it with a looser threshold); discrete actions
// are sent with heartbeat=false for a precise apply.
func (room *WatchRoom) playbackBroadcastLocked(heartbeat bool) *RoomPlaybackStatusPayload {
	mi := room.CurrentMediaInfo
	if !room.PlaybackActive || mi == nil {
		return nil
	}
	return &RoomPlaybackStatusPayload{
		RoomId:        room.ID,
		Paused:        room.paused,
		CurrentTime:   room.currentPositionLocked(),
		MediaId:       mi.MediaId,
		EpisodeNumber: mi.EpisodeNumber,
		AniDBEpisode:  mi.AniDBEpisode,
		StreamType:    mi.StreamType,
		Heartbeat:     heartbeat,
	}
}

// RoomPlaybackStatusPayload is a control action relayed between members. It carries
// ONLY position + media identity — deliberately NO audio/subtitle track fields, so each
// member keeps their own track selection (per-user tracks). client->server on a control
// action (play/pause/seek/episode change), server->followers to apply.
type RoomPlaybackStatusPayload struct {
	RoomId        string               `json:"roomId"`
	Paused        bool                 `json:"paused"`
	CurrentTime   float64              `json:"currentTime"`
	Duration      float64              `json:"duration"`
	MediaId       int                  `json:"mediaId"`
	EpisodeNumber int                  `json:"episodeNumber"`
	AniDBEpisode  string               `json:"aniDbEpisode"`
	StreamType    WatchPartyStreamType `json:"streamType"`
	// Stopped marks the controller ending the episode (closing the player). Followers stop
	// theirs too — the mirror of auto-start. When true the media fields are meaningless.
	Stopped bool `json:"stopped,omitempty"`
	// Heartbeat marks a periodic position broadcast from the controller (not a discrete
	// play/pause/seek action). Followers use it to reconcile position (catch up on drift)
	// using a looser threshold, so they stay in sync during continuous playback.
	Heartbeat bool `json:"heartbeat,omitempty"`
	// AudioTrack/SubtitleTrack are only set by the host when ForceHostTracks is on, so
	// members can mirror the host's selection. nil otherwise (per-user tracks).
	AudioTrack    *int `json:"audioTrack,omitempty"`
	SubtitleTrack *int `json:"subtitleTrack,omitempty"`
}

// RoomCard is the public, listing-safe view of a room shown on the discovery cards.
// It deliberately omits the participant list (no global userlist; members are only
// visible inside a room) and the password hash. mediaId/episode let the frontend render
// the cover from its own metadata — the hub does no metadata lookups.
type RoomCard struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	HostUsername  string `json:"hostUsername"`
	MemberCount   int    `json:"memberCount"`
	HasPassword   bool   `json:"hasPassword"`
	MediaId       int    `json:"mediaId,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	// Title and CoverImage are enriched by the handler from the anime collection
	// (the hub itself does no metadata lookups). Empty when media isn't resolved.
	Title      string `json:"title,omitempty"`
	CoverImage string `json:"coverImage,omitempty"`
}

// WatchRoomHub holds all same-instance rooms.
type WatchRoomHub struct {
	manager *Manager
	logger  *zerolog.Logger

	mu    sync.RWMutex
	rooms map[string]*WatchRoom

	stopOnce sync.Once
	stop     chan struct{}
}

// broadcastTickMs is how often the server fans out each active room's authoritative position
// to all members, so followers stay within ~1s of the controller during steady playback.
const broadcastTickMs = 1000

func NewWatchRoomHub(manager *Manager, logger *zerolog.Logger) *WatchRoomHub {
	h := &WatchRoomHub{
		manager: manager,
		logger:  logger,
		rooms:   make(map[string]*WatchRoom),
		stop:    make(chan struct{}),
	}
	// ponytail: one process-lifetime ticker for all rooms (rooms are few; members are few).
	go h.runBroadcastLoop()
	return h
}

// runBroadcastLoop periodically fans out every active room's authoritative (server-computed)
// playback state to all its members. This is what keeps followers synced during steady
// playback — the controller only needs to report discrete actions + occasional corrections.
func (h *WatchRoomHub) runBroadcastLoop() {
	ticker := time.NewTicker(broadcastTickMs * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			if h.manager == nil || h.manager.wsEventManager == nil {
				continue
			}
			for _, room := range h.snapshotRooms() {
				room.mu.RLock()
				payload := room.playbackBroadcastLocked(true)
				// The authoritative position is derived from the controller's reports, so the
				// controller is the source — don't echo it back to itself (that's what caused
				// the self-driven oscillation). Everyone else (incl. a non-driving host)
				// reconciles to it.
				var controllerClientID string
				if ctrl, ok := room.Participants[room.ControllerKey]; ok {
					controllerClientID = ctrl.ClientID
				}
				clientIDs := make([]string, 0, len(room.Participants))
				if payload != nil {
					for _, p := range room.Participants {
						if p.ClientID != "" && p.ClientID != controllerClientID {
							clientIDs = append(clientIDs, p.ClientID)
						}
					}
				}
				room.mu.RUnlock()
				for _, cid := range clientIDs {
					h.manager.wsEventManager.SendEventTo(cid, events.NakamaRoomPlaybackSync, payload, true)
				}
			}
		}
	}
}

// Stop ends the broadcast loop. Idempotent.
func (h *WatchRoomHub) Stop() {
	h.stopOnce.Do(func() { close(h.stop) })
}

var (
	ErrRoomNotFound       = errors.New("room not found")
	ErrRoomWrongPassword  = errors.New("incorrect room password")
	ErrRoomNameRequired   = errors.New("room name is required")
	ErrNotRoomHost        = errors.New("only the room host can do this")
	ErrParticipantUnknown = errors.New("participant not in room")
)

func hashRoomPassword(pw string) string {
	if pw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

// CreateRoom creates a room owned by `host` and adds them as the first participant
// (host + controller, can control). Returns the new room.
func (h *WatchRoomHub) CreateRoom(host PoolUser, clientID, name, password string) (*WatchRoom, error) {
	if name == "" {
		return nil, ErrRoomNameRequired
	}

	hostKey := host.Key()
	now := time.Now()
	room := &WatchRoom{
		ID:            uuid.New().String(),
		Name:          name,
		HostKey:       hostKey,
		ControllerKey: hostKey,
		HasPassword:   password != "",
		passwordHash:  hashRoomPassword(password),
		Participants: map[string]*RoomParticipant{
			hostKey: {
				User:         host,
				ClientID:     clientID,
				IsHost:       true,
				CanControl:   true,
				JoinedAt:     now,
				AutoSkipPref: "auto",
			},
		},
		CreatedAt: now,
	}

	h.mu.Lock()
	h.rooms[room.ID] = room
	h.mu.Unlock()

	h.logf("created room %q (%s) host=%s", name, room.ID, host.Username)
	h.broadcastRoomsUpdated()
	return room, nil
}

// JoinRoom adds `user` to the room (validating the password). Idempotent: re-joining
// updates the participant's clientID (e.g. a reconnect/new tab) without duplicating.
func (h *WatchRoomHub) JoinRoom(roomID string, user PoolUser, clientID, password string) (*WatchRoom, error) {
	room, ok := h.getRoom(roomID)
	if !ok {
		return nil, ErrRoomNotFound
	}

	room.mu.Lock()
	key := user.Key()
	if p, exists := room.Participants[key]; exists {
		// Reconnect / new tab: refresh the driving client, keep host/control/joinedAt.
		// No password re-check — they're already a member (and a reconnect carries none).
		p.ClientID = clientID
		// If this is the original host returning, control reverts to them (§2.6).
		if p.IsHost {
			room.ControllerKey = key
		}
		room.mu.Unlock()
		h.broadcastRoomState(room)
		return room, nil
	}

	// New join: enforce the room password.
	if room.passwordHash != "" && hashRoomPassword(password) != room.passwordHash {
		room.mu.Unlock()
		return nil, ErrRoomWrongPassword
	}

	room.Participants[key] = &RoomParticipant{
		User:         user,
		ClientID:     clientID,
		IsHost:       false,
		CanControl:   false,
		JoinedAt:     time.Now(),
		AutoSkipPref: "auto",
	}
	room.recomputeAutoSkipLocked()
	room.mu.Unlock()

	h.logf("user %s joined room %s", user.Username, roomID)
	h.broadcastRoomState(room)
	h.broadcastRoomsUpdated() // member count changed on the card
	return room, nil
}

// LeaveRoom removes a participant. If the original host leaves, control is promoted to
// the next member by join order; if the room empties, it is deleted.
func (h *WatchRoomHub) LeaveRoom(roomID string, userKey string) error {
	room, ok := h.getRoom(roomID)
	if !ok {
		// Idempotent: the room is already gone (e.g. the host closed it while we were
		// disconnected and we missed the close event). Treat as a successful leave so the
		// client can clear its local room state instead of getting stuck on a 500.
		return nil
	}

	room.mu.Lock()
	if _, exists := room.Participants[userKey]; !exists {
		room.mu.Unlock()
		return nil // already not a member — idempotent
	}
	// The host leaving = closing the room intentionally: tear it down and tell every other
	// member to stop playback. (A host whose CLIENT merely drops keeps the room alive — that
	// path is HandleClientDisconnect, which never removes the participant.)
	if userKey == room.HostKey {
		others := make([]string, 0, len(room.Participants))
		for k, p := range room.Participants {
			if k != userKey && p.ClientID != "" {
				others = append(others, p.ClientID)
			}
		}
		room.mu.Unlock()
		h.mu.Lock()
		delete(h.rooms, roomID)
		h.mu.Unlock()
		h.logf("host left room %s, closed", roomID)
		if h.manager != nil && h.manager.wsEventManager != nil {
			for _, cid := range others {
				h.manager.wsEventManager.SendEventTo(cid, events.NakamaWatchRoomClosed, roomID, true)
			}
		}
		h.broadcastRoomsUpdated()
		return nil
	}
	delete(room.Participants, userKey)
	empty := len(room.Participants) == 0
	// If the effective controller left, promote the next by join order.
	if room.ControllerKey == userKey && !empty {
		room.ControllerKey = nextControllerKeyLocked(room, userKey)
	}
	room.recomputeAutoSkipLocked()
	room.mu.Unlock()

	if empty {
		h.mu.Lock()
		delete(h.rooms, roomID)
		h.mu.Unlock()
		h.logf("room %s emptied, removed", roomID)
	} else {
		h.broadcastRoomState(room)
	}
	h.broadcastRoomsUpdated()
	return nil
}

// HandleClientDisconnect is called when a UI ws client goes away. It does NOT remove the
// participant (the user may be reconnecting); it only hands off control if the dropped
// client was the effective controller, so playback keeps driving. The original host
// reclaims control when they JoinRoom again (§2.6).
func (h *WatchRoomHub) HandleClientDisconnect(clientID string) {
	for _, room := range h.snapshotRooms() {
		room.mu.Lock()
		ctrl, ok := room.Participants[room.ControllerKey]
		if ok && ctrl.ClientID == clientID {
			room.ControllerKey = nextControllerKeyLocked(room, room.ControllerKey)
			h.logf("controller client %s dropped in room %s, promoted %s", clientID, room.ID, room.ControllerKey)
			room.mu.Unlock()
			h.broadcastRoomState(room)
			continue
		}
		room.mu.Unlock()
	}
}

// SetControl lets the room host grant/revoke control for a member (or, with all=true,
// every non-host member). v1 granularity: one boolean covering play/pause+seek+episode.
func (h *WatchRoomHub) SetControl(roomID, hostKey, targetKey string, canControl, all bool) error {
	room, ok := h.getRoom(roomID)
	if !ok {
		return ErrRoomNotFound
	}

	room.mu.Lock()
	defer room.mu.Unlock()
	if room.HostKey != hostKey {
		return ErrNotRoomHost
	}

	if all {
		for k, p := range room.Participants {
			if k == room.HostKey {
				continue
			}
			p.CanControl = canControl
		}
	} else {
		p, exists := room.Participants[targetKey]
		if !exists {
			return ErrParticipantUnknown
		}
		p.CanControl = canControl
	}

	go h.broadcastRoomState(room)
	return nil
}

// SetForceHostTracks toggles whether the host's audio/subtitle selection is forced onto
// all members (host only). Default off.
func (h *WatchRoomHub) SetForceHostTracks(roomID, hostKey string, value bool) error {
	room, ok := h.getRoom(roomID)
	if !ok {
		return ErrRoomNotFound
	}

	room.mu.Lock()
	if room.HostKey != hostKey {
		room.mu.Unlock()
		return ErrNotRoomHost
	}
	room.ForceHostTracks = value
	room.mu.Unlock()

	h.broadcastRoomState(room)
	return nil
}

// recomputeAutoSkipLocked tallies OP/ED auto-skip votes (caller holds room.mu). "on"/"off"
// are explicit votes; "auto" defers. Strict majority of on>off wins; tie = off (don't skip).
func (room *WatchRoom) recomputeAutoSkipLocked() {
	on, off := 0, 0
	for _, p := range room.Participants {
		switch p.AutoSkipPref {
		case "on":
			on++
		case "off":
			off++
		}
	}
	room.AutoSkipVotesOn = on
	room.AutoSkipVotesOff = off
	room.EffectiveAutoSkip = on > off
}

// SetAutoSkipPref sets a member's OP/ED auto-skip vote and recomputes the room result.
func (h *WatchRoomHub) SetAutoSkipPref(roomID, userKey, pref string) error {
	if pref != "on" && pref != "off" && pref != "auto" {
		pref = "auto"
	}
	room, ok := h.getRoom(roomID)
	if !ok {
		return ErrRoomNotFound
	}
	room.mu.Lock()
	p, exists := room.Participants[userKey]
	if !exists {
		room.mu.Unlock()
		return ErrParticipantUnknown
	}
	p.AutoSkipPref = pref
	room.recomputeAutoSkipLocked()
	room.mu.Unlock()

	h.broadcastRoomState(room)
	return nil
}

// CanControl reports whether the participant identified by userKey may drive playback in
// the room (host always can; others per their flag). Used by the sync relay (task 4).
func (h *WatchRoomHub) CanControl(roomID, userKey string) bool {
	room, ok := h.getRoom(roomID)
	if !ok {
		return false
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	p, exists := room.Participants[userKey]
	return exists && (p.IsHost || p.CanControl)
}

// RelayPlaybackStatus relays a member's control action to the other room members.
// Enforces control: the sender (resolved by their ws client id) must be allowed to drive
// — the host always may, others only when the host granted them control. Multi-source by
// design: with control granted to all, ANY member's play/pause/seek propagates to
// everyone (last-write-wins). The sender is never echoed back to.
func (h *WatchRoomHub) RelayPlaybackStatus(senderClientID string, p *RoomPlaybackStatusPayload) {
	room, ok := h.getRoom(p.RoomId)
	if !ok {
		return
	}

	targets, allowed := room.resolveRelay(senderClientID)
	if !allowed {
		h.logf("dropping playback status from client %s (not allowed to control room %s)", senderClientID, p.RoomId)
		return
	}

	_ = targets // membership fan-out is computed below; resolveRelay is kept for its permission check

	// Update the AUTHORITATIVE room state from the controller's report. The server — not the
	// controller's client — is now the source of truth: it holds {paused, position} and the
	// broadcast loop fans the computed live position out to everyone.
	room.mu.Lock()
	room.LastPlayback = p
	if p.Stopped {
		room.PlaybackActive = false
		room.paused = true
	} else {
		room.PlaybackActive = true
		room.paused = p.Paused
		room.position = p.CurrentTime
		room.positionAt = time.Now()
		room.CurrentMediaInfo = &WatchPartySessionMediaInfo{
			MediaId:       p.MediaId,
			EpisodeNumber: p.EpisodeNumber,
			AniDBEpisode:  p.AniDBEpisode,
			StreamType:    p.StreamType,
		}
	}
	// A discrete action (play/pause/seek/stop) broadcasts immediately for snappiness; a
	// periodic heartbeat just updates state and lets the ticker fan it out.
	var immediate *RoomPlaybackStatusPayload
	if p.Stopped {
		immediate = p // forward the stop verbatim so followers tear down
	} else if !p.Heartbeat {
		immediate = room.playbackBroadcastLocked(false)
	}
	// Broadcast to every member EXCEPT the sender. Echoing a member's own action back to it
	// and relying on a client-side "ignore my echo" guard is fragile — a missed guard makes
	// the controller drive itself into a play/pause oscillation. Excluding the sender makes
	// self-feedback impossible regardless of the client.
	targetIDs := make([]string, 0, len(room.Participants))
	for _, rp := range room.Participants {
		if rp.ClientID != "" && rp.ClientID != senderClientID {
			targetIDs = append(targetIDs, rp.ClientID)
		}
	}
	isController := room.ControllerKey != "" && func() bool {
		ctrl, ok := room.Participants[room.ControllerKey]
		return ok && ctrl.ClientID == senderClientID
	}()
	room.mu.Unlock()

	// Diagnostic: log discrete actions (not the periodic heartbeats) so the full sync
	// conversation is visible — who sent it, whether they're the controller, the paused/
	// position, and how many followers received it (0 = the relay isn't reaching anyone).
	if !p.Heartbeat {
		h.logf("relay sender=%s controller=%v paused=%v t=%.1f stopped=%v media=%d ep=%d -> %d follower(s)",
			senderClientID, isController, p.Paused, p.CurrentTime, p.Stopped, p.MediaId, p.EpisodeNumber, len(targetIDs))
	}

	if immediate == nil || h.manager == nil || h.manager.wsEventManager == nil {
		return
	}
	for _, cid := range targetIDs {
		h.manager.wsEventManager.SendEventTo(cid, events.NakamaRoomPlaybackSync, immediate, true)
	}
}

// RoomStreamInfo is the active-stream summary the "join stream" path needs: what to play
// (media identity) and whose resolved debrid link to reuse (the controller's user id).
type RoomStreamInfo struct {
	Active           bool
	MediaId          int
	EpisodeNumber    int
	AniDBEpisode     string
	StreamType       WatchPartyStreamType
	ControllerUserID uint
}

// StreamInfo returns the room's current active-stream summary (Active=false if nothing is
// playing). Used by the join-stream endpoint so a peer can (re)join the host's stream.
func (h *WatchRoomHub) StreamInfo(roomID string) RoomStreamInfo {
	room, ok := h.getRoom(roomID)
	if !ok {
		return RoomStreamInfo{}
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	if !room.PlaybackActive || room.CurrentMediaInfo == nil {
		return RoomStreamInfo{}
	}
	mi := room.CurrentMediaInfo
	var uid uint
	if ctrl, ok := room.Participants[room.ControllerKey]; ok {
		uid = ctrl.User.UserID
	}
	return RoomStreamInfo{
		Active:           true,
		MediaId:          mi.MediaId,
		EpisodeNumber:    mi.EpisodeNumber,
		AniDBEpisode:     mi.AniDBEpisode,
		StreamType:       mi.StreamType,
		ControllerUserID: uid,
	}
}

// resolveRelay returns the other members' client ids to relay to, and whether the sender
// (identified by ws client id) is allowed to drive playback. Pure (no I/O) so the
// enforcement is unit-testable without a manager.
func (room *WatchRoom) resolveRelay(senderClientID string) (targets []string, allowed bool) {
	room.mu.RLock()
	defer room.mu.RUnlock()

	var senderKey string
	for k, p := range room.Participants {
		if p.ClientID == senderClientID {
			senderKey = k
			break
		}
	}
	sender, ok := room.Participants[senderKey]
	if !ok || !(sender.IsHost || sender.CanControl) {
		return nil, false
	}

	for k, p := range room.Participants {
		if k == senderKey || p.ClientID == "" {
			continue
		}
		targets = append(targets, p.ClientID)
	}
	return targets, true
}

// ListRooms returns the discovery cards for every room (visible pool-wide).
func (h *WatchRoomHub) ListRooms() []*RoomCard {
	rooms := h.snapshotRooms()
	cards := make([]*RoomCard, 0, len(rooms))
	for _, room := range rooms {
		room.mu.RLock()
		card := &RoomCard{
			ID:           room.ID,
			Name:         room.Name,
			MemberCount:  len(room.Participants),
			HasPassword:  room.HasPassword,
			HostUsername: room.hostUsernameLocked(),
		}
		if room.CurrentMediaInfo != nil {
			card.MediaId = room.CurrentMediaInfo.MediaId
			card.EpisodeNumber = room.CurrentMediaInfo.EpisodeNumber
		}
		room.mu.RUnlock()
		cards = append(cards, card)
	}
	return cards
}

// GetRoom returns a room by id.
func (h *WatchRoomHub) GetRoom(roomID string) (*WatchRoom, bool) {
	return h.getRoom(roomID)
}

//////////////////////////////////////////////////////////////////////////////////////
// internals
//////////////////////////////////////////////////////////////////////////////////////

func (h *WatchRoomHub) getRoom(roomID string) (*WatchRoom, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	r, ok := h.rooms[roomID]
	return r, ok
}

func (h *WatchRoomHub) snapshotRooms() []*WatchRoom {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*WatchRoom, 0, len(h.rooms))
	for _, r := range h.rooms {
		out = append(out, r)
	}
	return out
}

// nextControllerKeyLocked picks the next controller by join order: the earliest joiner
// other than excludeKey (the dropped/leaving controller). Caller must hold room.mu.
// Returns "" if no candidate remains.
func nextControllerKeyLocked(room *WatchRoom, excludeKey string) string {
	type kp struct {
		key string
		at  time.Time
	}
	cands := make([]kp, 0, len(room.Participants))
	for k, p := range room.Participants {
		if k == excludeKey {
			continue
		}
		cands = append(cands, kp{k, p.JoinedAt})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at.Before(cands[j].at) })
	return cands[0].key
}

func (room *WatchRoom) hostUsernameLocked() string {
	if p, ok := room.Participants[room.HostKey]; ok {
		return p.User.Username
	}
	return ""
}

func (h *WatchRoomHub) logf(format string, args ...interface{}) {
	if h.logger != nil {
		h.logger.Debug().Msgf("nakama/rooms: "+format, args...)
	}
}

// broadcastRoomsUpdated pushes the room list to all connected UI clients (rooms are
// pool-visible). Guarded so the hub is usable without a manager (unit tests).
func (h *WatchRoomHub) broadcastRoomsUpdated() {
	if h.manager == nil || h.manager.wsEventManager == nil {
		return
	}
	h.manager.wsEventManager.SendEvent(events.NakamaRoomsUpdated, h.ListRooms())
}

// broadcastRoomState pushes one room's full state (incl. participant list) to that
// room's members only — the member list is visible only inside the room.
func (h *WatchRoomHub) broadcastRoomState(room *WatchRoom) {
	if h.manager == nil || h.manager.wsEventManager == nil {
		return
	}
	room.mu.RLock()
	clientIDs := make([]string, 0, len(room.Participants))
	for _, p := range room.Participants {
		if p.ClientID != "" {
			clientIDs = append(clientIDs, p.ClientID)
		}
	}
	room.mu.RUnlock()
	for _, cid := range clientIDs {
		h.manager.wsEventManager.SendEventTo(cid, events.NakamaWatchRoomState, room, true)
	}
}

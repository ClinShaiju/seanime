package debrid_client

import (
	"fmt"
	"seanime/internal/api/anilist"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/torrents/autoselect"
	"seanime/internal/util/filecache"
	"time"
)

// preferredSourceBucket holds each user's last-streamed source per entry, so later episodes keep
// to it (see autoselect.PreferredSource). Per user: the DB's batch history is shared by everyone.
var preferredSourceBucket = filecache.NewBucket("debrid-preferred-source", 180*24*time.Hour)

func preferredSourceKey(userID uint, mediaID int) string {
	return fmt.Sprintf("%d:%d", userID, mediaID)
}

// PreferredSource returns the user's remembered source for an entry, nil when none.
func (r *Repository) PreferredSource(userID uint, mediaID int) *autoselect.PreferredSource {
	if r.fileCacher == nil || mediaID == 0 {
		return nil
	}
	var p autoselect.PreferredSource
	if ok, err := r.fileCacher.Get(preferredSourceBucket, preferredSourceKey(userID, mediaID), &p); err != nil || !ok {
		return nil
	}
	return &p
}

// rememberSource records the release that just started streaming. An auto pick never replaces a
// manual one: the manual choice holds until the user picks manually again.
func (r *Repository) rememberSource(userID uint, mediaID int, t *hibiketorrent.AnimeTorrent, manual bool) {
	if r.fileCacher == nil || mediaID == 0 || t == nil {
		return
	}
	if !manual {
		if existing := r.PreferredSource(userID, mediaID); existing != nil && existing.Manual {
			return
		}
	}
	_ = r.fileCacher.Set(preferredSourceBucket, preferredSourceKey(userID, mediaID), autoselect.NewPreferredSource(t, manual))
}

// ForgetPreferredSource drops the remembered source for an entry.
func (r *Repository) ForgetPreferredSource(userID uint, mediaID int) {
	if r.fileCacher == nil {
		return
	}
	_ = r.fileCacher.Delete(preferredSourceBucket, preferredSourceKey(userID, mediaID))
}

// preferredSourceFor is the entry's own remembered source, else the one from a prequel cour of
// the same TV season: Dr. Stone New World Part 2 continues Part 1's S3 pack.
func (r *Repository) preferredSourceFor(userID uint, media *anilist.CompleteAnime) *autoselect.PreferredSource {
	if media == nil {
		return nil
	}
	if p := r.PreferredSource(userID, media.GetID()); p != nil {
		return p
	}
	if r.autoSelect == nil {
		return nil
	}
	season := r.autoSelect.ResolveTVEpisode(media.GetID(), 1).Season
	if season <= 0 {
		return nil
	}
	for _, edge := range media.GetRelations().GetEdges() {
		if edge == nil || edge.RelationType == nil || *edge.RelationType != anilist.MediaRelationPrequel || edge.Node == nil {
			continue
		}
		if r.autoSelect.ResolveTVEpisode(edge.Node.ID, 1).Season != season {
			continue
		}
		if p := r.PreferredSource(userID, edge.Node.ID); p != nil {
			return p
		}
	}
	return nil
}

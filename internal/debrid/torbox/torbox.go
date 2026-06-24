package torbox

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"seanime/internal/constants"
	"seanime/internal/debrid/debrid"
	"seanime/internal/util"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/mo"
)

type (
	TorBox struct {
		baseUrl string
		apiKey  mo.Option[string]
		client  *http.Client
		logger  *zerolog.Logger

		// dedup-path cache for getTorrents (AddTorrent only); short TTL so back-to-back plays
		// don't each re-fetch the whole mylist. ponytail: 6s TTL, no invalidation needed.
		mylistMu  sync.Mutex
		mylist    []*Torrent
		mylistAt  time.Time

		// fileIdCache maps "torrentID|shortName" -> numeric TorBox file id, so repeat resolves
		// of the same torrent+file (urlRefreshTTL refresh, replays, cross-consumer reuse) skip
		// the extra mylist round-trip in GetTorrentDownloadUrl. The first resolve still fetches.
		fileIdCache sync.Map
	}

	Response struct {
		Success bool        `json:"success"`
		Detail  string      `json:"detail"`
		Data    interface{} `json:"data"`
	}

	File struct {
		ID        int    `json:"id"`
		MD5       string `json:"md5"`
		S3Path    string `json:"s3_path"`
		Name      string `json:"name"`
		Size      int    `json:"size"`
		MimeType  string `json:"mimetype"`
		ShortName string `json:"short_name"`
	}

	Torrent struct {
		ID               int     `json:"id"`
		Hash             string  `json:"hash"`
		CreatedAt        string  `json:"created_at"`
		UpdatedAt        string  `json:"updated_at"`
		Magnet           string  `json:"magnet"`
		Size             int64   `json:"size"`
		Active           bool    `json:"active"`
		AuthID           string  `json:"auth_id"`
		DownloadState    string  `json:"download_state"`
		Seeds            int     `json:"seeds"`
		Peers            int     `json:"peers"`
		Ratio            float64 `json:"ratio"`
		Progress         float64 `json:"progress"`
		DownloadSpeed    float64 `json:"download_speed"`
		UploadSpeed      float64 `json:"upload_speed"`
		Name             string  `json:"name"`
		ETA              int64   `json:"eta"`
		Server           float64 `json:"server"`
		TorrentFile      bool    `json:"torrent_file"`
		ExpiresAt        string  `json:"expires_at"`
		DownloadPresent  bool    `json:"download_present"`
		DownloadFinished bool    `json:"download_finished"`
		Files            []*File `json:"files"`
		InactiveCheck    int     `json:"inactive_check"`
		Availability     float64 `json:"availability"`
	}

	TorrentInfo struct {
		Name  string             `json:"name"`
		Hash  string             `json:"hash"`
		Size  int64              `json:"size"`
		Files []*TorrentInfoFile `json:"files"`
	}

	TorrentInfoFile struct {
		Name string `json:"name"` // e.g. "Big Buck Bunny/Big Buck Bunny.mp4"
		Size int64  `json:"size"`
	}

	InstantAvailabilityItem struct {
		Name  string `json:"name"`
		Hash  string `json:"hash"`
		Size  int64  `json:"size"`
		Files []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
)

func NewTorBox(logger *zerolog.Logger) debrid.Provider {
	return &TorBox{
		baseUrl: "https://api.torbox.app/v1/api",
		apiKey:  mo.None[string](),
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		logger: logger,
	}
}

func (t *TorBox) GetSettings() debrid.Settings {
	return debrid.Settings{
		ID:   "torbox",
		Name: "TorBox",
	}
}

func (t *TorBox) doQuery(method, uri string, body io.Reader, contentType string) (*Response, error) {
	return t.doQueryCtx(context.Background(), method, uri, body, contentType)
}

const (
	torboxMaxRetries  = 4
	torboxBackoffBase = 1 * time.Second
	torboxBackoffCap  = 16 * time.Second
)

// torboxBackoff returns the exponential backoff for a retry attempt (1s, 2s, 4s, … capped).
// No jitter: a single-keyed client has no thundering herd to de-synchronize, and scheduled
// prewarms are serialized upstream, so there's no lockstep-retry case to spread.
func torboxBackoff(attempt int) time.Duration {
	d := torboxBackoffBase << attempt
	if d > torboxBackoffCap || d <= 0 {
		d = torboxBackoffCap
	}
	return d
}

// parseRetryAfter reads a Retry-After header (seconds form); falls back when absent/unparseable.
func parseRetryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return fallback
}

func (t *TorBox) doQueryCtx(ctx context.Context, method, uri string, body io.Reader, contentType string) (*Response, error) {
	apiKey, found := t.apiKey.Get()
	if !found {
		return nil, debrid.ErrNotAuthenticated
	}

	// Buffer the body so the request can be replayed on a 429 retry — a consumed io.Reader
	// can't be re-sent (createtorrent's multipart body would go out empty on the retry).
	var bodyBytes []byte
	if body != nil {
		var err error
		if bodyBytes, err = io.ReadAll(body); err != nil {
			return nil, err
		}
	}

	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if bodyBytes != nil {
			rdr = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, uri, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Add("Content-Type", contentType)
		req.Header.Add("Authorization", "Bearer "+apiKey)
		req.Header.Add("User-Agent", "Seanime/"+constants.Version)

		resp, err := t.client.Do(req)
		if err != nil {
			return nil, err
		}

		// Rate limited — honor Retry-After (else exponential backoff) and retry instead of
		// aborting the whole resolve, which is what a bare 429 used to do.
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt >= torboxMaxRetries {
				return nil, fmt.Errorf("torbox: rate limited (429) after %d retries", torboxMaxRetries)
			}
			wait := parseRetryAfter(resp, torboxBackoff(attempt))
			t.logger.Warn().Dur("wait", wait).Int("attempt", attempt+1).Msg("torbox: 429 rate limited, backing off")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		bodyB, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("request failed: code %d, body: %s", resp.StatusCode, string(bodyB))
		}
		if readErr != nil {
			t.logger.Error().Err(readErr).Msg("torbox: Failed to read response body")
			return nil, readErr
		}

		var ret Response
		if err := json.Unmarshal(bodyB, &ret); err != nil {
			trimmedBody := string(bodyB)
			if len(trimmedBody) > 2000 {
				trimmedBody = trimmedBody[:2000] + "..."
			}
			t.logger.Error().Err(err).Msg("torbox: Failed to decode response, response body: " + trimmedBody)
			return nil, err
		}

		if !ret.Success {
			return nil, fmt.Errorf("request failed: %s", ret.Detail)
		}

		return &ret, nil
	}
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func (t *TorBox) Authenticate(apiKey string) error {
	t.apiKey = mo.Some(apiKey)
	return nil
}

func (t *TorBox) GetInstantAvailability(hashes []string) map[string]debrid.TorrentItemInstantAvailability {

	t.logger.Trace().Strs("hashes", hashes).Msg("torbox: Checking instant availability")

	availability := make(map[string]debrid.TorrentItemInstantAvailability)

	if len(hashes) == 0 {
		return availability
	}

	var hashBatches [][]string

	for i := 0; i < len(hashes); i += 100 {
		end := i + 100
		if end > len(hashes) {
			end = len(hashes)
		}
		hashBatches = append(hashBatches, hashes[i:end])
	}

	for _, batch := range hashBatches {
		resp, err := t.doQuery("GET", t.baseUrl+fmt.Sprintf("/torrents/checkcached?hash=%s&format=list&list_files=true", strings.Join(batch, ",")), nil, "application/json")
		if err != nil {
			return availability
		}

		marshaledData, _ := json.Marshal(resp.Data)

		var items []*InstantAvailabilityItem
		err = json.Unmarshal(marshaledData, &items)
		if err != nil {
			return availability
		}

		for _, item := range items {
			availability[item.Hash] = debrid.TorrentItemInstantAvailability{
				CachedFiles: make(map[string]*debrid.CachedFile),
			}

			for idx, file := range item.Files {
				availability[item.Hash].CachedFiles[strconv.Itoa(idx)] = &debrid.CachedFile{
					Name: file.Name,
					Size: file.Size,
				}
			}
		}

	}

	return availability
}

func (t *TorBox) AddTorrent(opts debrid.AddTorrentOptions) (string, error) {

	// Check if the torrent is already added by checking existing torrents
	if opts.InfoHash != "" {
		// First check if it's already in our account using a more efficient approach
		torrents, err := t.getTorrentsCached()
		if err == nil {
			for _, torrent := range torrents {
				if strings.EqualFold(torrent.Hash, opts.InfoHash) {
					return strconv.Itoa(torrent.ID), nil
				}
			}
		}
		// Small delay to avoid rate limiting
		time.Sleep(500 * time.Millisecond)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	t.logger.Trace().Str("magnetLink", opts.MagnetLink).Msg("torbox: Adding torrent")

	err := writer.WriteField("magnet", opts.MagnetLink)
	if err != nil {
		return "", fmt.Errorf("torbox: Failed to add torrent: %w", err)
	}

	//err = writer.WriteField("seed", "1")
	//if err != nil {
	//	return "", fmt.Errorf("torbox: Failed to add torrent: %w", err)
	//}

	err = writer.Close()
	if err != nil {
		return "", fmt.Errorf("torbox: Failed to add torrent: %w", err)
	}

	resp, err := t.doQuery("POST", t.baseUrl+"/torrents/createtorrent", &body, writer.FormDataContentType())
	if err != nil {
		return "", fmt.Errorf("torbox: Failed to add torrent: %w", err)
	}

	type data struct {
		ID   int    `json:"torrent_id"`
		Name string `json:"name"`
		Hash string `json:"hash"`
	}

	marshaledData, _ := json.Marshal(resp.Data)

	var d data
	err = json.Unmarshal(marshaledData, &d)
	if err != nil {
		return "", fmt.Errorf("torbox: Failed to add torrent: %w", err)
	}

	t.logger.Debug().Str("torrentId", strconv.Itoa(d.ID)).Str("torrentName", d.Name).Str("torrentHash", d.Hash).Msg("torbox: Torrent added")

	// Keep the dedup cache consistent with what we just added so the long TTL doesn't re-POST
	// createtorrent for a torrent we know exists. ponytail: append-only; a full refresh still
	// happens when the TTL lapses.
	if d.Hash != "" {
		t.mylistMu.Lock()
		if t.mylist != nil {
			t.mylist = append(t.mylist, &Torrent{ID: d.ID, Hash: d.Hash})
		}
		t.mylistMu.Unlock()
	}

	return strconv.Itoa(d.ID), nil
}

// GetTorrentStreamUrl blocks until the torrent is downloaded and returns the stream URL for the torrent file by calling GetTorrentDownloadUrl.
func (t *TorBox) GetTorrentStreamUrl(ctx context.Context, opts debrid.StreamTorrentOptions, itemCh chan debrid.TorrentItem) (streamUrl string, err error) {

	t.logger.Trace().Str("torrentId", opts.ID).Str("fileId", opts.FileId).Msg("torbox: Retrieving stream link")

	doneCh := make(chan struct{})

	go func(ctx context.Context) {
		defer func() {
			close(doneCh)
		}()

		// ponytail: poll fast then back off (500ms→1s→2s→4s). Cached torrents report ready on the
		// first poll, so the old fixed 4s first-wait burned ~4s on the common case. Cap at 4s.
		delay := 500 * time.Millisecond
		for {
			select {
			case <-ctx.Done():
				err = ctx.Err()
				return
			case <-time.After(delay):
				torrent, _err := t.GetTorrent(opts.ID)
				if _err != nil {
					t.logger.Error().Err(_err).Msg("torbox: Failed to get torrent")
					err = fmt.Errorf("torbox: Failed to get torrent: %w", _err)
					return
				}

				itemCh <- *torrent

				// Check if the torrent is ready
				if torrent.IsReady {
					// ponytail: dropped the 1s settle sleep — DownloadPresent=true means the file is
					// available, so requestdl resolves immediately; the 429 backoff covers rate limits.
					downloadUrl, err := t.GetTorrentDownloadUrl(debrid.DownloadTorrentOptions{
						ID:     opts.ID,
						FileId: opts.FileId, // Filename
					})
					if err != nil {
						t.logger.Error().Err(err).Msg("torbox: Failed to get download URL")
						return
					}

					streamUrl = downloadUrl
					return
				}

				if delay < 4*time.Second {
					delay *= 2
				}
			}
		}
	}(ctx)

	<-doneCh

	return
}

func (t *TorBox) GetTorrentDownloadUrl(opts debrid.DownloadTorrentOptions) (downloadUrl string, err error) {

	t.logger.Trace().Str("torrentId", opts.ID).Msg("torbox: Retrieving download link")

	apiKey, found := t.apiKey.Get()
	if !found {
		return "", fmt.Errorf("torbox: Failed to get download URL: %w", debrid.ErrNotAuthenticated)
	}

	url := t.baseUrl + fmt.Sprintf("/torrents/requestdl?token=%s&torrent_id=%s&zip_link=true", apiKey, opts.ID)
	if opts.FileId != "" {
		// Map the short-name FileId -> numeric TorBox file id. Cache it so repeat resolves of the
		// same torrent+file (URL refresh, replays, cross-consumer reuse) skip this extra mylist call.
		cacheKey := opts.ID + "|" + opts.FileId
		var fId string
		if v, ok := t.fileIdCache.Load(cacheKey); ok {
			fId = v.(string)
		} else {
			torrent, err := t.getTorrent(opts.ID)
			if err != nil {
				return "", fmt.Errorf("torbox: Failed to get download URL: %w", err)
			}
			for _, f := range torrent.Files {
				if f.ShortName == opts.FileId {
					fId = strconv.Itoa(f.ID)
					break
				}
			}
			if fId == "" {
				return "", fmt.Errorf("torbox: Failed to get download URL, file not found")
			}
			t.fileIdCache.Store(cacheKey, fId)
		}
		url = t.baseUrl + fmt.Sprintf("/torrents/requestdl?token=%s&torrent_id=%s&file_id=%s", apiKey, opts.ID, fId)
	}

	resp, err := t.doQuery("GET", url, nil, "application/json")
	if err != nil {
		return "", fmt.Errorf("torbox: Failed to get download URL: %w", err)
	}

	marshaledData, _ := json.Marshal(resp.Data)

	var d string
	err = json.Unmarshal(marshaledData, &d)
	if err != nil {
		return "", fmt.Errorf("torbox: Failed to get download URL: %w", err)
	}

	t.logger.Debug().Str("downloadUrl", d).Msg("torbox: Download link retrieved")

	return d, nil
}

func (t *TorBox) GetTorrent(id string) (ret *debrid.TorrentItem, err error) {
	torrent, err := t.getTorrent(id)
	if err != nil {
		return nil, err
	}

	ret = toDebridTorrent(torrent)

	return ret, nil
}

func (t *TorBox) getTorrent(id string) (ret *Torrent, err error) {

	resp, err := t.doQuery("GET", t.baseUrl+fmt.Sprintf("/torrents/mylist?bypass_cache=true&id=%s", id), nil, "application/json")
	if err != nil {
		return nil, fmt.Errorf("torbox: Failed to get torrent: %w", err)
	}

	marshaledData, _ := json.Marshal(resp.Data)

	err = json.Unmarshal(marshaledData, &ret)
	if err != nil {
		return nil, fmt.Errorf("torbox: Failed to parse torrent: %w", err)
	}

	return ret, nil
}

// GetTorrentInfo uses the info hash to return the torrent's data.
// For cached torrents, it uses the /checkcached endpoint for faster response.
// For uncached torrents, it falls back to /torrentinfo endpoint.
func (t *TorBox) GetTorrentInfo(opts debrid.GetTorrentInfoOptions) (ret *debrid.TorrentInfo, err error) {

	if opts.InfoHash == "" {
		return nil, fmt.Errorf("torbox: No info hash provided")
	}

	resp, err := t.doQuery("GET", t.baseUrl+fmt.Sprintf("/torrents/checkcached?hash=%s&format=object&list_files=true", opts.InfoHash), nil, "application/json")
	if err != nil {
		return nil, fmt.Errorf("torbox: Failed to check cached torrent: %w", err)
	}

	// If the torrent is cached
	if resp.Data != nil {
		data, ok := resp.Data.(map[string]interface{})
		if ok {
			if torrentData, exists := data[opts.InfoHash]; exists {
				marshaledData, _ := json.Marshal(torrentData)

				var torrent TorrentInfo
				err = json.Unmarshal(marshaledData, &torrent)
				if err != nil {
					return nil, fmt.Errorf("torbox: Failed to parse cached torrent: %w", err)
				}

				ret = toDebridTorrentInfo(&torrent)
				return ret, nil
			}
		}
	}

	// If not cached, fall back
	resp, err = t.doQuery("GET", t.baseUrl+fmt.Sprintf("/torrents/torrentinfo?hash=%s&timeout=15", opts.InfoHash), nil, "application/json")
	if err != nil {
		return nil, fmt.Errorf("torbox: Failed to get torrent info: %w", err)
	}

	// DEVNOTE: Handle incorrect TorBox API response
	data, ok := resp.Data.(map[string]interface{})
	if ok {
		if _, ok := data["data"]; ok {
			if _, ok := data["data"].(map[string]interface{}); ok {
				data = data["data"].(map[string]interface{})
			} else {
				return nil, fmt.Errorf("torbox: Failed to parse response")
			}
		}
	}

	marshaledData, _ := json.Marshal(data)

	var torrent TorrentInfo
	err = json.Unmarshal(marshaledData, &torrent)
	if err != nil {
		return nil, fmt.Errorf("torbox: Failed to parse torrent: %w", err)
	}

	ret = toDebridTorrentInfo(&torrent)

	return ret, nil
}

func (t *TorBox) GetTorrents() (ret []*debrid.TorrentItem, err error) {

	torrents, err := t.getTorrents(true)
	if err != nil {
		return nil, fmt.Errorf("torbox: Failed to get torrents: %w", err)
	}

	// Limit the number of torrents to 500
	if len(torrents) > 500 {
		torrents = torrents[:500]
	}

	for _, t := range torrents {
		ret = append(ret, toDebridTorrent(t))
	}

	slices.SortFunc(ret, func(i, j *debrid.TorrentItem) int {
		return cmp.Compare(j.AddedAt, i.AddedAt)
	})

	return ret, nil
}

// mylistCacheTTL bounds how long the dedup cache is reused. The mylist (often multiple MB) is
// fetched only to answer "is this infohash already added?", and AddTorrent appends what it adds,
// so a long TTL is safe: the only staleness is a torrent added out-of-band (TorBox web, another
// process), which at worst causes one redundant createtorrent that returns the same id.
// Short TTLs made back-to-back / concurrent plays each re-fetch the whole list — the dominant
// "Adding torrent…" stall under multi-user load.
const mylistCacheTTL = 120 * time.Second

// getTorrentsCached is getTorrents with a TTL, used only by AddTorrent's dedup check.
// Staleness is harmless: TorBox dedups server-side, so a missed cache hit at worst re-POSTs
// a magnet that already exists (and gets the same id back).
func (t *TorBox) getTorrentsCached() ([]*Torrent, error) {
	t.mylistMu.Lock()
	if t.mylist != nil && time.Since(t.mylistAt) < mylistCacheTTL {
		// Copy-on-return: AddTorrent appends into t.mylist under the lock; handing out the
		// shared backing array would race a lock-free iterator if the append reuses spare cap.
		cached := slices.Clone(t.mylist)
		t.mylistMu.Unlock()
		return cached, nil
	}
	t.mylistMu.Unlock()

	// Fetch the mylist WITHOUT holding the lock. Previously the slow
	// /torrents/mylist fetch ran under mylistMu, so two users starting a debrid stream at once
	// serialized here (AddTorrent's dedup) — the 2nd waited for the 1st's full fetch. Releasing
	// the lock lets concurrent resolves run in parallel; the worst case is a couple of redundant
	// fetches, which is harmless. bypass_cache=false: the dedup check doesn't need a server-side
	// rebuild of the list (slow + spiky), TorBox's own cached mylist is fine and ~5x faster.
	torrents, err := t.getTorrents(false)
	if err != nil {
		return nil, err
	}

	t.mylistMu.Lock()
	t.mylist = torrents
	t.mylistAt = time.Now()
	cached := slices.Clone(t.mylist)
	t.mylistMu.Unlock()
	return cached, nil
}

func (t *TorBox) getTorrents(bypassCache bool) (ret []*Torrent, err error) {

	resp, err := t.doQuery("GET", t.baseUrl+fmt.Sprintf("/torrents/mylist?bypass_cache=%t", bypassCache), nil, "application/json")
	if err != nil {
		return nil, fmt.Errorf("torbox: Failed to get torrents: %w", err)
	}

	marshaledData, _ := json.Marshal(resp.Data)

	err = json.Unmarshal(marshaledData, &ret)
	if err != nil {
		t.logger.Error().Err(err).Msg("Failed to parse torrents")
		return nil, fmt.Errorf("torbox: Failed to parse torrents: %w", err)
	}

	return ret, nil
}

func toDebridTorrent(t *Torrent) (ret *debrid.TorrentItem) {

	addedAt, _ := time.Parse(time.RFC3339Nano, t.CreatedAt)

	completionPercentage := int(t.Progress * 100)

	ret = &debrid.TorrentItem{
		ID:                   strconv.Itoa(t.ID),
		Name:                 t.Name,
		Hash:                 t.Hash,
		Size:                 t.Size,
		FormattedSize:        util.Bytes(uint64(t.Size)),
		CompletionPercentage: completionPercentage,
		ETA:                  util.FormatETA(int(t.ETA)),
		Status:               toDebridTorrentStatus(t),
		AddedAt:              addedAt.Format(time.RFC3339),
		Speed:                util.ToHumanReadableSpeed(int(t.DownloadSpeed)),
		Seeders:              t.Seeds,
		IsReady:              t.DownloadPresent,
	}

	return
}

func toDebridTorrentInfo(t *TorrentInfo) (ret *debrid.TorrentInfo) {

	var files []*debrid.TorrentItemFile
	for idx, f := range t.Files {
		nameParts := strings.Split(f.Name, "/")
		var name string

		if len(nameParts) == 1 {
			name = nameParts[0]
		} else {
			name = nameParts[len(nameParts)-1]
		}

		files = append(files, &debrid.TorrentItemFile{
			ID:    name, // Set the ID to the og name so GetStreamUrl can use that to get the real file ID
			Index: idx,
			Name:  name,                       // e.g. "Big Buck Bunny.mp4"
			Path:  fmt.Sprintf("/%s", f.Name), // e.g. "/Big Buck Bunny/Big Buck Bunny.mp4"
			Size:  f.Size,
		})
	}

	ret = &debrid.TorrentInfo{
		Name:  t.Name,
		Hash:  t.Hash,
		Size:  t.Size,
		Files: files,
	}

	return
}

func toDebridTorrentStatus(t *Torrent) debrid.TorrentItemStatus {
	if t.DownloadFinished && t.DownloadPresent {
		switch t.DownloadState {
		case "uploading":
			return debrid.TorrentItemStatusSeeding
		default:
			return debrid.TorrentItemStatusCompleted
		}
	}

	switch t.DownloadState {
	case "downloading", "metaDL":
		return debrid.TorrentItemStatusDownloading
	case "stalled", "stalled (no seeds)":
		return debrid.TorrentItemStatusStalled
	case "completed", "cached":
		return debrid.TorrentItemStatusCompleted
	case "paused":
		return debrid.TorrentItemStatusPaused
	default:
		return debrid.TorrentItemStatusOther
	}
}

func (t *TorBox) DeleteTorrent(id string) error {

	type body = struct {
		ID        int    `json:"torrent_id"`
		Operation string `json:"operation"`
	}

	b := body{
		ID:        util.StringToIntMust(id),
		Operation: "delete",
	}

	marshaledData, _ := json.Marshal(b)

	_, err := t.doQuery("POST", t.baseUrl+fmt.Sprintf("/torrents/controltorrent"), bytes.NewReader(marshaledData), "application/json")
	if err != nil {
		return fmt.Errorf("torbox: Failed to delete torrent: %w", err)
	}

	return nil
}

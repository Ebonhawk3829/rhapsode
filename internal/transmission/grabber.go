// Package transmission is a torrent Grabber: it hands magnet links to the
// Transmission RPC and watches the transfers it started.
//
// Kishizu's transmission client proved the CSRF handshake and Add path; this
// package mirrors it and adds Status, because rhapsode's runner polls a
// grabbing job until it lands in staging — the part kishizu never needed
// (its done-remove.sh hook moves files out instead).
//
// On completion transmission's configured done-remove.sh removes the torrent
// while leaving the files, so Add needs no cleanup handshake here.
package transmission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/grab"
	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

// Grabber sends magnets to Transmission and reports their progress.
type Grabber struct {
	// dir is where torrents land, as Transmission sees the path. ABB
	// torrents keep their own landing inside the shared downloads tree so
	// no other consumer's scanner mistakes them.
	dir string
	// localDir is the same host directory as rhapsode sees it. The two
	// containers may mount the same tree at different names; Located
	// translates between them.
	localDir string
	url      string
	hc       *http.Client
	// session is cached after the first 409 round trip.
	session string
	// captured holds each torrent's file list and download directory, taken
	// from the RPC while the torrent still exists. Transmission's done hook
	// removes the torrent on completion, after which the RPC can no longer
	// answer where the files went — so the answer is recorded the first
	// time Status sees the metadata, and Located reads it back.
	captured map[grab.ID]torrentShape
}

// torrentShape is the location evidence captured from one torrent.
type torrentShape struct {
	downloadDir string
	// files are the torrent's member paths, relative to downloadDir.
	files []string
}

// New builds a Grabber. url is the RPC endpoint, e.g.
// http://100.64.0.1:9091/transmission/rpc; dir is Transmission's download
// directory for rhapsode torrents, as Transmission sees the path;
// localDir is the same host directory as rhapsode sees it.
func New(url, dir, localDir string) *Grabber {
	return &Grabber{
		url:      url,
		dir:      dir,
		localDir: localDir,
		hc:       &http.Client{Timeout: 30 * time.Second},
		captured: map[grab.ID]torrentShape{},
	}
}

// Name identifies the downloader.
func (g *Grabber) Name() string { return "transmission" }

// Kind is the transport this grabber serves.
func (g *Grabber) Kind() source.Kind { return source.Torrent }

// Add seeds one magnet and returns its Transmission id as the tracking ID.
//
// The magnet arrives as the handle's magnet string; a torrent is one unit —
// per-file selection does not apply.
func (g *Grabber) Add(ctx context.Context, h source.Handle) (grab.ID, error) {
	// Two handles use a magnet: the minting source wraps it in a Stringer
	// (abb's magnetHandle) and a rebuilt candidate may carry a bare string.
	magnet := ""
	switch ref := h.Ref.(type) {
	case string:
		magnet = ref
	case interface{ String() string }:
		magnet = ref.String()
	}
	if !strings.HasPrefix(magnet, "magnet:") {
		return "", fmt.Errorf("transmission: handle is not a magnet URI")
	}
	var out struct {
		TorrentAdded *struct {
			ID int `json:"id"`
		} `json:"torrent-added"`
		TorrentDuplicate *struct {
			ID int `json:"id"`
		} `json:"torrent-duplicate"`
	}
	if err := g.call(ctx, "torrent-add", map[string]any{
		"filename":     magnet,
		"download-dir": g.dir,
	}, &out); err != nil {
		return "", err
	}
	switch {
	case out.TorrentAdded != nil:
		return grab.ID(fmt.Sprintf("%d", out.TorrentAdded.ID)), nil
	case out.TorrentDuplicate != nil:
		// Someone already added this exact torrent: tracking its id is
		// better than failing — the bytes will land either way.
		return grab.ID(fmt.Sprintf("%d", out.TorrentDuplicate.ID)), nil
	default:
		return "", fmt.Errorf("transmission: torrent-add returned no torrent")
	}
}

// Status polls one torrent's progress by id.
//
// Transmission reports percentDone as 0..1 and leaves finished torrents
// seeding until the done-hook removes them, so Done is keyed on both: the
// torrent vanished (removed by the hook) counts as done, and a torrent still
// present at 100% is done but still "seeding" from Transmission's view.
//
// While the torrent exists, its file list and download directory are
// captured for Located: the done hook removes the torrent, and after that
// the RPC cannot say where the files went.
func (g *Grabber) Status(ctx context.Context, id grab.ID) (grab.State, error) {
	var out struct {
		Torrents []torrentRow `json:"torrents"`
	}
	if err := g.call(ctx, "torrent-get", map[string]any{
		"ids": []int{atoi(string(id))},
		"fields": []string{"id", "name", "percentDone", "status", "totalSize",
			"rateDownload", "eta", "error", "errorString", "downloadDir", "files"},
	}, &out); err != nil {
		return grab.State{}, err
	}
	// Not in the list: the done-hook removed it after a completed
	// download, or the torrent was cleared some other way. When the
	// shape was captured on an earlier poll the bytes landed and Done
	// is right. Without a capture there is no evidence either way —
	// report Unknown so the runner parks visibly instead of declaring
	// success and failing Located forever.
	if len(out.Torrents) == 0 {
		if _, ok := g.captured[id]; ok {
			return grab.State{Phase: grab.Done, Progress: 1}, nil
		}
		return grab.State{Phase: grab.Unknown, Message: "torrent absent and never captured"}, nil
	}
	t := out.Torrents[0]
	if t.Error != 0 {
		return grab.State{Phase: grab.Failed, Message: t.ErrorString}, nil
	}
	// Capture while the metadata is available. A magnet has no file list
	// until Transmission resolves it, so this fills in on the first poll
	// that carries one and is refreshed afterwards — cheap, and it keeps
	// Located working for the whole life of the job.
	if t.DownloadDir != "" && len(t.Files) > 0 {
		shape := torrentShape{downloadDir: t.DownloadDir}
		for _, f := range t.Files {
			shape.files = append(shape.files, f.Name)
		}
		g.captured[id] = shape
	}
	return grab.State{
		Phase:      phaseFor(t),
		Progress:   t.PercentDone,
		BytesDone:  int64(t.PercentDone * float64(t.TotalSize)),
		BytesTotal: t.TotalSize,
		Rate:       int64(t.RateDownload),
	}, nil
}

// Located reports the completed files' paths as rhapsode sees them.
//
// The evidence was captured during Status while the torrent still existed;
// once the done hook has removed the torrent the RPC cannot answer this, so
// a torrent that was never polled with metadata cannot be located.
func (g *Grabber) Located(_ context.Context, id grab.ID) ([]string, error) {
	shape, ok := g.captured[id]
	if !ok {
		return nil, fmt.Errorf("transmission: no file list captured for torrent %s", id)
	}
	var out []string
	for _, f := range shape.files {
		out = append(out, g.localPath(shape.downloadDir, f))
	}
	return out, nil
}

// Cancel removes a torrent and its partial files. An absent torrent is a
// no-op: the done hook or a previous cancel already took it.
func (g *Grabber) Cancel(ctx context.Context, id grab.ID) error {
	var out struct {
		Torrents []torrentRow `json:"torrents"`
	}
	if err := g.call(ctx, "torrent-get", map[string]any{
		"ids":    []int{atoi(string(id))},
		"fields": []string{"id"},
	}, &out); err != nil {
		return err
	}
	if len(out.Torrents) == 0 {
		return nil
	}
	return g.call(ctx, "torrent-remove", map[string]any{
		"ids":              []int{atoi(string(id))},
		"delete-local-data": true,
	}, nil)
}

// localPath translates a transmission-side path into the same file as
// rhapsode sees it.
//
// Transmission reports absolute paths under its own download directory,
// which is the configured dir; the prefix swap maps it onto rhapsode's view
// of the same host tree.
func (g *Grabber) localPath(downloadDir, file string) string {
	p := path.Join(downloadDir, file)
	if g.dir != "" && strings.HasPrefix(p, g.dir) {
		return path.Join(g.localDir, strings.TrimPrefix(p, g.dir))
	}
	return p
}

// torrentRow is one torrent as torrent-get returns it.
type torrentRow struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	PercentDone  float64 `json:"percentDone"`
	Status       int     `json:"status"`
	TotalSize    int64   `json:"totalSize"`
	RateDownload int     `json:"rateDownload"`
	ETA          int64   `json:"eta"`
	Error        int     `json:"error"`
	ErrorString  string  `json:"errorString"`
	DownloadDir  string  `json:"downloadDir"`
	Files        []struct {
		Name string `json:"name"`
	} `json:"files"`
}

// phaseFor maps Transmission's status int onto the runner's phases.
//
// 0=stopped 1=check-wait 2=check 3=download-wait 4=download 5=seed-wait
// 6=seed. Seeding after 100% still means the bytes are here: Done.
func phaseFor(t torrentRow) grab.Phase {
	switch t.Status {
	case 1, 2:
		return grab.Active
	case 3:
		return grab.Queued
	case 4:
		if t.PercentDone >= 1 {
			return grab.Done
		}
		return grab.Active
	case 5, 6, 0:
		if t.PercentDone >= 1 {
			return grab.Done
		}
		// Stopped before finishing is stalled, not done — a user paused it
		// or Transmission throttled it.
		return grab.Stalled
	default:
		return grab.Unknown
	}
}

// call performs one RPC request, handling the CSRF session token. Same
// handshake kishizu's client proved: 409 carries the fresh token, retry once.
func (g *Grabber) call(ctx context.Context, method string, args any, out any) error {
	body, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if g.session != "" {
			req.Header.Set("X-Transmission-Session-Id", g.session)
		}
		resp, err := g.hc.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusConflict {
			g.session = resp.Header.Get("X-Transmission-Session-Id")
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
			resp.Body.Close()
			return fmt.Errorf("%s: %d %s", method, resp.StatusCode, strings.TrimSpace(string(b)))
		}
		defer resp.Body.Close()
		var envelope struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return err
		}
		if envelope.Result != "success" {
			return fmt.Errorf("%s: %s", method, envelope.Result)
		}
		if out != nil {
			return json.Unmarshal(envelope.Arguments, out)
		}
		return nil
	}
	return fmt.Errorf("%s: could not obtain a session token", method)
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

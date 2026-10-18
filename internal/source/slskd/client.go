// Package slskd implements source.Source and grab.Grabber against the slskd
// REST API.
//
// slskd is both a source and a grabber: it searches the Soulseek network for
// copies, and it is the thing that transfers them. The two roles are separate
// types here because the interfaces are separate — a Source finds and fetches
// a handle, a Grabber tracks bytes — even though one client serves both.
package slskd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one slskd instance over HTTP.
type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

// New builds a client for the slskd instance at base.
func New(base, apiKey string) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is a non-2xx response from slskd.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("slskd: HTTP %d: %s", e.Status, e.Body)
}

// do performs a request and decodes the response into out.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("slskd: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{Status: resp.StatusCode, Body: string(b)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// ---- Search ----

type searchRequest struct {
	SearchText    string `json:"searchText"`
	SearchTimeout int    `json:"searchTimeout"`
}

type searchState struct {
	ID            string `json:"id"`
	SearchText    string `json:"searchText"`
	State         string `json:"state"`
	IsComplete    bool   `json:"isComplete"`
	ResponseCount int    `json:"responseCount"`
	FileCount     int    `json:"fileCount"`
}

// startSearch begins a search and returns its ID.
//
// slskd <0.26 returns the bare id string; 0.26+ returns the full search
// object. Both shapes are accepted so the client works across versions.
func (c *Client) startSearch(ctx context.Context, text string, timeoutMS int) (string, error) {
	body, err := json.Marshal(searchRequest{SearchText: text, SearchTimeout: timeoutMS})
	if err != nil {
		return "", fmt.Errorf("marshal search: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v0/searches", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build search request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("slskd: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", &APIError{Status: resp.StatusCode, Body: string(b)}
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read search response: %w", err)
	}

	var id string
	if err := json.Unmarshal(raw, &id); err == nil && id != "" {
		return id, nil
	}
	var obj searchState
	if err := json.Unmarshal(raw, &obj); err == nil && obj.ID != "" {
		return obj.ID, nil
	}
	return "", fmt.Errorf("slskd: unexpected search response: %.200s", raw)
}

func (c *Client) searchState(ctx context.Context, id string) (*searchState, error) {
	var st searchState
	if err := c.do(ctx, http.MethodGet, "/api/v0/searches/"+url.PathEscape(id), nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) deleteSearch(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v0/searches/"+url.PathEscape(id), nil, nil)
}

// file is one file in a peer's search response.
type file struct {
	Filename  string   `json:"filename"`
	Size      int64    `json:"size"`
	BitRate   *int     `json:"bitRate"`
	Length    *float64 `json:"length"`
	Extension string   `json:"extension"`
	IsLocked  bool     `json:"isLocked"`
}

// userResponse is one peer's response to a search.
type userResponse struct {
	Username          string `json:"username"`
	HasFreeUploadSlot bool   `json:"hasFreeUploadSlot"`
	UploadSpeed       int64  `json:"uploadSpeed"`
	QueueLength       int    `json:"queueLength"`
	Files             []file `json:"files"`
	LockedFiles       []file `json:"lockedFiles"`
}

func (c *Client) searchResponses(ctx context.Context, id string) ([]userResponse, error) {
	var rs []userResponse
	if err := c.do(ctx, http.MethodGet, "/api/v0/searches/"+url.PathEscape(id)+"/responses", nil, &rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// ---- Enqueue ----

// batchRequest is the body of POST /api/v0/transfers/downloads/batches.
//
// The batch endpoint is used rather than the per-peer endpoint because only
// the batch endpoint accepts Options.Destination, which is what keeps
// audiobook downloads out of the music pipeline's complete/ directory.
type batchRequest struct {
	ID       string       `json:"id,omitempty"`
	SearchID string       `json:"searchId,omitempty"`
	Username string       `json:"username"`
	Files    []batchFile  `json:"files"`
	Options  batchOptions `json:"options"`
}

type batchFile struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type batchOptions struct {
	Destination string `json:"destination"`
}

// batchResponse is the reply from the batch endpoint.
type batchResponse struct {
	Batch struct {
		ID string `json:"id"`
	} `json:"batch"`
	Failures []struct {
		Filename string `json:"filename"`
		Message  string `json:"message"`
	} `json:"failures"`
}

// enqueueBatch enqueues files from one peer into a named destination.
func (c *Client) enqueueBatch(ctx context.Context, username, destination string, files []batchFile) (*batchResponse, error) {
	req := batchRequest{
		Username: username,
		Files:    files,
		Options:  batchOptions{Destination: destination},
	}

	var out batchResponse
	if err := c.do(ctx, http.MethodPost, "/api/v0/transfers/downloads/batches", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- Transfers ----

// flexTime tolerates slskd's inconsistent timestamps: some carry a Z suffix,
// some are bare "2006-01-02T15:04:05.999999999".
type flexTime struct{ time.Time }

func (t *flexTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return nil
}

// transfer is one file transfer record.
type transfer struct {
	ID               string    `json:"id"`
	Filename         string    `json:"filename"`
	Size             int64     `json:"size"`
	BytesTransferred int64     `json:"bytesTransferred"`
	PercentComplete  float64   `json:"percentComplete"`
	AverageSpeed     float64   `json:"averageSpeed"`
	State            string    `json:"state"`
	PlaceInQueue     *int      `json:"placeInQueue"`
	RequestedAt      *flexTime `json:"requestedAt"`
	StartedAt        *flexTime `json:"startedAt"`
}

// downloads returns transfers grouped by peer.
//
// slskd 0.26+ returns a flat array of users with nested directories; older
// versions return a per-user tree. Both are accepted.
func (c *Client) downloads(ctx context.Context) (map[string][]transfer, error) {
	var flat []struct {
		Username    string `json:"username"`
		Directories []struct {
			Directory string     `json:"directory"`
			Files     []transfer `json:"files"`
		} `json:"directories"`
	}
	flatErr := c.do(ctx, http.MethodGet, "/api/v0/transfers/downloads", nil, &flat)
	if flatErr == nil {
		out := make(map[string][]transfer, len(flat))
		for _, u := range flat {
			var ts []transfer
			for _, d := range u.Directories {
				ts = append(ts, d.Files...)
			}
			out[u.Username] = ts
		}
		return out, nil
	}

	var tree map[string]struct {
		Directories []struct {
			Transfers []transfer `json:"transfers"`
		} `json:"directories"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v0/transfers/downloads", nil, &tree); err != nil {
		return nil, fmt.Errorf("transfers: %v / %v", flatErr, err)
	}
	out := make(map[string][]transfer, len(tree))
	for user, rec := range tree {
		var ts []transfer
		for _, d := range rec.Directories {
			ts = append(ts, d.Transfers...)
		}
		out[user] = ts
	}
	return out, nil
}

// batch is the reply of GET /api/v0/transfers/downloads/batches/{id}.
//
// The batch is the unit rhapsode enqueues, so it is also the unit verify
// locates: its transfers carry the per-file states, and its options carry
// the destination the files were enqueued under — the one authoritative
// record of where slskd was told to put them.
type batch struct {
	ID        string      `json:"id"`
	Username  string      `json:"username"`
	Transfers []transfer  `json:"transfers"`
	Options   batchOption `json:"options"`
}

type batchOption struct {
	Destination string `json:"destination"`
}

// BatchByID fetches one batch with its transfers. Exported because the
// verify phase resolves a job's staged files through the batch record; the
// grabber is the caller in-tree, and a smoke probe outside the tree is the
// other.
func (c *Client) BatchByID(ctx context.Context, id string) (*batch, error) {
	return c.batchByID(ctx, id)
}

// batchByID fetches one batch with its transfers.
//
// Completed transfers stay queryable for slskd's retention window, so a
// batch remains locatable long after the transfer left the live queue —
// which is what lets verify run after the runner's poll loop has already
// seen Done.
func (c *Client) batchByID(ctx context.Context, id string) (*batch, error) {
	var b batch
	if err := c.do(ctx, http.MethodGet,
		"/api/v0/transfers/downloads/batches/"+url.PathEscape(id), nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// cancelTransfer removes a transfer.
func (c *Client) cancelTransfer(ctx context.Context, username, transferID string, remove bool) error {
	path := fmt.Sprintf("/api/v0/transfers/downloads/%s/%s?remove=%t",
		url.PathEscape(username), url.PathEscape(transferID), remove)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

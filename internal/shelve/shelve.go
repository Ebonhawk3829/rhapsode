// Package shelve lands the tagged .m4b in the library and asks
// Audiobookshelf to scan it in.
//
// The path rules are the library contract (docs/library-format.md):
//
//	<Author>/<Series>/<Book Title>/<Book Title>.m4b   (series book)
//	<Author>/<Book Title>/<Book Title>.m4b            (standalone)
//
// One .m4b per book, no series-position numbers in folder names, no colons,
// no '#' and no "[Dramatized Adaptation]" qualifier anywhere in a path. The
// copy goes to a temporary name first and is renamed into place, so a
// half-copied file can never be scanned by ABS as a broken book.
package shelve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// scanTimeout bounds the ABS scan call.
const scanTimeout = 30 * time.Second

// Shelver copies finished books into the library and triggers the ABS scan.
type Shelver struct {
	// library is the library root, as rhapsode sees it.
	library string
	// absURL and absToken authenticate the Audiobookshelf scan call.
	absURL   string
	absToken string
	// libraryID is the ABS library to scan.
	libraryID string
	hc        *http.Client
}

// New builds a Shelver.
func New(library, absURL, absToken, libraryID string) *Shelver {
	return &Shelver{
		library:   library,
		absURL:    strings.TrimRight(absURL, "/"),
		absToken:  absToken,
		libraryID: libraryID,
		hc:        &http.Client{Timeout: scanTimeout},
	}
}

// Shelve copies the finished file into its library path and triggers the ABS
// scan. It returns the library path the book landed at.
func (s *Shelver) Shelve(ctx context.Context, src string, m Tagged) (string, error) {
	destDir, fileName, err := libraryTarget(s.library, m)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("shelve: mkdir: %w", err)
	}

	dest := path.Join(destDir, fileName+".m4b")
	tmp := dest + ".part"

	if err := copyFile(src, tmp); err != nil {
		return "", fmt.Errorf("shelve: copy: %w", err)
	}
	// The rename is the atomic gate: ABS never sees a partial book under
	// its final name.
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("shelve: rename: %w", err)
	}

	if err := s.scan(ctx); err != nil {
		// The book is in the library; a failed scan call is reported but
		// does not undo the copy — ABS picks the file up on its next scan.
		return dest, fmt.Errorf("shelve: scan: %w", err)
	}
	return dest, nil
}

// Tagged is the metadata the library path is derived from. It mirrors the
// tag package's contract fields (the tagged file is the source of truth).
type Tagged struct {
	Title          string
	Author         string
	Series         string
	SeriesSequence string
}

// LibraryPath derives the contract path for one book.
//
// Series books nest under the series folder; standalones do not (no
// "Stand-Alone Title" placeholder). The sequence lives in metadata, never in
// a path, so no position number appears here.
func LibraryPath(library string, m Tagged) (string, error) {
	dir, _, err := libraryTarget(library, m)
	return dir, err
}

// libraryTarget derives the contract directory and file stem for one book.
// Both go through cleanSegment: the filename must satisfy the same
// no-colon/no-hash/no-separator contract as the directories.
func libraryTarget(library string, m Tagged) (dir, stem string, err error) {
	author := cleanSegment(m.Author)
	title := cleanSegment(m.Title)
	if author == "" || title == "" {
		return "", "", fmt.Errorf("shelve: author and title are required for the library path")
	}
	if m.Series != "" {
		series := cleanSegment(m.Series)
		if series != "" {
			return path.Join(library, author, series, title), title, nil
		}
	}
	return path.Join(library, author, title), title, nil
}

// cleanSegment makes one path segment library-contract safe: no colons, no
// '#', no bracketed "[Dramatized Adaptation]" qualifiers, no path
// separators. Apostrophes stay (the contract keeps them: "The Emperor's
// Soul").
func cleanSegment(s string) string {
	s = strings.TrimSpace(s)
	// Bracketed qualifiers are edition decoration, never path material.
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.Index(s[i:], "]"); j >= 0 {
			s = s[:i] + s[i+j+1:]
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/', r == '\\', r == ':', r == '#':
			// separators and the forbidden characters — dropped
		case r < 32:
			// control characters — dropped
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// scan asks ABS to rescan the library.
func (s *Shelver) scan(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/libraries/%s/scan", s.absURL, s.libraryID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.absToken)
	req.Header.Set("Content-Length", "0")

	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("ABS scan %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// copyFile copies one file to a temporary path (the caller renames).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// AlreadyOwned asks ABS whether the library already carries the title.
//
// The check is a title search inside the target library — the cheap guard
// against double-shelving a book that is already in. A failed call returns
// an error, never a silent "not owned": an unreachable ABS must not cause a
// duplicate shelf.
func (s *Shelver) AlreadyOwned(ctx context.Context, title string) (bool, error) {
	url := fmt.Sprintf("%s/api/libraries/%s/search?q=%s", s.absURL, s.libraryID, urlQueryEscape(title))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+s.absToken)

	resp, err := s.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return false, fmt.Errorf("ABS search %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out struct {
		Book []struct {
			LibraryItem struct {
				Media struct {
					Metadata struct {
						Title string `json:"title"`
					} `json:"metadata"`
				} `json:"media"`
			} `json:"libraryItem"`
		} `json:"book"`
	}
	if err := json.NewDecoder(bytes.NewReader(readAll(resp))).Decode(&out); err != nil {
		return false, fmt.Errorf("decode ABS search: %w", err)
	}
	want := ownershipKey(title)
	for _, hit := range out.Book {
		if ownershipKey(hit.LibraryItem.Media.Metadata.Title) == want {
			return true, nil
		}
	}
	return false, nil
}

// ownershipKey is the identity a title is compared under: contract-cleaned,
// case-folded and stripped of a leading article — one catalogue says "Black
// Prism", the library says "The Black Prism", and they are the same book
// (the same fold the catalog merge uses).
func ownershipKey(title string) string {
	key := cleanSegment(title)
	for _, a := range []string{"The ", "A ", "An "} {
		if strings.HasPrefix(key, a) {
			key = key[len(a):]
			break
		}
	}
	return strings.ToLower(key)
}

func readAll(resp *http.Response) []byte {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return b
}

// urlQueryEscape percent-encodes a query value.
func urlQueryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Package tag writes the library metadata contract onto the merged .m4b.
//
// The contract (docs/library-format.md) is what Audiobookshelf reads and
// what the library layout is derived from: clean title (no series prefix, no
// bracketed qualifiers), bare series name with the sequence in its own
// field, "First Last" author, narratorName "GraphicAudio" for GA titles,
// bare-YYYY published year, and a description.
//
// tone is the writer: it rewrites mp4 atoms in place without re-muxing, and
// it targets local filesystems — the caller must pass a staging path, never
// the network library mount.
package tag

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// tagTimeout bounds one tone tag run.
const tagTimeout = 10 * time.Minute

// Tagger drives tone.
type Tagger struct {
	// run executes one tone invocation. Injected for tests; production
	// wires execRunner.
	run func(ctx context.Context, args []string) error
}

// New builds a Tagger.
func New() *Tagger {
	return &Tagger{run: execRunner}
}

// SetRunner replaces the tone runner (tests script invocations).
func (t *Tagger) SetRunner(run func(ctx context.Context, args []string) error) {
	t.run = run
}

// Metadata is the library contract for one book.
type Metadata struct {
	Title          string // clean title, no series prefix, no [qualifiers]
	Author         string // "First Last"
	Series         string // bare series name, empty for a standalone
	SeriesSequence string // position within Series
	Narrator       string // "GraphicAudio" for GA titles
	Publisher      string
	PublishedYear  string // bare YYYY
	Description    string
	Genre          string
}

// Tag writes the metadata onto the file at path.
//
// The mapping follows the tags the running library carries (probed with tone
// dump on the live collection): album/albumArtist carry title/author for the
// audiobook players, narrator and publisher are tone's dedicated audiobook
// atoms, and the series rides movement/movementName with the sequence in
// part — the shape ABS reads for series grouping.
func (t *Tagger) Tag(ctx context.Context, path string, m Metadata) error {
	if m.Title == "" || m.Author == "" {
		return fmt.Errorf("tag: title and author are required")
	}

	args := []string{"tag", path, "--assume-yes",
		"--meta-title", m.Title,
		"--meta-album", m.Title,
		"--meta-album-artist", m.Author,
		"--meta-artist", m.Author,
	}
	if m.Narrator != "" {
		args = append(args, "--meta-narrator", m.Narrator)
	}
	if m.Publisher != "" {
		args = append(args, "--meta-publisher", m.Publisher)
	}
	if m.Series != "" {
		args = append(args, "--meta-movement-name", m.Series)
		if m.SeriesSequence != "" {
			args = append(args, "--meta-part", m.SeriesSequence)
		}
	}
	if m.PublishedYear != "" {
		args = append(args, "--meta-publishing-date", m.PublishedYear+"-01-01")
	}
	if m.Description != "" {
		args = append(args, "--meta-description", m.Description)
	}
	if m.Genre != "" {
		args = append(args, "--meta-genre", m.Genre)
	}

	if err := t.run(ctx, args); err != nil {
		return fmt.Errorf("tag: tone: %w", err)
	}
	return nil
}

// execRunner runs tone with the given arguments.
func execRunner(ctx context.Context, args []string) error {
	runCtx, cancel := context.WithTimeout(ctx, tagTimeout)
	defer cancel()

	out, err := exec.CommandContext(runCtx, "tone", args...).CombinedOutput()
	if err != nil {
		tail := ""
		if len(out) > 0 {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if n := len(lines); n > 3 {
				lines = lines[n-3:]
			}
			tail = ": " + strings.Join(lines, " | ")
		}
		return fmt.Errorf("%v%s", err, tail)
	}
	return nil
}

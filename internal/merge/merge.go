// Package merge combines a verified staged batch into the one .m4b the
// library contract requires.
//
// The contract (docs/library-format.md) is one .m4b per book: a multi-part
// dramatisation or a multi-file folder rip is merged into a single file with
// chapters. A single-file offer skips the merge entirely — remuxing one file
// through ffmpeg would spend an hour of I/O to produce the bytes it already
// has.
//
// Merging runs on local staging (paths.staging), never on the network
// library mount: m4b-tool's chapter detection reads every input byte, and
// the B2 rclone mount turns that into a multi-hour network crawl. The
// shelving step copies the finished file to the library afterwards.
package merge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

// mergeTimeout bounds one m4b-tool run. A full remux of a long book takes
// minutes; a hung ffmpeg must not park the runner's sweep forever.
const mergeTimeout = 2 * time.Hour

// Merger drives m4b-tool.
type Merger struct {
	// staging is the local scratch directory the merged file is written
	// to, as rhapsode sees it.
	staging string
	// run executes one m4b-tool invocation. Injected for tests; production
	// wires execRunner.
	run func(ctx context.Context, args []string) error
}

// New builds a Merger writing into the staging directory.
func New(staging string) *Merger {
	return &Merger{staging: staging, run: execRunner}
}

// SetRunner replaces the m4b-tool runner (tests script invocations).
func (m *Merger) SetRunner(run func(ctx context.Context, args []string) error) {
	m.run = run
}

// Result reports what the merge step produced.
type Result struct {
	// Path is the merged (or passed-through) file, as rhapsode sees it.
	Path string
	// Merged is true when m4b-tool ran; false when the single input was
	// passed through untouched.
	Merged bool
}

// Merge produces the single .m4b for the batch.
//
// One input file is passed through as-is (the library contract wants one
// .m4b, and it already is one). Several inputs are merged with
// --no-conversion: the sources are already the audio the user confirmed, and
// re-encoding would spend an hour degrading it. The output is named
// <slug>-<title-slug>.m4b: the title keeps an `ls` readable, the job slug
// keeps two jobs for the same title from racing on one path.
func (m *Merger) Merge(ctx context.Context, slug, title string, inputs []string) (Result, error) {
	if title == "" {
		return Result{}, fmt.Errorf("merge: empty title")
	}
	if len(inputs) == 0 {
		return Result{}, fmt.Errorf("merge: no input files")
	}

	out := m.outputPath(slug, title)
	if err := os.MkdirAll(m.staging, 0o755); err != nil {
		return Result{}, fmt.Errorf("merge: staging: %w", err)
	}

	if len(inputs) == 1 {
		// Pass-through: the file is already the single .m4b the contract
		// wants. Copy rather than reference — the staging copy is what the
		// tag driver mutates, and the staged original stays intact for a
		// re-verify.
		if err := copyFile(inputs[0], out); err != nil {
			return Result{}, fmt.Errorf("merge: copy: %w", err)
		}
		return Result{Path: out, Merged: false}, nil
	}

	args := []string{"merge"}
	args = append(args, inputs...)
	args = append(args,
		"--output-file", out,
		"--no-conversion",
		"--no-cache",
	)
	if err := m.run(ctx, args); err != nil {
		return Result{}, fmt.Errorf("merge: m4b-tool: %w", err)
	}
	if _, err := os.Stat(out); err != nil {
		return Result{}, fmt.Errorf("merge: output missing after run: %w", err)
	}
	return Result{Path: out, Merged: true}, nil
}

// outputPath is the merged file's staging path. Path-safe characters only
// — the shelving step re-derives the library path from the tagged metadata,
// so this name is scratch.
func (m *Merger) outputPath(slug, title string) string {
	name := slugify(title)
	if slug != "" {
		name = slugify(slug) + "-" + name
	}
	return path.Join(m.staging, name+".m4b")
}

// execRunner runs m4b-tool with the given arguments.
func execRunner(ctx context.Context, args []string) error {
	runCtx, cancel := context.WithTimeout(ctx, mergeTimeout)
	defer cancel()

	out, err := exec.CommandContext(runCtx, "m4b-tool", args...).CombinedOutput()
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

// copyFile copies one file, preserving mode.
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

// slugify reduces a title to a filesystem-safe token.
func slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	lastDash := true // leading dashes are suppressed
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

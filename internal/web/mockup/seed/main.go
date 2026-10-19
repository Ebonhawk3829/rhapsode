// Mockup seed: creates fixture jobs and candidates in a scratch store,
// served by the real binary for visual review.
//
// Usage:
// 1. go run ./internal/web/mockup/seed  # prints the data dir
// 2. go build -o /tmp/kilo/mockui ./cmd/rhapsode
// 3. /tmp/kilo/mockui -config <datadir>/rhapsode.yaml
//
// The scratch config wires no providers or sources (degraded banner shows),
// so every page renders from fixtures only.
package main

import (
	"fmt"
	"os"

	"github.com/Ebonhawk3829/rhapsode/internal/store"
)

func main() {
	dir, err := os.MkdirTemp("", "mockui")
	if err != nil {
		panic(err)
	}
	st, err := store.Open(dir + "/rhapsode.db")
	if err != nil {
		panic(err)
	}

	mk := func(j *store.Job) string {
		if err := st.Create(j); err != nil {
			panic(err)
		}
		return j.ID
	}
	cand := func(c *store.Candidate) {
		if err := st.SaveCandidate(c); err != nil {
			panic(err)
		}
	}

	// Done job.
	mk(&store.Job{Title: "Isles of the Emberdark", Author: "Brandon Sanderson", Series: "Secret Projects", Narrator: "Kaleo Griffith", Slug: "emberdark-1", State: store.Done, Progress: 1, Source: "slskd"})
	// Downloading job.
	mk(&store.Job{Title: "The Black Prism", Author: "Brent Weeks", Slug: "prism-1", State: store.Downloading, Progress: 0.42, Source: "slskd", Grabber: "slskd", GrabID: "batch-1"})
	// Confirm job with one offer per band and per source.
	cj := mk(&store.Job{Title: "Wind and Truth", Author: "Brandon Sanderson", Series: "Stormlight Archive", SeriesSequence: "5", Narrator: "Michael Kramer", Slug: "wat-1", State: store.Confirm})
	_ = cj
	// Tag-review job.
	mk(&store.Job{Title: "Isles of the Emberdark", Author: "Brandon Sanderson", Series: "Secret Projects", SeriesSequence: "5", Narrator: "Kaleo Griffith, Jennifer Jill Araya", Publisher: "Dragonsteel, LLC", PublishedYear: "2025", Genre: "Fantasy", Description: "All his life, Sixth of the Dusk has been a traditional trapper of Aviar — the supernatural birds his people bond with — on the deadly island of Patji.", Slug: "emberdark-2", State: store.TagReview, Phase: "/staging/emberdark.m4b"})
	// Failed job.
	mk(&store.Job{Title: "Red Rising", Author: "Pierce Brown", Slug: "rr-1", State: store.Failed, Error: "no candidates from any source", Source: "slskd"})

	// Re-fetch the confirm job ID for candidates.
	jobs, _ := st.List("")
	var confirmID string
	for _, j := range jobs {
		if j.State == store.Confirm {
			confirmID = j.ID
		}
	}
	cand(&store.Candidate{JobID: confirmID, Source: "slskd", Kind: "p2p", Username: "ringadingding", Title: "share/Wind and Truth", RemoteDir: "share", Format: "m4b", Bitrate: 128, SizeBytes: 966195891, FreeSlot: true, Score: 0.95, Band: "accept", Files: []store.CandidateFile{{Name: "share/Wind and Truth/Part 1.m4b", Size: 500000000}, {Name: "share/Wind and Truth/Part 2.m4b", Size: 466195891}}, Reasons: []string{"✓ title in share", "✓ author in share", "✓ m4b format", "✓ audio files in share", "✓ live peer offering now"}})
	cand(&store.Candidate{JobID: confirmID, Source: "slskd", Kind: "p2p", Username: "calibrefan", Title: "ebooks/wind-and-truth", RemoteDir: "ebooks", Format: "epub", SizeBytes: 2000000, Score: 0.4, Band: "reject", Files: []store.CandidateFile{{Name: "ebooks/wind-and-truth.epub", Size: 2000000}}, Reasons: []string{"✓ title in share", "✗ unknown format", "✗ no audio files in share (companions only?)"}})
	cand(&store.Candidate{JobID: confirmID, Source: "audiobookbay", Kind: "torrent", Title: "Wind and Truth - Brandon Sanderson", RemoteDir: "/abss/wind-and-truth/", ExternalURL: "https://audiobookbay.lu/abss/wind-and-truth/", Format: "m4b", Bitrate: 128, SizeBytes: 950000000, Score: 0.7, Band: "review", Files: []store.CandidateFile{{Name: "/abss/wind-and-truth/"}}, Reasons: []string{"✓ title in share", "✓ author in share", "· posted 6 Jul 2025", "· torrent: availability unknown until grabbed"}})

	cfg := fmt.Sprintf(`server:
  listen: 127.0.0.1:18999
  data_dir: %s
paths:
  downloads: %s/dl
  staging: %s/st
  library: %s/lib
library:
  url: http://127.0.0.1:1
  token: x
  library_id: x
providers:
  graphicaudio:
    url: ""
  audnexus:
    url: ""
  audible:
    base: ""
sources:
  audiobookbay:
    url: ""
    flaresolverr: ""
  slskd:
    url: ""
    api_key: ""
grabbers:
  transmission:
    url: ""
    dir: /x
    local_dir: /x
notify:
  ntfy:
    url: ""
    topic: ""
`, dir, dir, dir, dir)
	if err := os.WriteFile(dir+"/rhapsode.yaml", []byte(cfg), 0o644); err != nil {
		panic(err)
	}
	for _, d := range []string{"dl", "st", "lib"} {
		if err := os.MkdirAll(dir+"/"+d, 0o755); err != nil {
			panic(err)
		}
	}
	if err := st.Close(); err != nil {
		panic(err)
	}
	fmt.Println(dir)
}

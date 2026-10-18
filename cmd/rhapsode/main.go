// Command rhapsode is an audiobook request platform.
//
// It searches a request across metadata catalogues, finds a copy in the
// configured sources, hands it to a downloader, then verifies, merges, tags
// and shelves the result into the library.
//
// Run with -serve for the web UI. The remaining flags are one-shot commands
// that act on the database and exit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/catalog"
	"github.com/Ebonhawk3829/rhapsode/internal/config"
	"github.com/Ebonhawk3829/rhapsode/internal/grab"
	"github.com/Ebonhawk3829/rhapsode/internal/job"
	"github.com/Ebonhawk3829/rhapsode/internal/merge"
	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
	audible "github.com/Ebonhawk3829/rhapsode/internal/metadata/audible"
	audnexus "github.com/Ebonhawk3829/rhapsode/internal/metadata/audnexus"
	graphicaudio "github.com/Ebonhawk3829/rhapsode/internal/metadata/graphicaudio"
	"github.com/Ebonhawk3829/rhapsode/internal/notify"
	"github.com/Ebonhawk3829/rhapsode/internal/shelve"
	"github.com/Ebonhawk3829/rhapsode/internal/source"
	abb "github.com/Ebonhawk3829/rhapsode/internal/source/abb"
	slskd "github.com/Ebonhawk3829/rhapsode/internal/source/slskd"
	"github.com/Ebonhawk3829/rhapsode/internal/store"
	"github.com/Ebonhawk3829/rhapsode/internal/tag"
	"github.com/Ebonhawk3829/rhapsode/internal/transmission"
	"github.com/Ebonhawk3829/rhapsode/internal/version"
	"github.com/Ebonhawk3829/rhapsode/internal/web"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	var (
		configPath  = flag.String("config", "/data/rhapsode.yaml", "path to the configuration file")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("rhapsode %s (%s)\n", version.String(), version.GoVersion())
		return
	}

	if err := run(*configPath); err != nil {
		log.Fatalf("rhapsode: %v", err)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.Server.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.Open(filepath.Join(cfg.Server.DataDir, "rhapsode.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	// Audnexus enriches the scaffolds the Audible search produces: the
	// scraper discovers, the catalogue declares. The join lives in catalog.
	nexus := audnexus.New(cfg.Providers.Audnexus.URL, cfg.Providers.Audnexus.Region)

	providers := []metadata.Provider{
		graphicaudio.New(cfg.Providers.GraphicAudio.URL),
		audible.New(cfg.Providers.Audible.Base, cfg.Providers.Audible.FlareSolverr),
	}
	var cat = catalog.New(providers...).WithEnricher(nexus)

	// Acquisition: slskd is both a source and a grabber, served by one
	// client. ABB is the torrent source, served by the Transmission
	// grabber. More of each hang off the same two maps.
	var (
		sources  []source.Source
		grabbers = map[source.Kind]grab.Grabber{}
	)
	if cfg.Sources.Slskd.APIKey != "" {
		client := slskd.New(cfg.Sources.Slskd.URL, cfg.Sources.Slskd.APIKey)
		sources = append(sources, slskd.NewSource(client))
		// slskd's download root is where its destinations resolve from. Both
		// containers mount the same host downloads tree at the same /downloads
		// prefix (the standardized layout), so one derived name serves both
		// views; the grabber still translates through root/localRoot rather
		// than assuming equality.
		root := slskdDownloadsRoot(cfg)
		grabbers[source.P2P] = slskd.NewGrabber(client, root, root)
	}
	if cfg.Sources.AudioBookBay.FlareSolverr != "" && cfg.Grabbers.Transmission.URL != "" {
		abbClient := abb.New(
			abbDefault(cfg.Sources.AudioBookBay.URL),
			abb.NewFlareSolverr(cfg.Sources.AudioBookBay.FlareSolverr),
		)
		if len(cfg.Sources.AudioBookBay.Mirrors) > 0 {
			abbClient.Mirrors = cfg.Sources.AudioBookBay.Mirrors
		}
		sources = append(sources, abb.NewSource(abbClient))
		grabbers[source.Torrent] = transmission.New(
			cfg.Grabbers.Transmission.URL,
			cfg.Grabbers.Transmission.Dir,
			cfg.Grabbers.Transmission.LocalDir,
		)
	}

	runner := job.New(st, sources, grabbers)

	// Post-download tooling: merge on local staging, tag with tone, shelve
	// into the library and scan ABS. All four live in the same image as the
	// binary (the m4b-tool base), so no external wiring is needed.
	runner.SetMerger(merge.New(cfg.Paths.Staging))
	runner.SetTagger(tag.New())
	shelver := shelve.New(
		cfg.Paths.Library,
		cfg.Library.URL,
		cfg.Library.Token,
		cfg.Library.LibraryID,
	)
	runner.SetShelver(shelver)
	// The shelver also answers the duplicate guard: the search step asks
	// ABS whether the title is already shelved before asking any source.
	runner.SetOwnedChecker(shelver)
	if cfg.Notify.Ntfy.URL != "" && cfg.Notify.Ntfy.Topic != "" {
		runner.SetNotifier(notify.New(
			cfg.Notify.Ntfy.URL,
			cfg.Notify.Ntfy.Topic,
			cfg.Notify.Ntfy.Token,
		))
	}

	srv := web.New(cfg, st, cat, runner)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The runner lives and dies with the process: jobs are durable in the
	// store, so a restart just resumes sweeping where the last one left off.
	go runner.Run(ctx)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", cfg.Server.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// abbDefault picks the ABB base URL, defaulting to the domain the user finds
// more reliable than the mirrors.
func abbDefault(u string) string {
	if u == "" {
		return "audiobookbay.lu"
	}
	return u
}

// slsdDownloadsRoot is slskd's download root as slskd sees it.
//
// slskd resolves every batch destination against its own configured root;
// rhapsode's mount of the same host tree is named differently, and the
// grabber translates between the two. The slskd-side name is derived from
// rhapsode's view rather than configured separately: both containers mount
// the same downloads directory, so the only difference is the mount point,
// and one source of truth for the tree keeps them from drifting.
func slskdDownloadsRoot(cfg *config.File) string {
	return path.Join(path.Dir(cfg.Paths.Downloads), "slskd-complete")
}

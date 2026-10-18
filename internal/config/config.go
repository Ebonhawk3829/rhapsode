// Package config loads rhapsode's configuration file.
//
// One file holds the whole deployment: where files live, which catalogues to
// ask, which sources to search, and which downloaders to drive. They are
// together because a deployment is one thing — the paths, the catalogues and
// the clients belong to the same setup, and splitting them means several files
// to mount and several to get out of sync.
//
// Every field has a zero value that means "not configured", and Load fills in
// the defaults. That is what lets a minimal file work: the user writes what
// they care about and nothing else.
package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// File is the whole configuration file.
type File struct {
	Server    Server    `yaml:"server"`
	Paths     Paths     `yaml:"paths"`
	Library   Library   `yaml:"library"`
	Providers Providers `yaml:"providers"`
	Sources   Sources   `yaml:"sources"`
	Grabbers  Grabbers  `yaml:"grabbers"`
	Notify    Notify    `yaml:"notify"`
}

// Server is the HTTP listener.
type Server struct {
	// Listen is the address the UI and API bind to. There is no
	// authentication, so binding a wider interface exposes full control of
	// the tool.
	Listen string `yaml:"listen"`
	// DataDir is where the SQLite database lives.
	DataDir string `yaml:"data_dir"`
}

// Paths are the filesystem locations rhapsode reads and writes.
type Paths struct {
	// Downloads is where the downloader writes completed files, as rhapsode
	// sees it. Each job stages under a slug subdirectory here.
	Downloads string `yaml:"downloads"`
	// Staging is local scratch space for merging and tagging. It must be on
	// local block storage: tone targets local filesystems, not network ones.
	Staging string `yaml:"staging"`
	// Library is the finished library, as rhapsode sees it.
	Library string `yaml:"library"`
}

// Library is how rhapsode talks to Audiobookshelf.
type Library struct {
	// URL is the ABS base URL.
	URL string `yaml:"url"`
	// Token is an ABS API key. It is a secret and belongs in SOPS, never in
	// the repository.
	Token string `yaml:"token"`
	// LibraryID is the ABS library to check for already-owned titles and to
	// scan after shelving.
	LibraryID string `yaml:"library_id"`
}

// Providers are the metadata catalogues.
type Providers struct {
	// GraphicAudio is the self-hosted GA scraper.
	GraphicAudio GraphicAudio `yaml:"graphicaudio"`
	// Audnexus is the public Audible metadata API.
	Audnexus Audnexus `yaml:"audnexus"`
	// Audible is the Audible storefront search, scraped.
	Audible Audible `yaml:"audible"`
}

// GraphicAudio is the self-hosted GA scraper.
type GraphicAudio struct {
	// URL is the scraper's base URL.
	URL string `yaml:"url"`
	// Enabled turns the provider off without removing its configuration.
	Enabled *bool `yaml:"enabled"`
}

// Audnexus is the public Audible metadata API.
type Audnexus struct {
	// URL is the API base URL.
	URL string `yaml:"url"`
	// Region selects the catalogue region.
	Region string `yaml:"region"`
	// Enabled turns the provider off without removing its configuration.
	Enabled *bool `yaml:"enabled"`
}

// Audible is the Audible storefront search. It is the only title→ASIN
// discovery surface in the stack: Audnexus is lookup-only, so without this
// the omnisearch finds only what the GA scraper indexes.
type Audible struct {
	// Base is the storefront to search. Different regions order differently
	// and hold different catalogues; the enricher's region should match it.
	Base string `yaml:"base"`
	// FlareSolverr is the challenge-solving proxy the scrape is routed
	// through. Audible geo-redirects and throttles datacentre IPs; from
	// this deployment the proxy is not optional. Empty means fetch direct,
	// which only makes sense from a residential IP.
	FlareSolverr string `yaml:"flaresolverr"`
	// Enabled turns the provider off without removing its configuration.
	Enabled *bool `yaml:"enabled"`
}

// Sources are the places copies are found.
type Sources struct {
	// AudioBookBay is the torrent indexer.
	AudioBookBay AudioBookBay `yaml:"audiobookbay"`
	// Slskd is the Soulseek client.
	Slskd Slskd `yaml:"slskd"`
}

// AudioBookBay is the torrent indexer.
type AudioBookBay struct {
	// URL is the site's base URL. Mirrors are rotated on failure, not per
	// request.
	URL string `yaml:"url"`
	// Mirrors are alternate base URLs to fall back to.
	Mirrors []string `yaml:"mirrors"`
	// FlareSolverr is the challenge-solving proxy, already running for
	// Prowlarr.
	FlareSolverr string `yaml:"flaresolverr"`
	// Enabled turns the source off without removing its configuration.
	Enabled *bool `yaml:"enabled"`
}

// Slskd is the Soulseek client, used both as a source and as a grabber.
type Slskd struct {
	// URL is the slskd API base URL.
	URL string `yaml:"url"`
	// APIKey authenticates to slskd. A secret; keep it in SOPS.
	APIKey string `yaml:"api_key"`
	// Enabled turns the source off without removing its configuration.
	Enabled *bool `yaml:"enabled"`
}

// Grabbers are the downloaders.
type Grabbers struct {
	// Transmission is the torrent client.
	Transmission Transmission `yaml:"transmission"`
}

// Transmission is the torrent client.
type Transmission struct {
	// URL is the RPC endpoint.
	URL string `yaml:"url"`
	// Username and Password authenticate to the RPC endpoint.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Dir is where rhapsode's torrents land, as Transmission sees the path.
	// Inside the shared downloads tree so no other consumer's scanner
	// mistakes them for its own.
	Dir string `yaml:"dir"`
	// LocalDir is the same host directory as rhapsode sees it. The two
	// containers may mount the same tree at different names; verification
	// reads the completed files through rhapsode's view, so the mapping
	// between the two is configuration, not guesswork.
	LocalDir string `yaml:"local_dir"`
	// Enabled turns the grabber off without removing its configuration.
	Enabled *bool `yaml:"enabled"`
}

// Notify is where failures are reported.
type Notify struct {
	// Ntfy is the push endpoint. Only failures and a single confirmation are
	// sent; progress lives in the UI.
	Ntfy Ntfy `yaml:"ntfy"`
}

// Ntfy is the push endpoint.
type Ntfy struct {
	// URL is the ntfy server base URL.
	URL string `yaml:"url"`
	// Topic is the topic to publish to.
	Topic string `yaml:"topic"`
	// Token authenticates to ntfy, when the topic is protected.
	Token string `yaml:"token"`
	// Enabled turns notifications off without removing their configuration.
	Enabled *bool `yaml:"enabled"`
}

// Load reads the configuration file at path, applies defaults, and validates
// the result.
//
// Unknown keys are an error rather than a silent no-op: a misspelled key that
// is quietly ignored is a setting the user believes is in force and is not.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	f.applyDefaults()
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// applyDefaults fills in the values that have a sensible default. Anything
// deployment-specific — ports, credentials, hostnames — is left empty so that
// validate can insist on it rather than guessing.
func (f *File) applyDefaults() {
	if f.Server.Listen == "" {
		f.Server.Listen = "127.0.0.1:8099"
	}
	if f.Server.DataDir == "" {
		f.Server.DataDir = "/data"
	}
	if f.Paths.Downloads == "" {
		f.Paths.Downloads = "/downloads"
	}
	if f.Paths.Staging == "" {
		f.Paths.Staging = "/staging"
	}
	if f.Paths.Library == "" {
		f.Paths.Library = "/library"
	}
	if f.Providers.GraphicAudio.URL == "" {
		f.Providers.GraphicAudio.URL = "http://100.64.0.1:8092"
	}
	if f.Providers.Audnexus.URL == "" {
		f.Providers.Audnexus.URL = "https://api.audnex.us"
	}
	if f.Providers.Audnexus.Region == "" {
		f.Providers.Audnexus.Region = "us"
	}
	// The storefront must match where the server's IP actually lands:
	// audible.com geo-redirects a datacentre IP to its regional storefront,
	// and following that redirect means searching the home page, not the
	// search results. This deployment lands on .com.au.
	if f.Providers.Audible.Base == "" {
		f.Providers.Audible.Base = "https://www.audible.com.au"
	}
}

// validate rejects a configuration that cannot work.
//
// It checks only what would fail at runtime anyway, and it checks it at load
// so the failure names the file rather than surfacing hours later as a
// mysterious empty result.
func (f *File) validate() error {
	if f.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if f.Server.DataDir == "" {
		return fmt.Errorf("server.data_dir is required")
	}
	if f.Paths.Downloads == "" || f.Paths.Staging == "" || f.Paths.Library == "" {
		return fmt.Errorf("paths.downloads, paths.staging and paths.library are required")
	}
	if f.Paths.Staging == f.Paths.Library {
		return fmt.Errorf("paths.staging and paths.library must differ: staging is scratch, library is permanent")
	}
	if f.Library.URL == "" {
		return fmt.Errorf("library.url is required")
	}
	if f.Library.Token == "" {
		return fmt.Errorf("library.token is required")
	}
	if f.Library.LibraryID == "" {
		return fmt.Errorf("library.library_id is required")
	}
	if enabled(f.Providers.GraphicAudio.Enabled, f.Providers.GraphicAudio.URL != "") && f.Providers.GraphicAudio.URL == "" {
		return fmt.Errorf("providers.graphicaudio.url is required when the provider is enabled")
	}
	if enabled(f.Providers.Audnexus.Enabled, f.Providers.Audnexus.URL != "") && f.Providers.Audnexus.URL == "" {
		return fmt.Errorf("providers.audnexus.url is required when the provider is enabled")
	}
	if enabled(f.Sources.Slskd.Enabled, f.Sources.Slskd.URL != "") && f.Sources.Slskd.URL == "" {
		return fmt.Errorf("sources.slskd.url is required when the source is enabled")
	}
	if enabled(f.Sources.AudioBookBay.Enabled, f.Sources.AudioBookBay.URL != "") && f.Sources.AudioBookBay.URL == "" {
		return fmt.Errorf("sources.audiobookbay.url is required when the source is enabled")
	}
	if enabled(f.Grabbers.Transmission.Enabled, f.Grabbers.Transmission.URL != "") && f.Grabbers.Transmission.URL == "" {
		return fmt.Errorf("grabbers.transmission.url is required when the grabber is enabled")
	}
	if enabled(f.Grabbers.Transmission.Enabled, f.Grabbers.Transmission.URL != "") && f.Grabbers.Transmission.LocalDir == "" {
		return fmt.Errorf("grabbers.transmission.local_dir is required when the grabber is enabled: verification reads the files through rhapsode's own mount")
	}
	if enabled(f.Notify.Ntfy.Enabled, f.Notify.Ntfy.Topic != "") && f.Notify.Ntfy.Topic == "" {
		return fmt.Errorf("notify.ntfy.topic is required when notifications are enabled")
	}
	return nil
}

// enabled reports whether a component is switched on.
//
// An explicit flag always wins. Without one, a component is on when it is
// configured: a URL the user has filled in is a component they want used, and
// leaving it blank is how they say they do not. That is what lets a minimal
// config work — it names only what it uses, and the rest stays off.
func enabled(flag *bool, configured bool) bool {
	if flag != nil {
		return *flag
	}
	return configured
}

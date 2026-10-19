<div align="center">

<img src="img/rhapsode-icon-dark-512.png" width="110" alt="rhapsode logo">

# rhapsode

An audiobook request platform. Search once across catalogues, pick the edition, pick the copy — verified, merged, tagged, and shelved into your library.

[![Release](https://img.shields.io/github/v/release/Ebonhawk3829/rhapsode?style=flat-square)](https://github.com/Ebonhawk3829/rhapsode/releases)
[![Docker Image](https://img.shields.io/badge/ghcr.io-ebonhawk3829%2Frhapsode-blue?style=flat-square&logo=docker)](https://github.com/Ebonhawk3829/rhapsode/pkgs/container/rhapsode)
[![License](https://img.shields.io/badge/license-MIT-green?style=flat-square)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/Ebonhawk3829/rhapsode/ci.yml?label=CI&style=flat-square)](https://github.com/Ebonhawk3829/rhapsode/actions)

</div>

---

rhapsode takes an audiobook request from search to shelf: catalogue metadata
confirms the edition, sources offer copies ranked by match evidence, a
downloader fetches your pick, and the file is verified against the release,
merged to a single m4b, tagged with your review, and shelved into
Audiobookshelf.

| Stage | What happens |
|---|---|
| **Search** | Omnisearch across GraphicAudio, Audible, and Audnexus by author, series, title, ISBN, or ASIN |
| **Confirm** | Pick the edition, then pick the copy from ranked slskd and AudioBookBay offers |
| **Grab** | Hands the selection to slskd or Transmission and tracks it to completion |
| **Verify** | Dumps the staged files' tags with tone and checks them against the release |
| **Merge** | Combines parts into one m4b with m4b-tool (no re-encode) |
| **Review** | Proposes tags from the catalogue record; you edit and confirm before anything is written |
| **Shelve** | Copies into the `Author/Series/Title` library layout and triggers an ABS scan |
| **Clean** | Removes the download directory on success, failure, or cancel, plus a startup sweep for crash debris |

## Contents

- [Status](#status)
- [Requirements](#requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [How matching works](#how-matching-works)
- [Job states](#job-states)
- [Flags](#flags)
- [API](#api)
- [Security](#security)
- [License](#license)

## Status

rhapsode handles audiobook requests for one person.

Releases follow semver. Patch releases are safe to take unread. Minor releases
may add configuration keys; existing ones keep working. A major release may
change behaviour, and the release notes will say so.

What it does not do:

- **Serve multiple users.** No auth, no permissions model. See [Security](#security).
- **Compare sources against each other.** slskd and AudioBookBay rank on different evidence; scores compare within a source, and the UI sections them separately.
- **Write tags without review.** The tag step always parks for your edit and confirm.

## Requirements

- A Soulseek client (**slskd**) and/or a torrent client (**Transmission**).
- A self-hosted **GraphicAudio scraper** for dramatisation metadata (optional but first in query order).
- **FlareSolverr** for the Audible scrape and AudioBookBay (both bot-wall datacentre IPs).
- **Audiobookshelf** for duplicate detection and the post-shelve scan.
- `m4b-tool` and `tone` — provided by the Docker image.
- Go 1.27+ if building from source. Docker needs nothing else.
- Optionally, **ntfy** for failure and shelved alerts.

## Installation

### Docker Compose

```yaml
services:
  rhapsode:
    image: ghcr.io/ebonhawk3829/rhapsode:latest
    user: "1000:1000"             # your media user's uid:gid
    volumes:
      - ./config:/data            # database and config file
      - ./downloads:/downloads    # shared downloads tree (same host dir the downloaders write)
      - ./staging:/staging        # local scratch for merging (must be local block storage)
      - ./library:/library        # finished library (ABS reads this too)
    ports:
      - "127.0.0.1:8099:8099"
    restart: unless-stopped
    command:
      - "-config"
      - "/data/rhapsode.yaml"
```

Then open **http://localhost:8099**, search, and request. Copy
`rhapsode.yaml.example` to `./config/rhapsode.yaml` and fill in the service
URLs and keys first — blank details mean that piece is off, and the UI says
so with a degraded banner rather than failing silently.

Three things about that compose file:

**Mount the same downloads tree the downloaders write.** slskd resolves
destinations under its own root (`/downloads/slskd-complete` in slskd.yml);
rhapsode reads the same tree through `/downloads`. Both must see the same
host directory or verification cannot find completed files.

**Keep staging on local block storage.** `tone` targets local filesystems,
not network mounts.

**Bind loopback and reverse-proxy.** There is no authentication; expose the
port through Caddy or a tailnet, never directly.

## Configuration

Copy `rhapsode.yaml.example` to `rhapsode.yaml`. Every value shown is the
default; a minimal file names only what it uses. Presence is the switch: a
component with its connection details filled in is on, blank them to turn it
off. There are no `enabled` flags.

The region chain matters: the Audible storefront base must be the one your
server's IP geo-lands on (`.com.au` from a datacentre box — a `.com` base
geo-redirects to the home page and the parser finds nothing), and the
Audnexus region must agree with it (ASINs are per-region; an AU ASIN looked
up in the US catalogue 404s).

## How matching works

Pre-download, the scorer grades offers on peer-typed text (slskd) or catalogue
rows (ABB): title, author, narrator, series, part structure, format, and
audio-file presence. Companion-only shares (covers, epubs) are flagged, not
hidden. Scores compare within a source — the confirm screen sections offers
by source so a torrent and a live peer are never fused into one number.

Post-download, the verifier dumps the staged files' own tags with tone and
checks them against the release: title containment (subtitles pass),
per-voice narrator overlap (co-narration passes), abridgement markers, and
soft misses for tags folder rips commonly omit. A contradiction parks the job
back in confirm; only matches merge, tag, and shelve.

## Job states

`pending → searching → confirm → downloading → verifying → merging →
tag-review → tagging → shelving → done`, with `failed` and `cancelled` as
terminal exits. The UI renders friendly labels (`Pick a Copy`, `Review Tags`)
rather than the internal names. The jobs list hides shelved jobs; failed and
cancelled rows clear with a button.

## Flags

| Flag | Description |
|---|---|
| `-config` | Path to the configuration file (default `/data/rhapsode.yaml`, or `$RHAPSODE_CONFIG`) |
| `-version` | Print the version and exit |

## API

The handlers speak JSON alongside the HTML: `GET /api/search?q=`,
`POST /api/jobs`, `GET /api/jobs`, `GET /api/jobs/{id}`,
`GET /api/jobs/{id}/candidates`, `POST /api/jobs/{id}/confirm`,
`POST /api/jobs/{id}/reject`, `POST /api/jobs/{id}/tags`,
`POST /api/jobs/{id}/cancel`, `DELETE /api/jobs/{id}`, `GET /api/version`.

## Security

There is no authentication on the web UI or API. Anyone who can reach the
port has full control: search, download, tag, shelve, cancel, and clear. Bind
`127.0.0.1` and reverse-proxy over a tailnet; never expose the port to a
network you do not trust. Secrets (API keys, tokens) live in the config file
— keep it in SOPS or equivalent, never in the repository.

## License

MIT. See [LICENSE](LICENSE).

# rhapsode

An audiobook request platform. Search a request, find a copy across sources,
acquire it, verify it, merge it, tag it correctly, and land it in the library —
from one UI instead of a chain of manual scripts.

A *rhapsode* is a professional reciter of epic poetry: spoken stories,
aggregated.

## Status

End-to-end MVP. The full pipeline is implemented and unit-tested:

- **Catalogue** — GraphicAudio scraper + Audible storefront search, enriched
  by Audnexus; the omnisearch merges releases into works.
- **Sources** — slskd (Soulseek shares) and AudioBookBay (torrents via
  FlareSolverr), each with a live-probed client and fixture-pinned parsers.
- **Downloaders** — slskd batch transfers and Transmission RPC, routed by
  transport kind.
- **Pipeline** — search → confirm (user picks the offer and files) →
  download → verify (tone tag inspection against the release; a failed
  verify reopens the confirm screen with the candidate rejected) → merge
  (`m4b-tool merge --no-conversion` on local staging) → tag (`tone tag`,
  the library contract) → shelve (`<Author>/<Series>/<Title>/<Title>.m4b`
  with a `.part` rename, then an ABS library scan) → done.
- **UI** — no-JavaScript HTML: omnisearch, releases panel, job queue, per-job
  confirm screen with per-file selection and scoring evidence.

Deployment: a container in the `media-stack` compose stack, built on the
`sandreas/m4b-tool` image so the merge/tag tooling ships with the app.
Configuration is one YAML file (`rhapsode.yaml.example` documents every
field); secrets stay in SOPS.

Design notes live in `docs/`.


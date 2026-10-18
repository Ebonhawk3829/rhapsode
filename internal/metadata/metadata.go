// Package metadata defines the catalogue side of rhapsode: the interface every
// metadata provider implements, and the types that cross it.
//
// A provider answers two questions. Search is the fuzzy entry point behind the
// omnisearch box, where the user types a title and wants candidates back.
// Lookup is the exact entry point, where an identifier is already known and the
// job is to enrich a release with the fields the library contract requires.
//
// The two are separate because they fail differently. A search that returns
// nothing is a normal outcome the UI renders as "no match". A lookup that
// returns nothing means the identifier is wrong, and the caller has to decide
// whether to retry as a different identifier type or drop it.
package metadata

import "context"

// Provider is one catalogue rhapsode can ask about books.
//
// Implementations must be safe for concurrent use: the omnisearch fans out to
// every provider at once, and a single provider may be asked several questions
// while an earlier one is still in flight.
type Provider interface {
	// Name identifies the provider in logs, in the UI, and in the source
	// field of a Book. It is stable and lowercase, e.g. "graphicaudio".
	Name() string

	// Search returns candidate works matching a free-text query, best match
	// first. An empty result is not an error: it means the catalogue has
	// nothing for that query, which is a normal outcome.
	//
	// Implementations must not return an error for a query that simply
	// matches nothing. Reserve errors for transport and parse failures.
	Search(ctx context.Context, query string) ([]*Book, error)

	// Lookup resolves one identifier to a single work.
	//
	// The identifier may be an ISBN or an ASIN, and shape alone cannot tell
	// them apart — a numeric identifier is ambiguous. Implementations should
	// attempt the lookup and let the catalogue decide, returning ErrNotFound
	// when the identifier is not theirs.
	Lookup(ctx context.Context, id Identifier) (*Book, error)
}

// Identifier is a catalogue key for a work or release.
//
// Kind is carried explicitly rather than inferred from the value because a
// bare number can be either an ISBN-10 or a numeric ASIN, and guessing wrong
// sends the lookup to the wrong catalogue.
type Identifier struct {
	Kind  IdentifierKind
	Value string
}

// IdentifierKind is which namespace an Identifier's value belongs to.
type IdentifierKind string

const (
	// ISBN identifies a published edition. GraphicAudio releases carry one.
	ISBN IdentifierKind = "isbn"
	// ASIN identifies an Audible edition. Audnexus is keyed by it.
	ASIN IdentifierKind = "asin"
)

// Book is a work as a catalogue describes it.
//
// A Book is the work, not a specific release: it is what the omnisearch shows
// and what the releases panel expands. The releases themselves are fetched
// separately, because one work has many and they come from different
// catalogues.
type Book struct {
	// Title is the clean title, with no series prefix and no bracketed
	// qualifiers. It is written to the library as-is.
	Title string
	// Author is "First Last".
	Author string
	// Series is the bare series name, with no sequence number. Empty for a
	// standalone work.
	Series string
	// SeriesSequence is the position within Series, as a string because
	// catalogues express it inconsistently ("1", "1.5", "2, Kylar
	// Chronicles"). Empty when the work has no position.
	SeriesSequence string
	// Narrator is the credited narrator, or the literal "GraphicAudio" for a
	// full-cast dramatisation.
	Narrator string
	// Publisher is the publishing house.
	Publisher string
	// PublishedYear is a bare YYYY. Empty when the catalogue does not say.
	PublishedYear string
	// Description is the publisher's blurb.
	Description string
	// Genres are flattened genre names. Catalogues that return genre objects
	// must flatten them before they reach this field.
	Genres []string
	// Identifiers are the catalogue keys this work is known by. A work may
	// carry both an ISBN and an ASIN.
	Identifiers []Identifier
	// Source is the Name of the provider that produced this Book.
	Source string
}

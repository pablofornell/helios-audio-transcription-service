package domain

// ListFilter and Page live in domain rather than in the package that
// consumes them for a mechanical reason worth stating: Go interface
// satisfaction requires exact type identity, and the persistence adapter
// deliberately does not import internal/app (see internal/app/ports.go).
// If each layer declared its own structurally-identical ListFilter, the
// adapter could never satisfy app.Repository, and every call site would need
// a pointless conversion. Declaring them once here -- in the package both
// layers already depend on -- is what lets the arrows keep pointing one way.
//
// internal/app and internal/postgres both alias these types, so app.Page,
// postgres.Page and domain.Page are the same type, not three lookalikes.

// ListFilter narrows and pages a listing of transcriptions. The zero value
// is a valid request for the first page of everything.
type ListFilter struct {
	// Status, when non-empty, restricts the listing to one status.
	Status Status
	// Cursor is an opaque keyset cursor returned by a previous listing.
	// Empty means "start at the newest". It is deliberately opaque -- it
	// encodes (created_at, id) -- and callers must not construct one.
	Cursor string
	// Limit is the maximum number of items to return. Zero or negative
	// means the default; anything above the maximum is clamped rather than
	// rejected, since a too-eager limit is not worth failing a read over.
	Limit int
}

// Page is one page of a listing.
type Page struct {
	Items []*Transcription `json:"items"`
	// NextCursor is empty when the listing is exhausted. A non-empty cursor
	// does not promise the next page is non-empty -- only that the caller
	// has not provably reached the end yet.
	NextCursor string `json:"nextCursor,omitempty"`
}

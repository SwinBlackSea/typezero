package httpapi

import "errors"

// errSessionMissing is returned when a session id has no state on the server.
// It maps to HTTP 400 in the handler.
var errSessionMissing = errors.New("session missing")

// errSessionTotalMismatch is returned when the chunk_total field of a later
// chunk disagrees with the first chunk that established the session.
var errSessionTotalMismatch = errors.New("chunk_total mismatch")

// errSessionIndexOutOfRange is returned when chunk_index is negative or not
// less than chunk_total.
var errSessionIndexOutOfRange = errors.New("chunk_index out of range")

// errSessionInconsistentTotal is returned when a non-final chunk arrives after
// the session already saw a final chunk, indicating the client misordered
// requests.
var errSessionInconsistentTotal = errors.New("session already finalized")
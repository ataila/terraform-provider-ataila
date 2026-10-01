// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Response headers the contract declares and the client reads.
const (
	// LocationHeader names, on every 202, the operation to poll:
	// /api/v1/operations/{id}.
	LocationHeader = "Location"
	// ReplayedHeader is "true" on the stored answer to an earlier request
	// with the same Idempotency-Key: nothing was done again.
	ReplayedHeader = "Idempotent-Replayed"
)

// Accepted is a 202: work the platform runs in the background.
type Accepted struct {
	// Operation is the operation the 202 carried. Its Id is the operation
	// the Location header names, which is the one to poll.
	Operation *Operation
	// Replayed is true when the answer is the stored one of an earlier
	// request under the same Idempotency-Key (one of this request's own
	// retries reached the platform before): the work was started then, once.
	Replayed bool
}

// accepted reads a 202: the operation in the body, the operation to poll
// from Location, and Idempotent-Replayed. A Location that names another
// operation than the body is an error; without Location (a platform older
// than the declared header) the body's id is polled.
func accepted(what string, resp *http.Response, op *Operation) (*Accepted, error) {
	if op == nil {
		return nil, unexpected(what, resp)
	}
	if loc := strings.TrimSpace(resp.Header.Get(LocationHeader)); loc != "" {
		id, err := operationFromLocation(loc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		switch {
		case op.Id == "":
			op.Id = id
		case op.Id != id:
			return nil, fmt.Errorf("%s: the Location header names operation %q, the answer's body %q", what, id, op.Id)
		}
	}
	if op.Id == "" {
		return nil, fmt.Errorf("%s: the answer names no operation to poll", what)
	}
	return &Accepted{
		Operation: op,
		Replayed:  strings.EqualFold(strings.TrimSpace(resp.Header.Get(ReplayedHeader)), "true"),
	}, nil
}

// operationFromLocation reads {id} from a Location of /api/v1/operations/{id},
// as a path or an absolute URL.
func operationFromLocation(loc string) (string, error) {
	u, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("the Location header %q is not a URL", loc)
	}
	const seg = "/operations/"
	p := u.EscapedPath()
	i := strings.LastIndex(p, seg)
	if i < 0 {
		return "", fmt.Errorf("the Location header %q names no operation", loc)
	}
	id, err := url.PathUnescape(p[i+len(seg):])
	if err != nil || id == "" || strings.Contains(id, "/") {
		return "", fmt.Errorf("the Location header %q names no operation", loc)
	}
	return id, nil
}

// idempotencyKey is a fresh Idempotency-Key for one request, which that
// request's own retries reuse (the generated client sets it once).
func (a *API) idempotencyKey() *string {
	k := a.transport.newKey()
	return &k
}

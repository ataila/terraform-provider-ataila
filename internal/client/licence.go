// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CodeStaleEpoch is the 409 of a bundle whose epoch is not higher than the
// installed document's, the installed bundle itself included.
const CodeStaleEpoch = "stale_epoch"

// BundlePrefix starts the copy-paste form of a licence bundle.
const BundlePrefix = "acplic1."

// BundleDocumentDigest is the sha256 (hex) of a bundle's `document` member,
// the value the platform reports as `document_digest`. It accepts the
// `acplic1.` form and the raw JSON form, as the platform does. The bundle is
// only decoded, never verified: verification is the platform's.
func BundleDocumentDigest(bundle string) (string, error) {
	text := strings.TrimSpace(bundle)
	var raw []byte
	switch {
	case strings.HasPrefix(text, BundlePrefix):
		enc := strings.TrimRight(text[len(BundlePrefix):], "=")
		b, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil {
			return "", errors.New("the bundle is not valid acplic1 base64")
		}
		raw = b
	case strings.HasPrefix(text, "{"):
		raw = []byte(text)
	default:
		return "", fmt.Errorf("the bundle does not start with %q", BundlePrefix)
	}
	var doc struct {
		Document string `json:"document"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", errors.New("the bundle does not hold a JSON object")
	}
	if doc.Document == "" {
		return "", errors.New("the bundle has no document")
	}
	sum := sha256.Sum256([]byte(doc.Document))
	return hex.EncodeToString(sum[:]), nil
}

// GetLicence reads GET /licence.
func (a *API) GetLicence(ctx context.Context) (*Licence, error) {
	rsp, err := a.raw.LicenceGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /licence", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutLicenceBundle sends PUT /licence/bundle. A bundle whose epoch is not
// higher than the installed one's (the installed bundle included) is a 409
// stale_epoch *APIError carrying installed_epoch and
// installed_document_digest.
func (a *API) PutLicenceBundle(ctx context.Context, bundle string) (*LicenceInstalled, error) {
	rsp, err := a.raw.LicenceBundlePutWithResponse(ctx, LicenceBundlePut{Bundle: bundle})
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PUT /licence/bundle", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// GetSocketFacts reads GET /licence/socket-facts.
func (a *API) GetSocketFacts(ctx context.Context) (*SocketFacts, error) {
	rsp, err := a.raw.LicenceSocketFactsWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /licence/socket-facts", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

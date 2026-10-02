// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The licence and brand endpoints of /api/v1, with the platform's rules:
//
//   - the licence is a singleton: read it, install a bundle, read the socket
//     census; nothing activates, signs or removes a licence. A bundle installs
//     only when its epoch is higher than the installed document's, so
//     re-sending the installed bundle is 409 stale_epoch naming
//     installed_epoch and installed_document_digest. The full serial and the
//     installed bundle go only to a principal holding licence-admin-global.
//   - a mock bundle is "acplic1." + base64url(JSON {"document": <string>,
//     "signature": "mock-valid"}), the document being JSON with licence_id,
//     licence_epoch, instance_id, tier and modules. Any other signature is 422
//     bundle_invalid; another instance_id is 422 instance_mismatch.
//   - the brand is a singleton replaced as a whole (PUT) with If-Match
//     "<version>" (412 version_mismatch naming current_version); attribution
//     and first_party are read-only and refused in a body; a replace that
//     changes nothing bumps no version.
//   - brand assets are content-addressed: the same bytes as the same kind
//     return the stored asset (200), as the other kind are 409
//     asset_kind_conflict; there is no delete.

// MockInstanceID is the mock portal's instance id, which bundles bind.
const MockInstanceID = "inst-mock-0001"

const mockBundleSignature = "mock-valid"

type licenceState struct {
	installed   bool
	epoch       int
	licenceID   string
	tier        string
	modules     []string
	bundle      string
	digest      string
	installedAt string
	serial      string
	facts       []map[string]any
	chainIntact bool
}

type mockBrandAsset struct {
	id, kind, sha, mime, filename, uploadedAt string
	width, height                             *int
	bytes                                     int
}

type brandState struct {
	fields     map[string]any
	version    int
	updatedAt  string
	firstParty bool
	assets     map[string]*mockBrandAsset
	noStorage  bool
	bumpOnRead bool // the next read is followed by a change in the portal
}

func newLicenceState() *licenceState {
	ts := now()
	return &licenceState{serial: "ACP-MOCK-1111-2222-3333", chainIntact: true, facts: []map[string]any{
		{"node_name": "node-a", "cluster": "cluster-1", "sockets": 2, "cores_per_socket": 16, "source": "proxmox", "measured_at": ts},
		{"node_name": "node-b", "cluster": "cluster-1", "sockets": 1, "cores_per_socket": 8, "source": "proxmox", "measured_at": ts},
	}}
}

func newBrandState() *brandState {
	return &brandState{version: 1, updatedAt: now(), assets: map[string]*mockBrandAsset{}, fields: map[string]any{
		"product_name": "Example Cloud", "product_name_accent": "Cloud", "brand_color": "#2196f3",
		"page_title": "Example Cloud", "logo_size": "regular", "logo_offset_x": 0,
		"logo_asset_id": nil, "favicon_asset_id": nil,
	}}
}

// ── test helpers ─────────────────────────────────────────────────────────────

// MockBundle is a bundle the mock accepts (or, with another instance id or
// signature, refuses).
func MockBundle(epoch int, instanceID, signature string) string {
	doc, _ := json.Marshal(map[string]any{
		"licence_id": fmt.Sprintf("LIC-MOCK-%03d", epoch), "licence_epoch": epoch, "instance_id": instanceID,
		"tier": "sp", "modules": []string{"projects", "sp-mode"},
		"valid_from": "2026-01-01T00:00:00Z", "valid_until": "2027-01-01T00:00:00Z",
	})
	b, _ := json.Marshal(map[string]any{"document": string(doc), "signature": signature})
	return "acplic1." + base64.RawURLEncoding.EncodeToString(b)
}

// InstallLicence installs a bundle out of band (as the portal's Licence page
// does), whatever its epoch.
func (m *MockAPI) InstallLicence(bundle string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, _, err := decodeMockBundle(bundle)
	if err != nil {
		panic(err)
	}
	m.installBundle(bundle, doc)
}

// LicenceDigest is the installed document's digest ("" when none).
func (m *MockAPI) LicenceDigest() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.licence.digest
}

// SetBrandField changes a brand field out of band, bumping the version.
func (m *MockAPI) SetBrandField(field string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.brand.fields[field] = value
	m.brand.version++
	m.brand.updatedAt = now()
}

// ChangeBrandAfterNextRead makes someone change the brand in the portal right
// after the next read of it, so that a write naming the version just read
// meets 412 version_mismatch.
func (m *MockAPI) ChangeBrandAfterNextRead() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.brand.bumpOnRead = true
}

// Brand is the wire form of the brand.
func (m *MockAPI) Brand() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.brandWire()
}

// SetBrandStorage switches object storage for assets on or off.
func (m *MockAPI) SetBrandStorage(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.brand.noStorage = !on
}

// BrandAssetCount is how many assets are stored.
func (m *MockAPI) BrandAssetCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.brand.assets)
}

// ── licence ──────────────────────────────────────────────────────────────────

func decodeMockBundle(bundle string) (map[string]any, string, error) {
	text := strings.TrimSpace(bundle)
	var raw []byte
	switch {
	case strings.HasPrefix(text, "acplic1."):
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(text[len("acplic1."):], "="))
		if err != nil {
			return nil, "", fmt.Errorf("bundle is not valid acplic1 base64 JSON")
		}
		raw = b
	case strings.HasPrefix(text, "{"):
		raw = []byte(text)
	default:
		return nil, "", fmt.Errorf("expected an `acplic1.` bundle")
	}
	var outer map[string]any
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, "", fmt.Errorf("bundle is not valid JSON")
	}
	docText, _ := outer["document"].(string)
	if docText == "" {
		return nil, "", fmt.Errorf("bundle has no `document`")
	}
	if sig, _ := outer["signature"].(string); sig != mockBundleSignature {
		return nil, "", fmt.Errorf("the bundle's signature does not verify against the pinned roots")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(docText), &doc); err != nil {
		return nil, "", fmt.Errorf("the document is not JSON")
	}
	sum := sha256.Sum256([]byte(docText))
	return doc, hex.EncodeToString(sum[:]), nil
}

func (m *MockAPI) installBundle(bundle string, doc map[string]any) {
	l := m.licence
	epoch, _ := doc["licence_epoch"].(float64)
	_, digest, _ := decodeMockBundle(bundle)
	l.installed, l.epoch, l.bundle, l.digest, l.installedAt = true, int(epoch), bundle, digest, now()
	l.licenceID, _ = doc["licence_id"].(string)
	l.tier, _ = doc["tier"].(string)
	l.modules = nil
	if mods, ok := doc["modules"].([]any); ok {
		for _, x := range mods {
			l.modules = append(l.modules, fmt.Sprint(x))
		}
	}
}

func (m *MockAPI) routeLicence(c *call) reply {
	switch {
	case c.path == "/licence" && c.r.Method == http.MethodGet:
		if r := m.require(c, "licence-read-global", "licence-admin-global"); r != nil {
			return *r
		}
		return ok(http.StatusOK, m.licenceWire())
	case c.path == "/licence/bundle" && c.r.Method == http.MethodPut:
		return m.licenceBundlePut(c)
	case c.path == "/licence/socket-facts" && c.r.Method == http.MethodGet:
		if r := m.require(c, "licence-read-global", "licence-admin-global"); r != nil {
			return *r
		}
		total := 0
		for _, f := range m.licence.facts {
			total += f["sockets"].(int)
		}
		return ok(http.StatusOK, map[string]any{"facts": m.licence.facts, "total": total,
			"chain": map[string]any{"rows": len(m.licence.facts), "intact": m.licence.chainIntact, "first_broken_row": nil}})
	case c.path == "/licence" || c.path == "/licence/bundle" || c.path == "/licence/socket-facts":
		return c.methodNotAllowed()
	}
	return c.problem(http.StatusNotFound, "not_found", "", nil)
}

func (m *MockAPI) licenceWire() map[string]any {
	l := m.licence
	full := m.scopes()["licence-admin-global"]
	out := map[string]any{
		"state": "UNLICENSED", "state_reason": "No licence is installed.", "overlays": []string{},
		"serial": nil, "serial_masked": false, "licence_id": nil, "licence_epoch": nil, "product_code": nil,
		"tier": nil, "licence_class": nil, "tenancy_mode": nil, "modules": []string{},
		"sockets":    map[string]any{"licensed": nil, "uncapped": false, "observed": 3},
		"valid_from": nil, "valid_until": nil, "grace_days": nil, "days_remaining": nil,
		"instance_id": MockInstanceID, "fqdn": "portal.example.com", "bound_fqdn": nil,
		"document_digest": nil, "bundle": nil, "installed_at": nil,
		"growth_allowed": false, "writes_allowed": true, "sp_mode_enabled": false,
	}
	if !l.installed {
		return out
	}
	serial := l.serial
	if !full {
		serial = "****-" + serial[strings.LastIndex(serial, "-")+1:]
	}
	var bundle any
	if full {
		bundle = l.bundle
	}
	for k, v := range map[string]any{
		"state": "ACTIVE", "state_reason": "", "serial": serial, "serial_masked": !full, "licence_id": l.licenceID,
		"licence_epoch": l.epoch, "product_code": "acp", "tier": l.tier, "licence_class": "subscription",
		"tenancy_mode": "multi", "modules": l.modules,
		"sockets":    map[string]any{"licensed": 8, "uncapped": false, "observed": 3},
		"valid_from": "2026-01-01T00:00:00Z", "valid_until": "2027-01-01T00:00:00Z", "grace_days": 14,
		"days_remaining": 93, "bound_fqdn": "portal.example.com", "document_digest": l.digest, "bundle": bundle,
		"installed_at": l.installedAt, "growth_allowed": true, "sp_mode_enabled": true,
	} {
		out[k] = v
	}
	return out
}

func (m *MockAPI) licenceBundlePut(c *call) reply {
	if r := m.require(c, "licence-admin-global"); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "bundle")
	bundle, _ := strField(v, body, "bundle", true, false, length(1, 65536))
	if r := v.reply(c); r != nil {
		return *r
	}
	doc, digest, err := decodeMockBundle(*bundle)
	if err != nil {
		return c.problem(http.StatusUnprocessableEntity, "bundle_invalid", err.Error(), nil)
	}
	if inst, _ := doc["instance_id"].(string); inst != MockInstanceID {
		return c.problem(http.StatusUnprocessableEntity, "instance_mismatch",
			"The bundle is bound to another portal instance.", nil)
	}
	epoch, _ := doc["licence_epoch"].(float64)
	if m.licence.installed && int(epoch) <= m.licence.epoch {
		return c.problem(http.StatusConflict, "stale_epoch",
			fmt.Sprintf("This portal already holds a licence document with epoch %d; a bundle installs only "+
				"when its epoch is higher. Nothing was installed.", m.licence.epoch),
			map[string]any{"installed_epoch": m.licence.epoch, "installed_document_digest": m.licence.digest,
				"already_installed": digest == m.licence.digest})
	}
	m.installBundle(*bundle, doc)
	out := m.licenceWire()
	out["installed"] = true
	out["warnings"] = []any{}
	return ok(http.StatusOK, out)
}

// ── brand ────────────────────────────────────────────────────────────────────

var (
	rxHexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	rxIfMatch   = regexp.MustCompile(`^(W/)?"?([0-9]{1,9})"?$`)
)

func (m *MockAPI) brandWire() map[string]any {
	b := m.brand
	out := map[string]any{}
	for k, v := range b.fields {
		out[k] = v
	}
	out["first_party"] = b.firstParty
	out["attribution"] = "Powered by the mock platform"
	out["version"] = b.version
	out["updated_at"] = b.updatedAt
	return out
}

func (m *MockAPI) routeBrand(c *call) reply {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	switch {
	case len(parts) == 1 && c.r.Method == http.MethodGet:
		if r := m.require(c, "brand-center-read-global", "brand-center-admin-global"); r != nil {
			return *r
		}
		rep := ok(http.StatusOK, m.brandWire())
		rep.headers = map[string]string{"ETag": fmt.Sprintf(`"%d"`, m.brand.version)}
		if m.brand.bumpOnRead {
			m.brand.bumpOnRead = false
			m.brand.fields["page_title"] = "Changed in the portal"
			m.brand.version++
			m.brand.updatedAt = now()
		}
		return rep
	case len(parts) == 1 && c.r.Method == http.MethodPut:
		return m.brandPut(c)
	case len(parts) == 2 && parts[1] == "assets" && c.r.Method == http.MethodGet:
		return m.brandAssetsList(c)
	case len(parts) == 2 && parts[1] == "assets" && c.r.Method == http.MethodPost:
		return m.brandAssetsCreate(c)
	case len(parts) == 3 && parts[1] == "assets" && c.r.Method == http.MethodGet:
		if r := m.require(c, "brand-center-read-global", "brand-center-admin-global"); r != nil {
			return *r
		}
		id, valid := uuidID(parts[2])
		a := m.brand.assets[id]
		if !valid || a == nil {
			return c.problem(http.StatusNotFound, "brand_asset_not_found", "No such brand asset.", nil)
		}
		return ok(http.StatusOK, assetWire(a))
	case len(parts) <= 3:
		return c.methodNotAllowed()
	}
	return c.problem(http.StatusNotFound, "not_found", "", nil)
}

func (m *MockAPI) brandPut(c *call) reply {
	if r := m.require(c, "brand-center-admin-global"); r != nil {
		return *r
	}
	expected := -1
	if raw := strings.TrimSpace(c.r.Header.Get("If-Match")); raw != "" && raw != "*" {
		match := rxIfMatch.FindStringSubmatch(raw)
		if match == nil {
			return c.problem(http.StatusBadRequest, "invalid_if_match",
				`If-Match must be the brand's version as an entity tag, e.g. "7".`, nil)
		}
		expected, _ = strconv.Atoi(match[2])
	}
	v := &validation{}
	body := decodeBody(c, v, "product_name", "product_name_accent", "brand_color", "page_title", "logo_size",
		"logo_offset_x", "logo_asset_id", "favicon_asset_id")
	name, _ := strField(v, body, "product_name", true, false, length(2, 32))
	accent, _ := strField(v, body, "product_name_accent", true, false, length(0, 32))
	colour, _ := strField(v, body, "brand_color", true, false, pattern(rxHexColour))
	title, _ := strField(v, body, "page_title", true, false, length(1, 60))
	size, _ := strField(v, body, "logo_size", true, false, oneOf("compact", "medium", "regular", "large"))
	offset, _ := intField(v, body, "logo_offset_x", false, func(n int) string {
		if n < -40 || n > 40 {
			return "Input should be between -40 and 40"
		}
		return ""
	})
	for _, f := range []string{"logo_offset_x", "logo_asset_id", "favicon_asset_id"} {
		if _, present := body[f]; !present {
			v.fail(f, "Field required")
		}
	}
	logo, _ := strField(v, body, "logo_asset_id", false, true, nil)
	favicon, _ := strField(v, body, "favicon_asset_id", false, true, nil)
	if name != nil && accent != nil && *accent != "" && !strings.Contains(*name, *accent) {
		v.fail("", "product_name_accent must be part of product_name")
	}
	if r := v.reply(c); r != nil {
		return *r
	}
	if expected >= 0 && expected != m.brand.version {
		return c.problem(http.StatusPreconditionFailed, "version_mismatch",
			fmt.Sprintf("The brand is at version %d, not %d.", m.brand.version, expected),
			map[string]any{"current_version": m.brand.version})
	}
	checkAsset := func(id *string, kind, field string) (any, *reply) {
		if id == nil {
			return nil, nil
		}
		canon, valid := uuidID(*id)
		a := m.brand.assets[canon]
		if !valid || a == nil {
			r := c.problem(http.StatusUnprocessableEntity, "asset_not_found", field+": no brand asset has this id.",
				map[string]any{"field": field})
			return nil, &r
		}
		if a.kind != kind {
			r := c.problem(http.StatusUnprocessableEntity, "asset_wrong_kind",
				fmt.Sprintf("%s: the asset is a %s, not a %s.", field, a.kind, kind),
				map[string]any{"field": field, "kind": a.kind})
			return nil, &r
		}
		return canon, nil
	}
	logoID, bad := checkAsset(logo, "logo", "logo_asset_id")
	if bad != nil {
		return *bad
	}
	faviconID, bad := checkAsset(favicon, "favicon", "favicon_asset_id")
	if bad != nil {
		return *bad
	}
	wanted := map[string]any{"product_name": *name, "product_name_accent": *accent,
		"brand_color": strings.ToLower(*colour), "page_title": *title, "logo_size": *size,
		"logo_offset_x": *offset, "logo_asset_id": logoID, "favicon_asset_id": faviconID}
	same := true
	for k, val := range wanted {
		same = same && fmt.Sprint(m.brand.fields[k]) == fmt.Sprint(val)
	}
	if !same {
		m.brand.fields = wanted
		m.brand.version++
		m.brand.updatedAt = now()
	}
	rep := ok(http.StatusOK, m.brandWire())
	rep.headers = map[string]string{"ETag": fmt.Sprintf(`"%d"`, m.brand.version)}
	return rep
}

func assetWire(a *mockBrandAsset) map[string]any {
	var w, h any
	if a.width != nil {
		w = *a.width
	}
	if a.height != nil {
		h = *a.height
	}
	return map[string]any{"id": a.id, "kind": a.kind, "sha256": a.sha, "mime": a.mime, "width": w, "height": h,
		"bytes": a.bytes, "filename": a.filename, "url": "/api/brand/assets/" + a.sha, "uploaded_at": a.uploadedAt}
}

func (m *MockAPI) brandAssetsList(c *call) reply {
	if r := m.require(c, "brand-center-read-global", "brand-center-admin-global"); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	after := ""
	if p.after != nil {
		after, _ = p.after["id"].(string)
	}
	q := c.r.URL.Query()
	ids := make([]string, 0, len(m.brand.assets))
	for id := range m.brand.assets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var items []map[string]any
	for _, id := range ids {
		a := m.brand.assets[id]
		if id <= after || (q.Has("kind") && a.kind != q.Get("kind")) || (q.Has("sha256") && a.sha != q.Get("sha256")) {
			continue
		}
		items = append(items, assetWire(a))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		return map[string]any{"id": it["id"]}
	}))
}

// sniff is the mock's file check: PNG, SVG or ICO, as the kind allows.
func sniff(raw []byte, kind string) (mime string, width *int, problem string) {
	switch {
	case len(raw) > 512*1024:
		return "", nil, "the file is larger than 512 KB"
	case len(raw) >= 24 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		w := int(raw[16])<<24 | int(raw[17])<<16 | int(raw[18])<<8 | int(raw[19])
		if kind == "logo" && (w < 120 || w > 2000) {
			return "", nil, fmt.Sprintf("a logo must be 120-2000 px wide, this one is %d", w)
		}
		return "image/png", &w, ""
	case strings.HasPrefix(strings.TrimSpace(string(raw)), "<svg"):
		if strings.Contains(string(raw), "<script") {
			return "", nil, "the SVG has active content"
		}
		return "image/svg+xml", nil, ""
	case kind == "favicon" && len(raw) >= 4 && string(raw[:4]) == "\x00\x00\x01\x00":
		return "image/x-icon", nil, ""
	}
	return "", nil, "the file is not a PNG, SVG or ICO"
}

func (m *MockAPI) brandAssetsCreate(c *call) reply {
	if r := m.require(c, "brand-center-admin-global"); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "kind", "content_base64", "filename")
	kind, _ := strField(v, body, "kind", true, false, oneOf("logo", "favicon"))
	content, _ := strField(v, body, "content_base64", true, false, length(1, 700000))
	filename, _ := strField(v, body, "filename", false, false, length(0, 200))
	if r := v.reply(c); r != nil {
		return *r
	}
	raw, err := base64.StdEncoding.DecodeString(*content)
	if err != nil {
		return c.problem(http.StatusUnprocessableEntity, "asset_invalid", "content_base64 is not valid base64.", nil)
	}
	mime, width, problem := sniff(raw, *kind)
	if problem != "" {
		return c.problem(http.StatusUnprocessableEntity, "asset_invalid", problem, nil)
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	for _, a := range m.brand.assets {
		if a.sha != sha {
			continue
		}
		if a.kind != *kind {
			return c.problem(http.StatusConflict, "asset_kind_conflict",
				fmt.Sprintf("These bytes are already stored as a %s.", a.kind),
				map[string]any{"existing_asset_id": a.id, "existing_kind": a.kind})
		}
		return ok(http.StatusOK, assetWire(a))
	}
	if m.brand.noStorage {
		return c.problem(http.StatusServiceUnavailable, "storage_unavailable",
			"Object storage is not configured on this platform, so the asset cannot be stored.", nil)
	}
	name := ""
	if filename != nil {
		name = *filename
	}
	a := &mockBrandAsset{id: uuid.NewString(), kind: *kind, sha: sha, mime: mime, filename: name,
		uploadedAt: wireTime(time.Now()), width: width, bytes: len(raw)}
	if width != nil {
		h := *width / 2
		a.height = &h
	}
	m.brand.assets[a.id] = a
	return ok(http.StatusCreated, assetWire(a))
}

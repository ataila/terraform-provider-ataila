// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Brand Center application profiles (platform 1.0.305), as routers/brand.py
// serves them: PUT /brand/apps/{slug} replaces every writable member (each
// required, each nullable = inherited) and creates a missing profile (201);
// with If-Match it is conditional, and If-Match on a slug without a profile is
// a 412 at version 0; a replace that changes nothing keeps the version;
// DELETE is the admin key, not destroy-gated.

type mockBrandApp struct {
	fields     map[string]any
	version    int
	updatedAt  string
	bumpOnRead bool // the next read is followed by a change in the portal
}

var (
	brandAppMembers = []string{"display_name", "display_name_accent", "primary_color", "on_primary_color",
		"secondary_color", "font_preset", "header_logo_asset_id", "header_logo_size", "favicon_asset_id",
		"og_image_asset_id", "footer_text_en", "footer_text_hu", "footer_links", "co_brand_line_en",
		"co_brand_line_hu", "default_language", "legal_privacy_url", "legal_imprint_url"}
	rxMockSlug     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	rxMockLowerHex = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	rxMockHTTPS    = regexp.MustCompile(`^https://`)
)

func (m *MockAPI) brandApps() map[string]*mockBrandApp {
	if m.brand.apps == nil {
		m.brand.apps = map[string]*mockBrandApp{}
	}
	return m.brand.apps
}

// SetBrandAppInPortal creates or changes an application's profile as the
// portal would, bumping its version.
func (m *MockAPI) SetBrandAppInPortal(slug string, fields map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	apps := m.brandApps()
	app := apps[slug]
	if app == nil {
		app = &mockBrandApp{fields: map[string]any{}}
		for _, k := range brandAppMembers {
			app.fields[k] = nil
		}
		apps[slug] = app
	}
	for k, v := range fields {
		app.fields[k] = v
	}
	app.version++
	app.updatedAt = now()
}

// ChangeBrandAppAfterNextRead makes the portal change the profile right after
// the provider's next read of it, so that the provider's write names a stale
// version.
func (m *MockAPI) ChangeBrandAppAfterNextRead(slug string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if app := m.brandApps()[slug]; app != nil {
		app.bumpOnRead = true
	}
}

// BrandApp is an application's stored profile (nil without one).
func (m *MockAPI) BrandApp(slug string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	app := m.brandApps()[slug]
	if app == nil {
		return nil
	}
	out := map[string]any{"version": app.version}
	for k, v := range app.fields {
		out[k] = v
	}
	return out
}

func brandAppWire(slug string, app *mockBrandApp) map[string]any {
	out := map[string]any{"app_slug": slug, "version": app.version, "updated_at": app.updatedAt}
	for _, k := range brandAppMembers {
		out[k] = app.fields[k]
	}
	return out
}

func (m *MockAPI) routeBrandApps(c *call, parts []string) reply {
	switch {
	case len(parts) == 2 && c.r.Method == http.MethodGet:
		if r := m.require(c, "brand-center-read-global", "brand-center-admin-global"); r != nil {
			return *r
		}
		slugs := make([]string, 0, len(m.brandApps()))
		for s := range m.brandApps() {
			slugs = append(slugs, s)
		}
		sort.Strings(slugs)
		items := make([]map[string]any, 0, len(slugs))
		for _, s := range slugs {
			a := m.brandApps()[s]
			items = append(items, map[string]any{"app_slug": s, "version": a.version, "updated_at": a.updatedAt})
		}
		return ok(http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
	case len(parts) == 2:
		return c.methodNotAllowed()
	}
	slug := parts[2]
	if !rxMockSlug.MatchString(slug) {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "The application slug is not valid.", nil)
	}
	app := m.brandApps()[slug]
	switch {
	case len(parts) == 3 && c.r.Method == http.MethodGet:
		if r := m.require(c, "brand-center-read-global", "brand-center-admin-global"); r != nil {
			return *r
		}
		if app == nil {
			return c.problem(http.StatusNotFound, "brand_app_not_found", "This application has no brand profile.", nil)
		}
		rep := ok(http.StatusOK, brandAppWire(slug, app))
		if app.bumpOnRead {
			app.bumpOnRead = false
			app.fields["footer_text_en"] = "Changed in the portal"
			app.version++
			app.updatedAt = now()
		}
		return rep
	case len(parts) == 3 && c.r.Method == http.MethodPut:
		return m.brandAppPut(c, slug, app)
	case len(parts) == 3 && c.r.Method == http.MethodDelete:
		if r := m.require(c, "brand-center-admin-global"); r != nil {
			return *r
		}
		if app == nil {
			return c.problem(http.StatusNotFound, "brand_app_not_found", "This application has no brand profile.", nil)
		}
		delete(m.brandApps(), slug)
		return reply{status: http.StatusNoContent}
	case len(parts) == 4 && parts[3] == "resolved" && c.r.Method == http.MethodGet:
		if r := m.require(c, "brand-center-read-global", "brand-center-admin-global"); r != nil {
			return *r
		}
		return ok(http.StatusOK, m.brandAppResolved(slug, app))
	case len(parts) <= 4:
		return c.methodNotAllowed()
	}
	return c.problem(http.StatusNotFound, "not_found", "", nil)
}

func (m *MockAPI) brandAppPut(c *call, slug string, app *mockBrandApp) reply {
	if r := m.require(c, "brand-center-admin-global"); r != nil {
		return *r
	}
	expected := -1
	if raw := strings.TrimSpace(c.r.Header.Get("If-Match")); raw != "" && raw != "*" {
		match := rxIfMatch.FindStringSubmatch(raw)
		if match == nil {
			return c.problem(http.StatusBadRequest, "invalid_if_match",
				`If-Match must be the profile's version as an entity tag, e.g. "3".`, nil)
		}
		expected, _ = strconv.Atoi(match[2])
	}
	v := &validation{}
	body := decodeBody(c, v, brandAppMembers...)
	for _, k := range brandAppMembers {
		if _, present := body[k]; !present {
			v.fail(k, "Field required")
		}
	}
	name, _ := strField(v, body, "display_name", false, true, length(2, 32))
	accent, _ := strField(v, body, "display_name_accent", false, true, length(0, 32))
	for _, k := range []string{"primary_color", "on_primary_color", "secondary_color"} {
		strField(v, body, k, false, true, pattern(rxMockLowerHex))
	}
	strField(v, body, "font_preset", false, true, oneOf("inter", "montserrat", "system"))
	strField(v, body, "header_logo_size", false, true, oneOf("compact", "medium", "regular", "large"))
	strField(v, body, "default_language", false, true, oneOf("en", "hu"))
	strField(v, body, "footer_text_en", false, true, length(0, 160))
	strField(v, body, "footer_text_hu", false, true, length(0, 160))
	strField(v, body, "co_brand_line_en", false, true, length(0, 120))
	strField(v, body, "co_brand_line_hu", false, true, length(0, 120))
	for _, k := range []string{"legal_privacy_url", "legal_imprint_url"} {
		strField(v, body, k, false, true, pattern(rxMockHTTPS))
	}
	if links, isList := body["footer_links"].([]any); isList && len(links) > 5 {
		v.fail("footer_links", "List should have at most 5 items")
	}
	if accent != nil && name == nil {
		v.fail("display_name_accent", "display_name_accent needs display_name")
	}
	if name != nil && accent != nil && *accent != "" && !strings.Contains(*name, *accent) {
		v.fail("", "display_name_accent must be part of display_name")
	}
	if r := v.reply(c); r != nil {
		return *r
	}
	current := 0
	if app != nil {
		current = app.version
	}
	if expected >= 0 && (app == nil || expected != app.version) {
		return c.problem(http.StatusPreconditionFailed, "version_mismatch",
			fmt.Sprintf("The profile is at version %d, not %d.", current, expected),
			map[string]any{"current_version": current})
	}
	for field, kinds := range map[string][]string{"header_logo_asset_id": {"header_logo", "logo"},
		"favicon_asset_id": {"favicon"}, "og_image_asset_id": {"og_image"}} {
		raw, _ := body[field].(string)
		if raw == "" {
			continue
		}
		canon, valid := uuidID(raw)
		a := m.brand.assets[canon]
		if !valid || a == nil {
			return c.problem(http.StatusUnprocessableEntity, "asset_not_found", field+": no brand asset has this id.",
				map[string]any{"field": field})
		}
		if !contains(kinds, a.kind) {
			return c.problem(http.StatusUnprocessableEntity, "asset_wrong_kind",
				fmt.Sprintf("%s: the asset is a %s.", field, a.kind), map[string]any{"field": field, "kind": a.kind})
		}
		body[field] = canon
	}
	status := http.StatusOK
	if app == nil {
		app = &mockBrandApp{fields: map[string]any{}}
		m.brandApps()[slug] = app
		status = http.StatusCreated
	}
	same := app.version > 0
	for _, k := range brandAppMembers {
		same = same && fmt.Sprint(app.fields[k]) == fmt.Sprint(body[k])
	}
	if !same {
		for _, k := range brandAppMembers {
			app.fields[k] = body[k]
		}
		app.version++
		app.updatedAt = now()
	}
	rep := ok(status, brandAppWire(slug, app))
	rep.headers = map[string]string{"ETag": fmt.Sprintf(`"%d"`, app.version)}
	return rep
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// brandAppResolved resolves a profile against the mock's installation brand:
// enough of the real resolution for the data source's attributes.
func (m *MockAPI) brandAppResolved(slug string, app *mockBrandApp) map[string]any {
	source := map[string]any{}
	pick := func(field string, installation any, fallback any) any {
		if app != nil && app.fields[field] != nil {
			source[field] = "profile"
			return app.fields[field]
		}
		if installation != nil {
			source[field] = "installation"
			return installation
		}
		source[field] = "default"
		return fallback
	}
	name := pick("display_name", nil, strings.ToUpper(slug[:1])+slug[1:])
	accent := pick("display_name_accent", nil, "")
	primary := pick("primary_color", m.brand.fields["brand_color"], "#2196f3")
	secondary := pick("secondary_color", nil, "#ffcf00")
	onPrimary := pick("on_primary_color", nil, "#ffffff")
	font := pick("font_preset", nil, "inter")
	lang := pick("default_language", nil, "hu")
	links := pick("footer_links", nil, []any{})
	textEN := pick("footer_text_en", nil, "")
	textHU := pick("footer_text_hu", nil, "")
	coEN := pick("co_brand_line_en", nil, "")
	coHU := pick("co_brand_line_hu", nil, "")
	privacy := pick("legal_privacy_url", nil, nil)
	imprint := pick("legal_imprint_url", nil, nil)
	for _, k := range []string{"header_logo_asset_id", "header_logo_size", "favicon_asset_id", "og_image_asset_id"} {
		pick(k, nil, nil)
	}
	shades := map[string]any{}
	for _, k := range []string{"50", "100", "200", "300", "400", "500", "600", "700", "800", "900"} {
		shades[k] = primary
	}
	version := 0
	if app != nil {
		version = app.version
	}
	return map[string]any{
		"app_slug": slug, "display_name": name, "display_name_accent": accent,
		"display_name_split": []any{"", name, ""}, "primary_color": primary, "shades": shades,
		"on_primary": onPrimary, "secondary_color": secondary, "font_preset": font,
		"font_stack": "Inter, sans-serif", "default_language": lang,
		"assets": map[string]any{"header_logo_url": nil, "favicon_url": nil, "og_image_url": nil,
			"header_logo_size": map[string]any{"max_height": 40, "max_width": "88%"}},
		"footer": map[string]any{"text": map[string]any{"en": textEN, "hu": textHU},
			"co_brand_line": map[string]any{"en": coEN, "hu": coHU}, "links": links},
		"legal":       map[string]any{"privacy_url": privacy, "imprint_url": imprint},
		"attribution": "Powered by the mock platform", "first_party": m.brand.firstParty,
		"source": source, "etag": fmt.Sprintf("app-%s-%d", slug, version), "version": version,
		"install_version": m.brand.version,
	}
}

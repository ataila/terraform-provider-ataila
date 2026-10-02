// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strings"
)

// ProblemContentType is the media type of an RFC 9457 problem document.
const ProblemContentType = "application/problem+json"

// LicenceCodePrefix starts the code of every licence refusal. A 403 carrying
// such a code is final: retrying cannot change the platform's licence.
const LicenceCodePrefix = "licence_"

// maxRawBody bounds how much of a non-problem error body is kept.
const maxRawBody = 512

// APIError is a non-2xx answer from the API.
type APIError struct {
	// StatusCode is the HTTP status of the last attempt.
	StatusCode int
	// Method and Path identify the request (the path only, never the host).
	Method string
	Path   string
	// Problem is the parsed problem document, or nil when the body was not one
	// (for example an HTML page from a proxy in front of the API).
	Problem *Problem
	// RawBody holds the start of a body that was not a problem document.
	RawBody string
	// HeaderRequestID is the X-Request-ID response header.
	HeaderRequestID string
	// Attempts counts the requests sent, retries included.
	Attempts int
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if code := e.Code(); code != "" {
		fmt.Fprintf(&b, " (%s)", code)
	}
	if d := e.detail(); d != "" {
		fmt.Fprintf(&b, ": %s", d)
	}
	if id := e.RequestID(); id != "" {
		fmt.Fprintf(&b, " [request_id %s]", id)
	}
	return b.String()
}

// Code is the problem's machine-readable code, or "" when there is none.
func (e *APIError) Code() string {
	if e.Problem == nil {
		return ""
	}
	return e.Problem.Code
}

// Title is the problem's title, or the HTTP status text.
func (e *APIError) Title() string {
	if e.Problem != nil && e.Problem.Title != "" {
		return e.Problem.Title
	}
	if t := http.StatusText(e.StatusCode); t != "" {
		return t
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// RequestID is the id the API logged this request under.
func (e *APIError) RequestID() string {
	if e.Problem != nil && e.Problem.RequestId != nil && *e.Problem.RequestId != "" {
		return *e.Problem.RequestId
	}
	return e.HeaderRequestID
}

// IsNotFound reports a 404.
func (e *APIError) IsNotFound() bool { return e.StatusCode == http.StatusNotFound }

// IsFinalUnavailable reports a 503 that retrying cannot cure: the AI gateway
// is not configured on this platform or cannot be reached, or the secrets store refused to
// store a key's value. The provider does not retry it.
func (e *APIError) IsFinalUnavailable() bool {
	return e.StatusCode == http.StatusServiceUnavailable && finalUnavailable[e.Code()]
}

// IsLicenceRefusal reports a 403 whose code starts with "licence_".
func (e *APIError) IsLicenceRefusal() bool {
	return e.StatusCode == http.StatusForbidden && strings.HasPrefix(e.Code(), LicenceCodePrefix)
}

// Remedy is what the API says will lift a licence refusal.
func (e *APIError) Remedy() string {
	if s := e.extraString("remedy"); s != "" {
		return s
	}
	return e.detail()
}

func (e *APIError) detail() string {
	if e.Problem != nil && e.Problem.Detail != nil {
		return strings.TrimSpace(*e.Problem.Detail)
	}
	return ""
}

func (e *APIError) extraString(key string) string {
	if e.Problem == nil {
		return ""
	}
	v, ok := e.Problem.Get(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// Summary is a one-line diagnostic summary.
func (e *APIError) Summary() string {
	if e.IsLicenceRefusal() {
		return fmt.Sprintf("Refused by the platform licence (%s)", e.Code())
	}
	return "ATAILA API error: " + e.Title()
}

// Detail is the diagnostic body: what the API said, then the facts an
// operator needs to find the request in the API's own logs.
func (e *APIError) Detail() string {
	var parts []string

	if e.IsLicenceRefusal() {
		if r := e.Remedy(); r != "" {
			parts = append(parts, "Remedy: "+r)
		}
		if state := e.extraString("state"); state != "" {
			line := "Licence state: " + state
			if reason := e.extraString("state_reason"); reason != "" {
				line += " (" + reason + ")"
			}
			parts = append(parts, line)
		}
		parts = append(parts, "A licence refusal is final: the provider does not retry it.")
	} else {
		if d := e.detail(); d != "" {
			parts = append(parts, d)
		}
		if fields := e.validationErrors(); fields != "" {
			parts = append(parts, fields)
		}
		if extra := e.ExtraMembers(); len(extra) > 0 {
			parts = append(parts, strings.Join(extra, "\n"))
		}
		if e.Problem == nil && e.RawBody != "" {
			parts = append(parts, "The response was not a problem document. It began with:\n"+e.RawBody)
		}
	}

	facts := []string{fmt.Sprintf("HTTP %d on %s %s", e.StatusCode, e.Method, e.Path)}
	if c := e.Code(); c != "" {
		facts = append(facts, "code: "+c)
	}
	if id := e.RequestID(); id != "" {
		facts = append(facts, "request_id: "+id)
	}
	if e.Attempts > 1 {
		facts = append(facts, fmt.Sprintf("attempts: %d", e.Attempts))
	}
	parts = append(parts, strings.Join(facts, "\n"))
	return strings.Join(parts, "\n\n")
}

// Extra returns a member of the problem document beyond the standard ones,
// for example the "blockers" of a refused delete.
func (e *APIError) Extra(key string) (any, bool) {
	if e.Problem == nil {
		return nil, false
	}
	return e.Problem.Get(key)
}

// Blockers is the "blockers" member of a 409 (what keeps an object from
// being deleted, with a count each), or nil.
func (e *APIError) Blockers() map[string]int {
	raw, ok := e.Extra("blockers")
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		if n, ok := v.(float64); ok {
			out[k] = int(n)
		}
	}
	return out
}

// ExtraMembers renders the problem's non-standard members as sorted
// "name: value" lines, an object as "name: k=v, k=v". The validation "errors"
// list is rendered separately and left out here.
func (e *APIError) ExtraMembers() []string {
	if e.Problem == nil || len(e.Problem.AdditionalProperties) == 0 {
		return nil
	}
	var lines []string
	for k, v := range e.Problem.AdditionalProperties {
		if k == "errors" {
			continue
		}
		lines = append(lines, k+": "+renderValue(v))
	}
	sort.Strings(lines)
	return lines
}

func renderValue(v any) string {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+renderValue(t[k]))
		}
		return strings.Join(parts, ", ")
	case []interface{}:
		parts := make([]string, 0, len(t))
		objects := false
		for _, e := range t {
			_, isObject := e.(map[string]interface{})
			objects = objects || isObject
			parts = append(parts, renderValue(e))
		}
		if objects {
			return strings.Join(parts, "; ") // a list of objects: one per part
		}
		return strings.Join(parts, ", ")
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprint(t)
	case nil:
		return "null"
	}
	return fmt.Sprint(v)
}

// validationErrors renders the "errors" member of a 422.
func (e *APIError) validationErrors() string {
	if e.Problem == nil {
		return ""
	}
	raw, ok := e.Problem.Get("errors")
	if !ok {
		return ""
	}
	list, ok := raw.([]interface{})
	if !ok || len(list) == 0 {
		return ""
	}
	lines := make([]string, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		var loc []string
		if l, ok := m["loc"].([]interface{}); ok {
			for _, p := range l {
				loc = append(loc, fmt.Sprint(p))
			}
		}
		lines = append(lines, fmt.Sprintf("- %s: %v", strings.Join(loc, "."), m["msg"]))
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return ""
	}
	return "Invalid fields:\n" + strings.Join(lines, "\n")
}

// newAPIError builds the error for a non-2xx response whose body was read.
func newAPIError(resp *http.Response, body []byte, attempts int) *APIError {
	e := &APIError{
		StatusCode:      resp.StatusCode,
		HeaderRequestID: resp.Header.Get("X-Request-ID"),
		Attempts:        attempts,
	}
	if resp.Request != nil {
		e.Method = resp.Request.Method
		if resp.Request.URL != nil {
			e.Path = resp.Request.URL.Path
		}
	}
	if p := parseProblem(resp.Header.Get("Content-Type"), body); p != nil {
		e.Problem = p
		return e
	}
	raw := strings.TrimSpace(string(body))
	if len(raw) > maxRawBody {
		raw = raw[:maxRawBody] + "…"
	}
	e.RawBody = raw
	return e
}

// parseProblem accepts application/problem+json, and application/json when
// the body has the problem shape (a string code and a numeric status).
func parseProblem(contentType string, body []byte) *Problem {
	mt, _, _ := mime.ParseMediaType(contentType)
	if mt != ProblemContentType && mt != "application/json" {
		return nil
	}
	var p Problem
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	if p.Code == "" || p.Status == 0 {
		return nil
	}
	return &p
}

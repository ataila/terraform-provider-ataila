// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func apiErrorFor(t *testing.T, rep reply) *APIError {
	t.Helper()
	s := &scripted{replies: []reply{rep}}
	api, _ := newTestAPI(t, s)
	_, err := api.Whoami(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	return apiErr
}

func TestProblemIsMappedIntoTheDiagnostic(t *testing.T) {
	e := apiErrorFor(t, reply{status: 409, body: `{"type":"urn:ataila:api:problem:conflict","title":"Conflict","status":409,` +
		`"detail":"A customer with this key exists.","code":"customer_key_taken","instance":"/api/v1/whoami","request_id":"abc123"}`})

	if e.Code() != "customer_key_taken" || e.RequestID() != "abc123" || e.Title() != "Conflict" {
		t.Fatalf("code %q request_id %q title %q", e.Code(), e.RequestID(), e.Title())
	}
	if e.IsLicenceRefusal() {
		t.Error("a 409 is not a licence refusal")
	}
	if got := e.Summary(); got != "ATAILA API error: Conflict" {
		t.Errorf("summary = %q", got)
	}
	d := e.Detail()
	for _, want := range []string{
		"A customer with this key exists.",
		"HTTP 409 on GET /api/v1/whoami",
		"code: customer_key_taken",
		"request_id: abc123",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("detail lacks %q:\n%s", want, d)
		}
	}
	if !strings.Contains(e.Error(), "(customer_key_taken)") || !strings.Contains(e.Error(), "request_id abc123") {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestLicenceRefusalIsFinalAndQuotesTheRemedy(t *testing.T) {
	body := `{"type":"urn:ataila:api:problem:licence_locked","title":"Forbidden","status":403,` +
		`"detail":"Install a current licence.","code":"licence_locked","state":"LOCKED","state_reason":"expired",` +
		`"remedy":"Install a current licence on the Licence page.","remedy_url":"/licence","request_id":"lic-1"}`
	s := &scripted{replies: []reply{{status: 403, body: body}, {status: 200, body: metaBody}}}
	api, waits := newTestAPI(t, s)
	_, err := api.Meta(context.Background())
	var e *APIError
	if !errors.As(err, &e) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if s.count() != 1 || len(*waits) != 0 {
		t.Errorf("a licence refusal was retried: %d requests", s.count())
	}
	if !e.IsLicenceRefusal() {
		t.Fatal("not recognised as a licence refusal")
	}
	if got := e.Remedy(); got != "Install a current licence on the Licence page." {
		t.Errorf("remedy = %q", got)
	}
	if got := e.Summary(); got != "Refused by the platform licence (licence_locked)" {
		t.Errorf("summary = %q", got)
	}
	d := e.Detail()
	for _, want := range []string{
		"Remedy: Install a current licence on the Licence page.",
		"Licence state: LOCKED (expired)",
		"does not retry",
		"request_id: lic-1",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("detail lacks %q:\n%s", want, d)
		}
	}
}

func TestForbiddenWithoutLicenceCodeIsNotALicenceRefusal(t *testing.T) {
	e := apiErrorFor(t, reply{status: 403, body: problem(403, "forbidden")})
	if e.IsLicenceRefusal() {
		t.Error("a plain 403 was taken for a licence refusal")
	}
	e = apiErrorFor(t, reply{status: 409, body: problem(409, "licence_locked")})
	if e.IsLicenceRefusal() {
		t.Error("only a 403 can be a licence refusal")
	}
}

func TestValidationErrorsAreListed(t *testing.T) {
	e := apiErrorFor(t, reply{status: 422, body: `{"type":"urn:ataila:api:problem:validation_failed","title":"Unprocessable Content","status":422,` +
		`"detail":"The request does not match the schema.","code":"validation_failed",` +
		`"errors":[{"loc":["body","name"],"msg":"Field required","type":"missing"}]}`})
	if !strings.Contains(e.Detail(), "- body.name: Field required") {
		t.Errorf("detail lacks the field:\n%s", e.Detail())
	}
}

func TestNonProblemBodyIsKeptShort(t *testing.T) {
	html := "<html>" + strings.Repeat("x", 2000) + "</html>"
	s := &scripted{replies: []reply{{status: 500, headers: map[string]string{"Content-Type": "text/html", "X-Request-ID": "hdr-7"}, body: html}}}
	api, _ := newTestAPI(t, s)
	_, err := api.Meta(context.Background())
	var e *APIError
	if !errors.As(err, &e) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if e.Problem != nil {
		t.Fatal("HTML parsed as a problem")
	}
	if e.RequestID() != "hdr-7" {
		t.Errorf("request id from the header = %q", e.RequestID())
	}
	if len(e.RawBody) > maxRawBody+len("…") {
		t.Errorf("raw body kept %d bytes", len(e.RawBody))
	}
	if e.Summary() != "ATAILA API error: Internal Server Error" {
		t.Errorf("summary = %q", e.Summary())
	}
	if !strings.Contains(e.Detail(), "not a problem document") {
		t.Errorf("detail:\n%s", e.Detail())
	}
}

func TestParseProblemNeedsTheShape(t *testing.T) {
	if parseProblem("application/json", []byte(`{"detail":"x"}`)) != nil {
		t.Error("a body without code and status parsed as a problem")
	}
	if parseProblem("text/plain", []byte(problem(400, "bad_request"))) != nil {
		t.Error("text/plain parsed as a problem")
	}
	if p := parseProblem("application/problem+json; charset=utf-8", []byte(problem(400, "bad_request"))); p == nil || p.Code != "bad_request" {
		t.Errorf("problem+json with parameters not parsed: %+v", p)
	}
}

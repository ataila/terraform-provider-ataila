// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// APIPath is where the versioned API lives under a portal's base URL.
const APIPath = "/api/v1"

// DefaultRequestTimeout bounds one HTTP request (each retry gets its own).
const DefaultRequestTimeout = 60 * time.Second

// Config configures an API.
type Config struct {
	// Endpoint is the portal's base URL, for example https://portal.example.com.
	// A trailing /api/v1 is accepted and not doubled.
	Endpoint string
	// Token is an API token (personal or service account).
	Token string
	// CACertPEM, when set, is trusted in addition to the system roots.
	CACertPEM []byte
	// RequestTimeout bounds one HTTP request. Zero means DefaultRequestTimeout.
	RequestTimeout time.Duration
	// UserAgent is sent on every request.
	UserAgent string
	// MaxRetries overrides DefaultMaxRetries when > 0; a negative value
	// disables retries.
	MaxRetries int
	// BackoffBase and BackoffMax override the defaults when > 0.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// HTTPClient replaces the client built from the settings above (tests).
	HTTPClient *http.Client
	// PageSize is the page size of list reads; zero or anything above
	// MaxPageSize means MaxPageSize. Tests use small pages to cross page
	// boundaries cheaply.
	PageSize int
}

// API is the hand-written face of the generated client.
type API struct {
	baseURL      string
	raw          *ClientWithResponses
	transport    *transport
	listPageSize int
}

// UserAgent is the User-Agent this provider sends.
func UserAgent(version string) string {
	return "terraform-provider-ataila/" + version
}

// New validates the configuration and builds an API. It does not call it.
func New(cfg Config) (*API, error) {
	base, err := NormalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("an API token is required")
	}

	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	hc := cfg.HTTPClient
	if hc == nil {
		tlsCfg, err := TLSConfig(cfg.CACertPEM)
		if err != nil {
			return nil, err
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = tlsCfg
		hc = &http.Client{Timeout: timeout, Transport: tr}
	}

	t := &transport{
		hc:          hc,
		token:       strings.TrimSpace(cfg.Token),
		userAgent:   cfg.UserAgent,
		maxRetries:  DefaultMaxRetries,
		backoffBase: DefaultBackoffBase,
		backoffMax:  DefaultBackoffMax,
		sleep:       sleepContext,
		newKey:      NewIdempotencyKey,
	}
	if t.userAgent == "" {
		t.userAgent = UserAgent("dev")
	}
	switch {
	case cfg.MaxRetries > 0:
		t.maxRetries = cfg.MaxRetries
	case cfg.MaxRetries < 0:
		t.maxRetries = 0
	}
	if cfg.BackoffBase > 0 {
		t.backoffBase = cfg.BackoffBase
	}
	if cfg.BackoffMax > 0 {
		t.backoffMax = cfg.BackoffMax
	}

	raw, err := NewClientWithResponses(base, WithHTTPClient(t))
	if err != nil {
		return nil, err
	}
	return &API{baseURL: base, raw: raw, transport: t, listPageSize: cfg.PageSize}, nil
}

// BaseURL is the normalised API URL, ending in /api/v1.
func (a *API) BaseURL() string { return a.baseURL }

// Raw exposes the generated client; every call goes through the same transport.
func (a *API) Raw() *ClientWithResponses { return a.raw }

// Meta reads GET /meta.
func (a *API) Meta(ctx context.Context) (*Meta, error) {
	rsp, err := a.raw.MetaGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /meta", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// Whoami reads GET /whoami.
func (a *API) Whoami(ctx context.Context) (*Whoami, error) {
	rsp, err := a.raw.WhoamiGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /whoami", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// Operation reads GET /operations/{id}.
func (a *API) Operation(ctx context.Context, id string) (*Operation, error) {
	rsp, err := a.raw.OperationsGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /operations/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

func unexpected(op string, resp *http.Response) error {
	ct := ""
	status := 0
	if resp != nil {
		ct = resp.Header.Get("Content-Type")
		status = resp.StatusCode
	}
	return fmt.Errorf("%s: unexpected response (HTTP %d, Content-Type %q)", op, status, ct)
}

// NormalizeEndpoint turns a portal base URL into the API base URL. HTTPS is
// required except for a loopback host, where plain HTTP is allowed for local
// development.
func NormalizeEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", errors.New("the endpoint is empty")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("the endpoint is not a valid URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("the endpoint %q must be an absolute URL such as https://portal.example.com", endpoint)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the endpoint %q must not carry a query or a fragment", endpoint)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("the endpoint %q must use https: the API token would travel in clear text", endpoint)
		}
	default:
		return "", fmt.Errorf("the endpoint %q must use https", endpoint)
	}
	u.User = nil
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, APIPath) {
		path += APIPath
	}
	u.Path = path
	u.RawPath = ""
	return u.String(), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// TLSConfig trusts the system roots plus caPEM when it is not empty.
func TLSConfig(caPEM []byte) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(bytes.TrimSpace(caPEM)) == 0 {
		return cfg, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("the CA certificate holds no PEM-encoded certificate")
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// LoadCACert reads a CA certificate given either as PEM text or as the path
// of a PEM file (the form ATAILA_CA_CERT accepts).
func LoadCACert(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.Contains(value, "-----BEGIN") {
		return []byte(value), nil
	}
	b, err := os.ReadFile(value)
	if err != nil {
		return nil, fmt.Errorf("reading the CA certificate file: %w", err)
	}
	return b, nil
}

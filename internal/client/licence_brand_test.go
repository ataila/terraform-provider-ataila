// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"testing"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

func TestLicenceCalls(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	b3 := acctest.MockBundle(3, acctest.MockInstanceID, "mock-valid")
	l, err := api.PutLicenceBundle(ctx, b3)
	if err != nil || l.State != "ACTIVE" || l.LicenceEpoch == nil || *l.LicenceEpoch != 3 {
		t.Fatalf("install: %+v %v", l, err)
	}
	want, err := BundleDocumentDigest(b3)
	if err != nil || l.DocumentDigest == nil || *l.DocumentDigest != want {
		t.Fatalf("digest %v, want %s (%v)", l.DocumentDigest, want, err)
	}
	// The installed bundle again: 409 stale_epoch naming what is installed.
	_, err = api.PutLicenceBundle(ctx, b3)
	e := apiErr(t, err)
	if e.StatusCode != 409 || e.Code() != CodeStaleEpoch {
		t.Fatalf("re-send: %v", err)
	}
	if d, _ := e.Extra("installed_document_digest"); d != want {
		t.Errorf("installed_document_digest %v", d)
	}
	if a, _ := e.Extra("already_installed"); a != true {
		t.Errorf("already_installed %v", a)
	}
	if n := m.Calls("PUT", "/licence/bundle"); n != 2 {
		t.Errorf("%d PUTs: a 409 must not be retried", n)
	}
	facts, err := api.GetSocketFacts(ctx)
	if err != nil || facts.Total == nil || *facts.Total != 3 || !facts.Chain.Intact {
		t.Fatalf("facts %+v %v", facts, err)
	}
}

func TestBrandCalls(t *testing.T) {
	m, api := mockAPI(t, 1)
	ctx := context.Background()
	b, err := api.GetBrand(ctx)
	if err != nil || b.Version != 1 {
		t.Fatalf("get %+v %v", b, err)
	}
	body := BrandPut{ProductName: "Example Cloud", BrandColor: "#ABCDEF", PageTitle: "Example",
		LogoSize: "regular"}
	b, err = api.PutBrand(ctx, body, 1)
	if err != nil || b.Version != 2 || b.BrandColor != "#abcdef" {
		t.Fatalf("put %+v %v", b, err)
	}
	// The same body again changes nothing and bumps no version.
	if b, err = api.PutBrand(ctx, body, 2); err != nil || b.Version != 2 {
		t.Fatalf("no-op put %+v %v", b, err)
	}
	// A stale version: 412, never retried.
	_, err = api.PutBrand(ctx, body, 1)
	if e := apiErr(t, err); e.StatusCode != 412 || e.Code() != CodeVersionMismatch {
		t.Fatalf("stale put: %v", err)
	}
	if n := m.Calls("PUT", "/brand"); n != 3 {
		t.Errorf("%d PUTs, want 3", n)
	}

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x01\x90\x00\x00\x00\xc8\x08\x06")
	a, created, err := api.UploadBrandAsset(ctx, "logo", png, "logo.png")
	if err != nil || !created || a.Filename != "logo.png" || a.Width == nil || *a.Width != 400 {
		t.Fatalf("upload %+v %v %v", a, created, err)
	}
	again, created, err := api.UploadBrandAsset(ctx, "logo", png, "other.png")
	if err != nil || created || again.Id != a.Id {
		t.Fatalf("second upload %+v %v %v", again, created, err)
	}
	_, _, err = api.UploadBrandAsset(ctx, "favicon", png, "")
	if e := apiErr(t, err); e.Code() != CodeAssetKindConflict {
		t.Fatalf("other kind: %v", err)
	}
	png2 := append(append([]byte{}, png...), 1)
	if _, _, err := api.UploadBrandAsset(ctx, "favicon", png2, ""); err != nil {
		t.Fatal(err)
	}
	all, err := api.ListBrandAssets(ctx, "", "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list (page size 1): %d %v", len(all), err)
	}
	one, err := api.ListBrandAssets(ctx, "logo", a.Sha256)
	if err != nil || len(one) != 1 || one[0].Id != a.Id {
		t.Fatalf("by sha: %v %v", one, err)
	}

	// Without object storage: a final 503, not retried.
	m.SetBrandStorage(false)
	before := m.Calls("POST", "/brand/assets")
	_, _, err = api.UploadBrandAsset(ctx, "logo", append(png2, 2), "")
	if e := apiErr(t, err); !e.IsFinalUnavailable() {
		t.Fatalf("storage off: %v", err)
	}
	if n := m.Calls("POST", "/brand/assets") - before; n != 1 {
		t.Errorf("%d attempts, want 1", n)
	}
}

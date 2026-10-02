// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	testRepo   = "example-org/terraform-provider-ataila"
	testToken  = "test-token-not-a-secret"
	testCommit = "0123456789abcdef0123456789abcdef01234567"
)

// fakeGitHub is the part of the GitHub REST API the publisher uses.
type fakeGitHub struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	tagType  string // "", "commit" or "tag"
	tagSHA   string
	releases []*fakeRelease
	nextID   int64
	writes   int
}

type fakeRelease struct {
	ID     int64
	Tag    string
	Draft  bool
	Body   string
	Assets map[string]fakeAsset
}

type fakeAsset struct {
	ID    int64
	Size  int64
	State string
	Data  []byte
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, nextID: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) json(r *fakeRelease) map[string]any {
	var assets []map[string]any
	for name, a := range r.Assets {
		assets = append(assets, map[string]any{"id": a.ID, "name": name, "size": a.Size, "state": a.State})
	}
	return map[string]any{
		"id": r.ID, "tag_name": r.Tag, "draft": r.Draft, "assets": assets,
		"upload_url": fmt.Sprintf("%s/upload/%d/assets{?name,label}", f.srv.URL, r.ID),
		"html_url":   fmt.Sprintf("https://example.test/releases/%d", r.ID),
	}
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		f.writes++
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func() { reply(http.StatusNotFound, map[string]string{"message": "Not Found"}) }
	base := "/repos/" + testRepo
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == base+"/git/ref/tags/v1.2.3":
		if f.tagType == "" {
			notFound()
			return
		}
		sha := f.tagSHA
		if f.tagType == "tag" {
			sha = "feedfacefeedfacefeedfacefeedfacefeedface"
		}
		reply(200, map[string]any{"object": map[string]string{"sha": sha, "type": f.tagType}})
	case r.Method == "GET" && p == base+"/git/tags/feedfacefeedfacefeedfacefeedfacefeedface":
		reply(200, map[string]any{"object": map[string]string{"sha": f.tagSHA, "type": "commit"}})
	case r.Method == "GET" && p == base+"/releases/tags/v1.2.3":
		for _, rel := range f.releases {
			if rel.Tag == "v1.2.3" && !rel.Draft {
				reply(200, f.json(rel))
				return
			}
		}
		notFound()
	case r.Method == "GET" && p == base+"/releases":
		var list []map[string]any
		for _, rel := range f.releases {
			list = append(list, f.json(rel))
		}
		reply(200, list)
	case r.Method == "POST" && p == base+"/releases":
		var in struct {
			Tag   string `json:"tag_name"`
			Body  string `json:"body"`
			Draft bool   `json:"draft"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !in.Draft {
			f.t.Errorf("a release was created that is not a draft")
		}
		f.nextID++
		rel := &fakeRelease{ID: f.nextID, Tag: in.Tag, Draft: true, Body: in.Body, Assets: map[string]fakeAsset{}}
		f.releases = append(f.releases, rel)
		reply(201, f.json(rel))
	case r.Method == "POST" && strings.HasPrefix(p, "/upload/"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(p, "/upload/"), "/assets"), 10, 64)
		name := r.URL.Query().Get("name")
		data, _ := io.ReadAll(r.Body)
		for _, rel := range f.releases {
			if rel.ID == id {
				if !rel.Draft {
					f.t.Errorf("an asset was uploaded to a published release")
				}
				if _, dup := rel.Assets[name]; dup {
					reply(422, map[string]string{"message": "already_exists"})
					return
				}
				f.nextID++
				rel.Assets[name] = fakeAsset{ID: f.nextID, Size: int64(len(data)), State: "uploaded", Data: data}
				reply(201, map[string]any{"id": f.nextID, "name": name})
				return
			}
		}
		notFound()
	case r.Method == "DELETE" && strings.HasPrefix(p, base+"/releases/assets/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, base+"/releases/assets/"), 10, 64)
		for _, rel := range f.releases {
			for name, a := range rel.Assets {
				if a.ID == id {
					delete(rel.Assets, name)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}
		notFound()
	case r.Method == "PATCH" && strings.HasPrefix(p, base+"/releases/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, base+"/releases/"), 10, 64)
		var in struct {
			Draft bool `json:"draft"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, rel := range f.releases {
			if rel.ID == id {
				rel.Draft = in.Draft
				reply(200, f.json(rel))
				return
			}
		}
		notFound()
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, p)
		notFound()
	}
}

// writeDist writes the release files of 1.2.3 as the release job leaves them.
func writeDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256([]byte(content))
		return hex.EncodeToString(s[:]) + "  " + name + "\n"
	}
	sums := write("terraform-provider-ataila_1.2.3_linux_amd64.zip", "zip one") +
		write("terraform-provider-ataila_1.2.3_windows_amd64.zip", "zip two") +
		write("terraform-provider-ataila_1.2.3_manifest.json", `{"version":1}`)
	write("terraform-provider-ataila_1.2.3_SHA256SUMS", sums)
	write("terraform-provider-ataila_1.2.3_SHA256SUMS.sig", "signature")
	// The mirror job's bundle and its sum, as sha256sum writes it.
	write("terraform-provider-ataila_1.2.3_mirror.zip.sha256", write("terraform-provider-ataila_1.2.3_mirror.zip", "bundle"))
	return dir
}

func publisher(f *fakeGitHub) (*Publisher, *strings.Builder) {
	log := &strings.Builder{}
	return &Publisher{API: f.srv.URL, Repo: testRepo, Token: testToken, Client: f.srv.Client(), Log: log}, log
}

func TestPublishCreatesADraftUploadsAndPublishes(t *testing.T) {
	for _, tagType := range []string{"commit", "tag"} {
		t.Run(tagType, func(t *testing.T) {
			f := newFakeGitHub(t)
			f.tagType, f.tagSHA = tagType, testCommit
			p, _ := publisher(f)
			if err := p.Publish("v1.2.3", testCommit, writeDist(t), "notes\n"); err != nil {
				t.Fatal(err)
			}
			if len(f.releases) != 1 || f.releases[0].Draft || f.releases[0].Body != "notes\n" {
				t.Fatalf("releases: %+v", f.releases)
			}
			var names []string
			for n := range f.releases[0].Assets {
				names = append(names, n)
			}
			if len(names) != 7 {
				t.Fatalf("assets %v, want the 2 archives, the sums, their signature, the manifest, and the "+
					"mirror bundle with its sum", names)
			}
			for _, n := range []string{"terraform-provider-ataila_1.2.3_mirror.zip", "terraform-provider-ataila_1.2.3_mirror.zip.sha256"} {
				if _, ok := f.releases[0].Assets[n]; !ok {
					t.Errorf("%s was not attached", n)
				}
			}
			// A second run finds the release published with these files and writes nothing.
			before := f.writes
			if err := p.Publish("v1.2.3", testCommit, writeDist(t), "notes\n"); err != nil {
				t.Fatal(err)
			}
			if f.writes != before {
				t.Errorf("a run on a published release wrote %d times", f.writes-before)
			}
		})
	}
}

func TestPublishReusesTheDraftAnEarlierRunLeft(t *testing.T) {
	f := newFakeGitHub(t)
	f.tagType, f.tagSHA = "commit", testCommit
	f.releases = []*fakeRelease{{ID: 7, Tag: "v1.2.3", Draft: true, Assets: map[string]fakeAsset{
		"terraform-provider-ataila_1.2.3_linux_amd64.zip":   {ID: 8, Size: int64(len("zip one")), State: "uploaded"},
		"terraform-provider-ataila_1.2.3_windows_amd64.zip": {ID: 9, Size: 3, State: "starter"}, // broken upload
	}}}
	p, log := publisher(f)
	if err := p.Publish("v1.2.3", testCommit, writeDist(t), "notes\n"); err != nil {
		t.Fatal(err)
	}
	if len(f.releases) != 1 || f.releases[0].Draft || len(f.releases[0].Assets) != 7 {
		t.Fatalf("releases: %+v", f.releases[0])
	}
	if !strings.Contains(log.String(), "reusing the draft") || strings.Contains(log.String(), "uploaded terraform-provider-ataila_1.2.3_linux_amd64.zip") {
		t.Errorf("log:\n%s", log)
	}
	if got := f.releases[0].Assets["terraform-provider-ataila_1.2.3_windows_amd64.zip"]; string(got.Data) != "zip two" {
		t.Errorf("the broken upload was not replaced: %+v", got)
	}
}

func TestPublishRefuses(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(f *fakeGitHub, dist string)
		tag     string
		wantErr string
	}{
		{"tag not on GitHub", func(f *fakeGitHub, _ string) { f.tagType = "" }, "v1.2.3", "is not on GitHub: the mirror:github job"},
		{"tag names another commit", func(f *fakeGitHub, _ string) { f.tagSHA = strings.Repeat("ab", 20) }, "v1.2.3", "not the commit"},
		{"not a release tag", func(*fakeGitHub, string) {}, "v1.2.3-rc1", "is not a release tag"},
		// A dry run against a fake 0.x tag: refused before any request.
		{"a 0.x tag", func(*fakeGitHub, string) {}, "v0.9.0", "below 1.0.0"},
		{"the oldest 0.x tag", func(*fakeGitHub, string) {}, "v0.0.1", "below 1.0.0"},
		{"no mirror bundle", func(_ *fakeGitHub, dist string) {
			_ = os.Remove(filepath.Join(dist, "terraform-provider-ataila_1.2.3_mirror.zip"))
		}, "v1.2.3", "the mirror job did not run"},
		{"a mirror bundle that does not match its sum", func(_ *fakeGitHub, dist string) {
			_ = os.WriteFile(filepath.Join(dist, "terraform-provider-ataila_1.2.3_mirror.zip"), []byte("changed"), 0o644)
		}, "v1.2.3", "does not match its .sha256"},
		{"an archive is missing", func(_ *fakeGitHub, dist string) {
			_ = os.Remove(filepath.Join(dist, "terraform-provider-ataila_1.2.3_windows_amd64.zip"))
		}, "v1.2.3", "which is not in"},
		{"an archive does not match its sum", func(_ *fakeGitHub, dist string) {
			_ = os.WriteFile(filepath.Join(dist, "terraform-provider-ataila_1.2.3_linux_amd64.zip"), []byte("changed"), 0o644)
		}, "v1.2.3", "does not match its sum"},
		{"no signature", func(_ *fakeGitHub, dist string) {
			_ = os.Remove(filepath.Join(dist, "terraform-provider-ataila_1.2.3_SHA256SUMS.sig"))
		}, "v1.2.3", "no terraform-provider-ataila_1.2.3_SHA256SUMS.sig"},
		{"published with other files", func(f *fakeGitHub, _ string) {
			f.releases = []*fakeRelease{{ID: 7, Tag: "v1.2.3", Assets: map[string]fakeAsset{
				"terraform-provider-ataila_1.2.3_linux_amd64.zip": {ID: 8, Size: 1, State: "uploaded"},
			}}}
		}, "v1.2.3", "already published on GitHub without"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGitHub(t)
			f.tagType, f.tagSHA = "commit", testCommit
			dist := writeDist(t)
			c.setup(f, dist)
			p, _ := publisher(f)
			err := p.Publish(c.tag, testCommit, dist, "notes\n")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %v, want one containing %q", err, c.wantErr)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Error("the error holds the token")
			}
			if f.writes != 0 {
				t.Errorf("%d writes before the refusal", f.writes)
			}
		})
	}
}

func TestToken(t *testing.T) {
	if _, err := Token(""); err == nil {
		t.Error("an empty variable gave a token")
	}
	if v, err := Token(" value-token \n"); err != nil || v != "value-token" {
		t.Errorf("value: %q, %v", v, err)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := Token(file); err != nil || v != "file-token" {
		t.Errorf("file: %q, %v", v, err)
	}
}

func TestNotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	content := "# Changelog\n\n## 1.2.3 (2026-01-01)\n\n- one\n- two\n\n## 1.2.2 (2025-12-01)\n\n- old\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Notes(path, "1.2.3"); err != nil || got != "- one\n- two\n" {
		t.Errorf("Notes: %q, %v", got, err)
	}
	if _, err := Notes(path, "9.9.9"); err == nil {
		t.Error("a version without a section gave notes")
	}
}

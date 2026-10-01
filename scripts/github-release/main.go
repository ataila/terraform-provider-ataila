// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

// Command github-release publishes the signed release files that the release
// CI job built, as the GitHub release of a tag on the public repository, where
// the OpenTofu and Terraform registries read them. It builds nothing: the
// files are the ones GitLab built and signed once.
//
//	go run ./scripts/github-release -repo ataila/terraform-provider-ataila \
//	  -tag v0.7.0 -commit "$CI_COMMIT_SHA" -dir dist
//
// The token is read from GITHUB_RELEASE_TOKEN (its value, or the path of a
// file holding it): a fine-grained token with contents read and write on that
// repository only. It is never printed.
//
// The steps, each safe to run again after a failure:
//  1. The release files are complete: every archive SHA256SUMS lists is there
//     and matches, and so are SHA256SUMS.sig and the registry manifest.
//  2. The tag is on GitHub (the mirror:github job pushes it) and names the
//     commit that was built.
//  3. A published release of the tag that already holds these files is left
//     as it is; one that holds other files is an error (never changed here).
//  4. Otherwise a draft is created (or the one an earlier run left is
//     reused), the files are uploaded, and the draft is published, so the
//     registries never see a release without its files.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const tokenEnv = "GITHUB_RELEASE_TOKEN"

func main() {
	repo := flag.String("repo", "ataila/terraform-provider-ataila", "the GitHub repository, owner/name")
	tag := flag.String("tag", "", "the release tag, vX.Y.Z")
	commit := flag.String("commit", "", "the commit the tag must name")
	dir := flag.String("dir", "dist", "the directory holding the release files")
	changelog := flag.String("changelog", "CHANGELOG.md", "the changelog the release notes are taken from")
	api := flag.String("api", "https://api.github.com", "the GitHub API")
	flag.Parse()
	token, err := Token(os.Getenv(tokenEnv))
	if err == nil {
		var notes string
		notes, err = Notes(*changelog, strings.TrimPrefix(*tag, "v"))
		if err == nil {
			p := &Publisher{API: *api, Repo: *repo, Token: token, Client: &http.Client{Timeout: 10 * time.Minute}}
			err = p.Publish(*tag, *commit, *dir, notes)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "github-release:", err)
		os.Exit(1)
	}
}

// Token returns the token held by the variable: its value, or, for a GitLab
// File variable, the content of the file it names.
func Token(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("%s is not set", tokenEnv)
	}
	if fi, err := os.Stat(v); err == nil && fi.Mode().IsRegular() {
		b, err := os.ReadFile(v)
		if err != nil {
			return "", fmt.Errorf("%s names a file that cannot be read", tokenEnv)
		}
		v = strings.TrimSpace(string(b))
	}
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return "", fmt.Errorf("%s holds no usable token", tokenEnv)
	}
	return v, nil
}

// Notes returns the CHANGELOG section of the version, without its heading.
func Notes(path, version string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var out []string
	in := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			in = line == "## "+version || strings.HasPrefix(line, "## "+version+" ")
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	if !in && len(out) == 0 {
		return "", fmt.Errorf("%s has no section for %s", path, version)
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n", nil
}

// Asset is one release file.
type Asset struct {
	Name string
	Path string
	Size int64
}

// Files returns the release files of the version in dir, checked: every
// archive SHA256SUMS lists is there with that sum, the manifest is listed and
// matches, and the detached signature of the sums is there.
func Files(dir, version string) ([]Asset, error) {
	prefix := "terraform-provider-ataila_" + version + "_"
	sumsName := prefix + "SHA256SUMS"
	raw, err := os.ReadFile(filepath.Join(dir, sumsName))
	if err != nil {
		return nil, fmt.Errorf("no %s in %s: the release job did not finish", sumsName, dir)
	}
	archive := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `[a-z0-9]+_[a-z0-9]+\.zip$`)
	names := []string{sumsName, sumsName + ".sig"}
	manifest := false
	zips := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("%s: unreadable line %q", sumsName, line)
		}
		name := strings.TrimPrefix(f[1], "*")
		switch {
		case archive.MatchString(name):
			zips++
		case name == prefix+"manifest.json":
			manifest = true
		default:
			return nil, fmt.Errorf("%s lists %s, which is no release file", sumsName, name)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s lists %s, which is not in %s", sumsName, name, dir)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != strings.ToLower(f[0]) {
			return nil, fmt.Errorf("%s does not match its sum in %s", name, sumsName)
		}
		names = append(names, name)
	}
	if zips == 0 || !manifest {
		return nil, fmt.Errorf("%s lists %d archives and %s manifest; the registries need both", sumsName, zips,
			map[bool]string{true: "a", false: "no"}[manifest])
	}
	sort.Strings(names)
	var assets []Asset
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			return nil, fmt.Errorf("no %s in %s", n, dir)
		}
		assets = append(assets, Asset{Name: n, Path: filepath.Join(dir, n), Size: fi.Size()})
	}
	return assets, nil
}

// Publisher talks to the GitHub REST API.
type Publisher struct {
	API    string
	Repo   string
	Token  string
	Client *http.Client
	Log    io.Writer
}

type release struct {
	ID        int64  `json:"id"`
	TagName   string `json:"tag_name"`
	Draft     bool   `json:"draft"`
	UploadURL string `json:"upload_url"`
	HTMLURL   string `json:"html_url"`
	Assets    []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		State string `json:"state"`
	} `json:"assets"`
}

var semverTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Publish runs the steps in the package comment.
func (p *Publisher) Publish(tag, commit, dir, notes string) error {
	if !semverTag.MatchString(tag) {
		return fmt.Errorf("%q is not a release tag (vX.Y.Z)", tag)
	}
	if commit == "" {
		return errors.New("-commit is required")
	}
	assets, err := Files(dir, strings.TrimPrefix(tag, "v"))
	if err != nil {
		return err
	}
	if err := p.checkTag(tag, commit); err != nil {
		return err
	}

	var published release
	switch status, err := p.do("GET", p.API+"/repos/"+p.Repo+"/releases/tags/"+url.PathEscape(tag), nil, "", &published); {
	case err == nil:
		if missing := missingAssets(published, assets); len(missing) > 0 {
			return fmt.Errorf("%s is already published on GitHub without %s: change it by hand, never from CI",
				tag, strings.Join(missing, ", "))
		}
		p.logf("%s is already published with these %d files: %s\n", tag, len(assets), published.HTMLURL)
		return nil
	case status != http.StatusNotFound:
		return err
	}

	draft, err := p.findDraft(tag)
	if err != nil {
		return err
	}
	if draft == nil {
		body := map[string]any{"tag_name": tag, "name": tag, "body": notes, "draft": true}
		draft = &release{}
		if _, err := p.do("POST", p.API+"/repos/"+p.Repo+"/releases", body, "application/json", draft); err != nil {
			return fmt.Errorf("creating the draft release: %w", err)
		}
		p.logf("created a draft release of %s\n", tag)
	} else {
		p.logf("reusing the draft release of %s an earlier run left\n", tag)
	}

	have := map[string]int64{}
	for _, a := range draft.Assets {
		if a.State == "uploaded" {
			have[a.Name] = a.Size
		} else if _, err := p.do("DELETE", fmt.Sprintf("%s/repos/%s/releases/assets/%d", p.API, p.Repo, a.ID), nil, "", nil); err != nil {
			return fmt.Errorf("removing the incomplete upload %s: %w", a.Name, err)
		}
	}
	upload := strings.SplitN(draft.UploadURL, "{", 2)[0]
	if upload == "" {
		return errors.New("GitHub returned no upload URL for the draft")
	}
	for _, a := range assets {
		if size, ok := have[a.Name]; ok && size == a.Size {
			continue
		} else if ok {
			return fmt.Errorf("the draft of %s holds another %s; delete the draft on GitHub and run again", tag, a.Name)
		}
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return err
		}
		ctype := "application/octet-stream"
		switch {
		case strings.HasSuffix(a.Name, ".zip"):
			ctype = "application/zip"
		case strings.HasSuffix(a.Name, ".json"):
			ctype = "application/json"
		}
		if _, err := p.do("POST", upload+"?name="+url.QueryEscape(a.Name), data, ctype, nil); err != nil {
			return fmt.Errorf("uploading %s: %w", a.Name, err)
		}
		p.logf("uploaded %s (%d bytes)\n", a.Name, a.Size)
	}

	var done release
	if _, err := p.do("PATCH", fmt.Sprintf("%s/repos/%s/releases/%d", p.API, p.Repo, draft.ID),
		map[string]any{"draft": false}, "application/json", &done); err != nil {
		return fmt.Errorf("publishing the draft: %w", err)
	}
	p.logf("published %s with %d files: %s\n", tag, len(assets), done.HTMLURL)
	return nil
}

// checkTag fails unless the tag is on GitHub and names the commit.
func (p *Publisher) checkTag(tag, commit string) error {
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	status, err := p.do("GET", p.API+"/repos/"+p.Repo+"/git/ref/tags/"+url.PathEscape(tag), nil, "", &ref)
	if status == http.StatusNotFound {
		return fmt.Errorf("%s is not on GitHub: the mirror:github job pushes it (it runs when GITHUB_MIRROR_TOKEN is set)", tag)
	}
	if err != nil {
		return fmt.Errorf("reading the tag on GitHub: %w", err)
	}
	sha := ref.Object.SHA
	for i := 0; ref.Object.Type == "tag" && i < 4; i++ { // an annotated tag names a tag object
		if _, err := p.do("GET", p.API+"/repos/"+p.Repo+"/git/tags/"+sha, nil, "", &ref); err != nil {
			return fmt.Errorf("reading the annotated tag on GitHub: %w", err)
		}
		sha = ref.Object.SHA
	}
	if ref.Object.Type != "commit" || !strings.EqualFold(sha, commit) {
		return fmt.Errorf("%s on GitHub names %s %s, not the commit %s that was built", tag, ref.Object.Type, short(sha), short(commit))
	}
	return nil
}

// findDraft returns the draft release of the tag an earlier run left, if any.
func (p *Publisher) findDraft(tag string) (*release, error) {
	for page := 1; page <= 10; page++ {
		var list []release
		if _, err := p.do("GET", fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", p.API, p.Repo, page), nil, "", &list); err != nil {
			return nil, fmt.Errorf("listing the releases: %w", err)
		}
		for i := range list {
			if list[i].Draft && list[i].TagName == tag {
				return &list[i], nil
			}
		}
		if len(list) < 100 {
			break
		}
	}
	return nil, nil
}

func missingAssets(r release, assets []Asset) []string {
	have := map[string]int64{}
	for _, a := range r.Assets {
		if a.State == "uploaded" {
			have[a.Name] = a.Size
		}
	}
	var missing []string
	for _, a := range assets {
		if size, ok := have[a.Name]; !ok || size != a.Size {
			missing = append(missing, a.Name)
		}
	}
	return missing
}

// do sends one request. body is JSON-encoded unless it is []byte. The status
// is returned with any error; an error never holds the token.
func (p *Publisher) do(method, u string, body any, ctype string, out any) (int, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "terraform-provider-ataila-release")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the url.Error repeats the whole URL
		}
		return 0, fmt.Errorf("%s %s: %v", method, redact(u), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, redact(u), resp.StatusCode, e.Message)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: unreadable answer: %v", method, redact(u), err)
		}
	}
	return resp.StatusCode, nil
}

func (p *Publisher) logf(format string, a ...any) {
	w := p.Log
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintf(w, format, a...)
}

// redact drops the query of a URL from messages.
func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

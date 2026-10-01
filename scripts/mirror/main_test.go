// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildLaysOutBothRegistryAddresses(t *testing.T) {
	dist := t.TempDir()
	for _, name := range []string{
		"terraform-provider-ataila_1.2.3_linux_amd64",
		"terraform-provider-ataila_1.2.3_windows_amd64.exe",
		"terraform-provider-ataila_1.2.2_linux_amd64", // another version: left out
		"terraform-provider-ataila_1.2.3_SHA256SUMS",  // not a binary: left out
	} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "bundle.zip")
	n, err := Build("1.2.3", dist, out)
	if err != nil || n != 2 {
		t.Fatalf("Build: %d, %v", n, err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]os.FileMode{}
	for _, f := range zr.File {
		got[f.Name] = f.Mode()
	}
	for _, host := range Hosts {
		for _, p := range []string{
			host + "/ataila/ataila/1.2.3/linux_amd64/terraform-provider-ataila_v1.2.3",
			host + "/ataila/ataila/1.2.3/windows_amd64/terraform-provider-ataila_v1.2.3.exe",
		} {
			mode, ok := got[p]
			if !ok {
				t.Errorf("missing %s", p)
			} else if mode.Perm()&0o100 == 0 {
				t.Errorf("%s is not executable (%v)", p, mode)
			}
		}
	}
	if _, ok := got["README.md"]; !ok {
		t.Error("no README.md")
	}
	if len(got) != 2*len(Hosts)+2 {
		t.Errorf("%d entries: %v", len(got), got)
	}
	if !strings.Contains(Readme("1.2.3", []string{"linux_amd64"}), "filesystem_mirror") {
		t.Error("the README does not configure a filesystem_mirror")
	}
	if _, err := Build("9.9.9", dist, out); err == nil {
		t.Error("a version without binaries must fail")
	}
}

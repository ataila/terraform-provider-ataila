// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

// Command mirror packs the provider binaries of one release into an
// air-gapped mirror bundle: one archive holding the same binaries under both
// registry addresses, in the unpacked filesystem_mirror layout both CLIs read
//
//	<hostname>/ataila/ataila/<version>/<os>_<arch>/terraform-provider-ataila_v<version>[.exe]
//
// plus a README with the CLI configuration and a SHA256SUMS of the binaries.
//
//	go run ./scripts/mirror -version 0.7.0 -dist dist -out dist/terraform-provider-ataila_0.7.0_mirror.zip
//
// The binaries are read from -dist as the build job names them:
// terraform-provider-ataila_<version>_<os>_<arch>[.exe].
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Hosts are the registry addresses the bundle serves the provider under.
var Hosts = []string{"registry.opentofu.org", "registry.terraform.io"}

const (
	namespace = "ataila"
	typeName  = "ataila"
)

func main() {
	version := flag.String("version", "", "the release version, without the v")
	dist := flag.String("dist", "dist", "the directory holding the built binaries")
	out := flag.String("out", "", "the archive to write")
	flag.Parse()
	if *version == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "mirror: -version and -out are required")
		os.Exit(2)
	}
	n, err := Build(*version, *dist, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mirror:", err)
		os.Exit(1)
	}
	fmt.Printf("mirror: %s holds %d platforms under %d registry addresses\n", *out, n, len(Hosts))
}

// Build writes the bundle and returns how many platforms it holds.
func Build(version, dist, out string) (int, error) {
	rx := regexp.MustCompile(`^terraform-provider-ataila_` + regexp.QuoteMeta(version) +
		`_([a-z0-9]+)_([a-z0-9]+)(\.exe)?$`)
	entries, err := os.ReadDir(dist)
	if err != nil {
		return 0, err
	}
	type bin struct{ path, target, ext string }
	var bins []bin
	for _, e := range entries {
		m := rx.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		bins = append(bins, bin{filepath.Join(dist, e.Name()), m[1] + "_" + m[2], m[3]})
	}
	if len(bins) == 0 {
		return 0, fmt.Errorf("no binaries of version %s in %s", version, dist)
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].target < bins[j].target })

	f, err := os.Create(out)
	if err != nil {
		return 0, err
	}
	zw := zip.NewWriter(f)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // reproducible archives
	var sums []string
	for _, b := range bins {
		data, err := os.ReadFile(b.path)
		if err != nil {
			return 0, errors.Join(err, zw.Close(), f.Close())
		}
		sum := sha256.Sum256(data)
		name := "terraform-provider-ataila_v" + version + b.ext
		for _, host := range Hosts {
			p := strings.Join([]string{host, namespace, typeName, version, b.target, name}, "/")
			if err := addFile(zw, p, data, 0o755, stamp); err != nil {
				return 0, errors.Join(err, zw.Close(), f.Close())
			}
			sums = append(sums, hex.EncodeToString(sum[:])+"  "+p)
		}
	}
	sort.Strings(sums)
	if err := addFile(zw, "SHA256SUMS", []byte(strings.Join(sums, "\n")+"\n"), 0o644, stamp); err != nil {
		return 0, errors.Join(err, zw.Close(), f.Close())
	}
	var platforms []string
	for _, b := range bins {
		platforms = append(platforms, b.target)
	}
	readme := Readme(version, platforms)
	if err := addFile(zw, "README.md", []byte(readme), 0o644, stamp); err != nil {
		return 0, errors.Join(err, zw.Close(), f.Close())
	}
	if err := zw.Close(); err != nil {
		return 0, errors.Join(err, f.Close())
	}
	return len(bins), f.Close()
}

func addFile(zw *zip.Writer, name string, data []byte, mode os.FileMode, stamp time.Time) error {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp}
	h.SetMode(mode)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// Readme is the bundle's README: where to unpack it and how to point each
// CLI at it.
func Readme(version string, targets []string) string {
	return fmt.Sprintf(`# terraform-provider-ataila %[1]s: air-gapped mirror bundle

This archive holds the provider's binaries for %[2]s, under both registry
addresses, in the filesystem mirror layout OpenTofu and Terraform read:

    registry.opentofu.org/ataila/ataila/%[1]s/<os>_<arch>/
    registry.terraform.io/ataila/ataila/%[1]s/<os>_<arch>/

SHA256SUMS lists the sha256 of every binary.

## Install

Unpack the archive into a directory, for example /opt/terraform/mirror
(%%APPDATA%%\terraform.d\mirror on Windows works too), then point the CLI at it.

OpenTofu, in ~/.tofurc (%%APPDATA%%\tofu.rc on Windows):

    provider_installation {
      filesystem_mirror {
        path    = "/opt/terraform/mirror"
        include = ["registry.opentofu.org/ataila/ataila"]
      }
      direct {
        exclude = ["registry.opentofu.org/ataila/ataila"]
      }
    }

Terraform, in ~/.terraformrc (%%APPDATA%%\terraform.rc on Windows):

    provider_installation {
      filesystem_mirror {
        path    = "/opt/terraform/mirror"
        include = ["registry.terraform.io/ataila/ataila"]
      }
      direct {
        exclude = ["registry.terraform.io/ataila/ataila"]
      }
    }

Without any network at all, leave the direct block out. The configuration
keeps source = "ataila/ataila"; tofu init or terraform init then installs
%[1]s from the mirror and records its checksums in .terraform.lock.hcl.
`, version, strings.Join(targets, ", "))
}

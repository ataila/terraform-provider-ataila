// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	// SupportedAPIMajor is the only API major version this provider speaks.
	SupportedAPIMajor = "v1"
	// MinimumAPIVersion is the oldest API this provider release works with.
	// Raise it when the provider starts to rely on something added later in v1.
	MinimumAPIVersion = "1.0.0"
)

// VersionError explains why the provider refuses an API.
type VersionError struct {
	Got     string
	Minimum string
	Reason  string
}

func (e *VersionError) Error() string { return e.Reason }

// CheckAPIVersion refuses an API whose major version is not 1, or which is
// older than MinimumAPIVersion.
func CheckAPIVersion(apiVersion string) error {
	return checkAPIVersion(apiVersion, MinimumAPIVersion)
}

func checkAPIVersion(got, minimum string) error {
	v := "v" + strings.TrimPrefix(strings.TrimSpace(got), "v")
	if !semver.IsValid(v) {
		return &VersionError{Got: got, Minimum: minimum, Reason: fmt.Sprintf(
			"the platform reports API version %q, which is not a semantic version", got)}
	}
	if semver.Major(v) != SupportedAPIMajor {
		return &VersionError{Got: got, Minimum: minimum, Reason: fmt.Sprintf(
			"the platform serves API %s; this provider release speaks API %s only. "+
				"Use a provider release made for API %s", got, SupportedAPIMajor, semver.Major(v))}
	}
	if semver.Compare(v, "v"+minimum) < 0 {
		return &VersionError{Got: got, Minimum: minimum, Reason: fmt.Sprintf(
			"the platform serves API %s, older than %s, the minimum this provider release needs. "+
				"Upgrade the platform, or pin an older provider release", got, minimum)}
	}
	return nil
}

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
	// MinimumPlatformVersion is the oldest platform release this provider
	// release works with: the one whose contract it vendors. Every platform
	// since 1.0.155 serves API 1.0.0, so the API version alone cannot tell an
	// older platform apart: before 1.0.176 the API named its members after
	// internal systems, and before 1.0.187 it declared neither the
	// Idempotency-Key parameter nor the Location header the provider relies on.
	MinimumPlatformVersion = "1.0.187"
)

// CheckPlatformVersion refuses a platform release older than
// MinimumPlatformVersion, or one that does not report a semantic version.
func CheckPlatformVersion(platformVersion string) error {
	return checkPlatformVersion(platformVersion, MinimumPlatformVersion)
}

func checkPlatformVersion(got, minimum string) error {
	v := "v" + strings.TrimPrefix(strings.TrimSpace(got), "v")
	if !semver.IsValid(v) {
		return &VersionError{Got: got, Minimum: minimum, Reason: fmt.Sprintf(
			"the platform reports release %q, which is not a semantic version, so this provider release cannot "+
				"tell whether it is %s or later, the oldest it works with", got, minimum)}
	}
	if semver.Compare(v, "v"+minimum) < 0 {
		return &VersionError{Got: got, Minimum: minimum, Reason: fmt.Sprintf(
			"the platform is release %s, older than %s, the oldest this provider release works with. Its API "+
				"version is the same, but before 1.0.176 the API named the attributes differently (sso_*, "+
				"secret_*, nas_*, enable_object_storage …), and before 1.0.187 it did not declare the "+
				"Idempotency-Key parameter and the Location header this provider relies on: reads would come "+
				"back empty and writes would be refused. Upgrade the platform to %s or later",
			strings.TrimPrefix(strings.TrimSpace(got), "v"), minimum, minimum)}
	}
	return nil
}

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

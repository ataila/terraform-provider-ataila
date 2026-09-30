// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import "time"

// SetProvisioningPollInterval shortens the provisioning poll for a test and
// returns a function that restores it.
func SetProvisioningPollInterval(d time.Duration) func() {
	prev := provisioningPollInterval
	provisioningPollInterval = d
	return func() { provisioningPollInterval = prev }
}

// HasIPv4 is hasIPv4 for the tests.
var HasIPv4 = hasIPv4

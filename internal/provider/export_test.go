// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import "time"

// SetPollInterval shortens the operation poll for a test and
// returns a function that restores it.
func SetPollInterval(d time.Duration) func() {
	prev := operationPollInterval
	operationPollInterval = d
	return func() { operationPollInterval = prev }
}

// HasIPv4 is hasIPv4 for the tests.
var HasIPv4 = hasIPv4

// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

// Describe returns the contract's description of a property, from a schema
// of components.schemas through nested properties: Describe("AiNode",
// "cluster", "name") is the description of the cluster member's name. It is
// "" when the contract has none, or names no such property.
func Describe(schema string, path ...string) string {
	if len(path) == 0 {
		return ""
	}
	for _, p := range path[:len(path)-1] {
		next, ok := contractRefs[schema+"."+p]
		if !ok {
			return ""
		}
		schema = next
	}
	return contractDescriptions[schema+"."+path[len(path)-1]]
}

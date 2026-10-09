//go:build !integration

package googledrive

import "testing"

// postgresStoreCases: without the integration tag the Drive tests run against
// the in-memory registry only (make test-unit; no Docker).
func postgresStoreCases(*testing.T) []storeCase { return nil }

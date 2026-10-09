package valkeystore

// White-box: evaluate is the pure rule set behind CheckDurability. The
// store's behaviour against a real Valkey is tested in
// test/integration/valkeystore.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluate_Rules(t *testing.T) {
	t.Parallel()
	good := map[string]string{"appendonly": "yes", "appendfsync": "everysec", "maxmemory-policy": "noeviction"}
	assert.NoError(t, evaluate(good))

	for name, bad := range map[string]map[string]string{
		"no AOF":            {"appendonly": "no", "appendfsync": "everysec", "maxmemory-policy": "noeviction"},
		"fsync never":       {"appendonly": "yes", "appendfsync": "no", "maxmemory-policy": "noeviction"},
		"volatile eviction": {"appendonly": "yes", "appendfsync": "always", "maxmemory-policy": "volatile-lru"},
		"allkeys eviction":  {"appendonly": "yes", "appendfsync": "always", "maxmemory-policy": "allkeys-lfu"},
	} {
		var derr *DurabilityError
		require.ErrorAsf(t, evaluate(bad), &derr, name)
		assert.Len(t, derr.Problems, 1, name)
	}
}

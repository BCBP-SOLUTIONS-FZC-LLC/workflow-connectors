package valkeystore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// hashReply is a RESP2 HGETALL reply holding fields.
func hashReply(fields map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", 2*len(fields))
	for k, v := range fields {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n$%d\r\n%s\r\n", len(k), k, len(v), v)
	}
	return b.String()
}

func refHash(id, tenant string) map[string]string {
	return map[string]string{
		"reference_id": id, "tenant_id": tenant, "bucket": "b", "object_key": tenant + "/x",
		"content_type": "application/pdf", "size": "1", "sha256": strings.Repeat("a", 64),
		"created_at": "1700000000000", "updated_at": "1700000000000",
	}
}

// Get reads the hash back field by field: another tenant's ref, or a hash
// stored under another ref's key, is not found (its existence is not
// revealed); a hash this store cannot have written is an integrity
// violation, never a usable ref.
func TestGet_HashChecks(t *testing.T) {
	t.Parallel()
	id := docref.NewID()
	other := docref.NewID()
	malformed := refHash(id, "t")
	malformed["size"] = "one"
	otherMalformed := refHash(id, "t2")
	otherMalformed["size"] = "one"

	for name, tc := range map[string]struct {
		fields    map[string]string
		found     bool
		integrity bool
	}{
		"own ref":          {fields: refHash(id, "t"), found: true},
		"other tenant":     {fields: refHash(id, "t2")},
		"other ref's hash": {fields: refHash(other, "t")},
		"malformed size":   {fields: malformed, integrity: true},
		// The tenant is compared before anything else is decoded: another
		// tenant's corrupt ref is not found too, not an integrity
		// violation that would reveal it exists.
		"other tenant, malformed": {fields: otherMalformed},
	} {
		c := fakeClient(t, func(args []string) string {
			if args[0] == "HGETALL" {
				return hashReply(tc.fields)
			}
			return "-ERR unknown command\r\n"
		})
		ref, found, err := valkeystore.New(c, valkeystore.Options{}).Get(context.Background(), "t", id)
		if tc.integrity {
			require.ErrorIs(t, err, docref.ErrIntegrityViolation, name)
			assert.False(t, found, name)
			continue
		}
		require.NoError(t, err, name)
		assert.Equal(t, tc.found, found, name)
		if tc.found {
			assert.Equal(t, id, ref.ID, name)
			assert.Equal(t, int64(1), ref.Size, name)
			assert.Equal(t, int64(1700000000000), ref.CreatedAt.UnixMilli(), name)
		}
	}
}

// The create script's {0} (the key holds a different ref) is ErrExists:
// a ref is never overwritten.
func TestPut_ExistingDifferentRef_IsErrExists(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func(args []string) string {
		switch args[0] {
		case "EVAL", "EVALSHA":
			return "*1\r\n:0\r\n"
		case "WAITAOF":
			return "*2\r\n:1\r\n:0\r\n"
		}
		return "-ERR unknown command\r\n"
	})
	_, err := valkeystore.New(c, valkeystore.Options{}).Put(context.Background(), sampleRef())
	require.ErrorIs(t, err, docref.ErrExists)
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
}

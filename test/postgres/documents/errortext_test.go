//go:build integration

package sqlstore_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents/sqlstore"
)

// A failure reason that embeds a whole response body — megabytes, a NUL,
// invalid UTF-8 — is stored bounded and valid, on the row and in its audit
// record; PostgreSQL would refuse the NUL and the invalid bytes outright.
func TestPostgres_Fail_StoresABoundedValidReason(t *testing.T) {
	t.Parallel()
	pool := postgres(t)
	if pool == nil {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	ctx, identity := context.Background(), newIdentity()
	store := sqlstore.New(pool)

	doc, _, err := store.Claim(ctx, identity, "a1", time.Minute)
	require.NoError(t, err)
	reason := "googleapi: Error 500: \x00" + strings.Repeat("<html>\xff", 200_000)
	failed, err := store.Fail(ctx, doc.ID, "a1", reason)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(failed.LastError), 1024)
	assert.True(t, utf8.ValidString(failed.LastError))
	assert.True(t, strings.HasPrefix(failed.LastError, "googleapi: Error 500: <html>"))

	var audited string
	require.NoError(t, pool.WithConn(ctx, func(ctx context.Context, conn *pgcommon.Conn) error {
		return conn.QueryRow(ctx, `SELECT error FROM connector_document_attempts WHERE document_id = $1::uuid`, doc.ID).Scan(&audited)
	}))
	assert.Equal(t, failed.LastError, audited)
}

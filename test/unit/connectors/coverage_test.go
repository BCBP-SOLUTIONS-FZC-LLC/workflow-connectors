package connectors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestIsRetryable_OnlyTransient(t *testing.T) {
	t.Parallel()
	assert.True(t, connectors.IsRetryable(shared.Transient("http 503", errors.New("x"))))
	assert.False(t, connectors.IsRetryable(shared.Permanent("http 404", errors.New("x"))))
	assert.False(t, connectors.IsRetryable(errors.New("unclassified")))
}

func TestTenantFromContext_RoundTrips(t *testing.T) {
	t.Parallel()
	_, ok := connectors.TenantFromContext(context.Background())
	assert.False(t, ok)
	tenant, ok := connectors.TenantFromContext(connectors.WithTenant(context.Background(), "tenant-1"))
	assert.True(t, ok)
	assert.Equal(t, "tenant-1", tenant)
}

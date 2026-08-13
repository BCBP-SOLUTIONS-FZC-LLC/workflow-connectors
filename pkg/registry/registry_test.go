package registry_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

func TestAll_SixTypes(t *testing.T) {
	t.Parallel()

	defs := registry.All()
	require.Len(t, defs, 6)

	wantTypes := []string{
		registry.TypeStorage,
		registry.TypeSendEmail,
		registry.TypeDocumentExtract,
		registry.TypeRestCall,
		registry.TypeSQLQuery,
		registry.TypeChatNotify,
	}
	for _, want := range wantTypes {
		def, ok := defs[want]
		assert.True(t, ok, "missing definition for %q", want)
		assert.Equal(t, want, def.Type)
		assert.NotEmpty(t, def.DisplayName)
		assert.NotEmpty(t, def.Inputs)
		assert.NotEmpty(t, def.Outputs)
		assert.NotEmpty(t, def.Retry)
	}
}

func TestAll_RetryPolicies(t *testing.T) {
	t.Parallel()

	defs := registry.All()
	assert.Equal(t, registry.RetryPolicySafe, defs[registry.TypeStorage].Retry)
	assert.Equal(t, registry.RetryPolicyUnsafe, defs[registry.TypeSendEmail].Retry)
	assert.Equal(t, registry.RetryPolicySafe, defs[registry.TypeDocumentExtract].Retry)
	assert.Equal(t, registry.RetryPolicyConditional, defs[registry.TypeRestCall].Retry)
	assert.Equal(t, registry.RetryPolicySafe, defs[registry.TypeSQLQuery].Retry)
	assert.Equal(t, registry.RetryPolicyUnsafe, defs[registry.TypeChatNotify].Retry)
}

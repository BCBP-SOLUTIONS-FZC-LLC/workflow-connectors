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

// fieldByName finds a field by name in a slice, failing the test if absent —
// every per-field assertion below anchors on a field that must exist, so a
// dropped field fails loudly instead of the assertion silently never running.
func fieldByName(t *testing.T, fields []registry.Field, name string) registry.Field {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("field %q not found", name)
	return registry.Field{}
}

// TestAll_FieldContents asserts on individual field shape — Kind, Required,
// EnumValues — for every connector type, not just aggregate counts. This is
// the class of bug (chat-notify.visibility shipping as an enum with zero
// EnumValues) that aggregate-only assertions miss entirely.
func TestAll_FieldContents(t *testing.T) {
	t.Parallel()

	defs := registry.All()

	t.Run("storage", func(t *testing.T) {
		t.Parallel()
		in, out := defs[registry.TypeStorage].Inputs, defs[registry.TypeStorage].Outputs
		assert.True(t, fieldByName(t, in, "accessKey").IsSecretRef())
		assert.True(t, fieldByName(t, in, "secretKey").IsSecretRef())
		op := fieldByName(t, in, "operation")
		assert.Equal(t, registry.FieldKindEnum, op.Kind)
		assert.ElementsMatch(t, []string{"fetch", "upload", "delete"}, op.EnumValues)
		assert.True(t, op.Required)
		assert.Equal(t, registry.FieldKindTimestamp, fieldByName(t, out, "fetchedAt").Kind)
	})

	t.Run("send-email", func(t *testing.T) {
		t.Parallel()
		in, out := defs[registry.TypeSendEmail].Inputs, defs[registry.TypeSendEmail].Outputs
		assert.True(t, fieldByName(t, in, "apiKey").IsSecretRef())
		ct := fieldByName(t, in, "contentType")
		assert.Equal(t, registry.FieldKindEnum, ct.Kind)
		assert.ElementsMatch(t, []string{"text/plain", "text/html"}, ct.EnumValues)
		assert.Equal(t, registry.FieldKindTimestamp, fieldByName(t, out, "sentAt").Kind)
	})

	t.Run("document-extract", func(t *testing.T) {
		t.Parallel()
		in, out := defs[registry.TypeDocumentExtract].Inputs, defs[registry.TypeDocumentExtract].Outputs
		assert.True(t, fieldByName(t, in, "accessKey").IsSecretRef())
		assert.True(t, fieldByName(t, in, "secretKey").IsSecretRef())
		loc := fieldByName(t, in, "documentLocation")
		assert.Equal(t, registry.FieldKindEnum, loc.Kind)
		assert.ElementsMatch(t, []string{"s3", "inline"}, loc.EnumValues)
		assert.True(t, loc.Required)
		assert.Equal(t, registry.FieldKindMap, fieldByName(t, out, "confidence").Kind)
		assert.Equal(t, registry.FieldKindMap, fieldByName(t, out, "fields").Kind)
	})

	t.Run("rest-call", func(t *testing.T) {
		t.Parallel()
		in := defs[registry.TypeRestCall].Inputs
		alias := fieldByName(t, in, "endpointAlias")
		assert.Equal(t, registry.FieldKindString, alias.Kind)
		assert.True(t, alias.Required)
		for _, f := range in {
			assert.False(t, f.IsSecretRef(), "rest-call has no per-tenant secret fields (%s)", f.Name)
		}
	})

	t.Run("sql-query", func(t *testing.T) {
		t.Parallel()
		in := defs[registry.TypeSQLQuery].Inputs
		alias := fieldByName(t, in, "queryAlias")
		assert.Equal(t, registry.FieldKindString, alias.Kind)
		assert.True(t, alias.Required)
		for _, f := range in {
			assert.False(t, f.IsSecretRef(), "sql-query has no per-tenant secret fields (%s)", f.Name)
		}
	})

	t.Run("chat-notify", func(t *testing.T) {
		t.Parallel()
		in := defs[registry.TypeChatNotify].Inputs
		assert.True(t, fieldByName(t, in, "authToken").IsSecretRef())
		method := fieldByName(t, in, "method")
		assert.Equal(t, registry.FieldKindEnum, method.Kind)
		assert.ElementsMatch(t, []string{"create-channel", "invite-to-channel", "post-message"}, method.EnumValues)
		visibility := fieldByName(t, in, "visibility")
		assert.Equal(t, registry.FieldKindEnum, visibility.Kind)
		assert.NotEmpty(t, visibility.EnumValues, "visibility must declare its enum values")
		assert.ElementsMatch(t, []string{"public", "private"}, visibility.EnumValues)
	})
}

func TestIsIdempotentMethod(t *testing.T) {
	t.Parallel()

	assert.True(t, registry.IsIdempotentMethod("GET"))
	assert.True(t, registry.IsIdempotentMethod("get"))
	assert.True(t, registry.IsIdempotentMethod("PUT"))
	assert.True(t, registry.IsIdempotentMethod("DELETE"))
	assert.False(t, registry.IsIdempotentMethod("POST"))
	assert.False(t, registry.IsIdempotentMethod("PATCH"))
	assert.False(t, registry.IsIdempotentMethod(""))
}

package registry_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

func TestAll_FourTypes(t *testing.T) {
	t.Parallel()

	defs := registry.All()
	require.Len(t, defs, 4)

	wantTypes := []string{
		registry.TypeStorage,
		registry.TypeSendEmail,
		registry.TypeRestCall,
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
	assert.Equal(t, registry.RetryPolicyNotDelivered, defs[registry.TypeSendEmail].Retry)
	assert.Equal(t, registry.RetryPolicyConditional, defs[registry.TypeRestCall].Retry)
	assert.Equal(t, registry.RetryPolicyUnsafe, defs[registry.TypeChatNotify].Retry)
}

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

func hasField(fields []registry.Field, name string) bool {
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

func TestAll_FieldContents(t *testing.T) {
	t.Parallel()

	defs := registry.All()

	t.Run("storage", func(t *testing.T) {
		t.Parallel()
		in, out := defs[registry.TypeStorage].Inputs, defs[registry.TypeStorage].Outputs
		provider := fieldByName(t, in, "provider")
		assert.Equal(t, registry.FieldKindEnum, provider.Kind)
		assert.False(t, provider.Required)
		assert.Equal(t, []string{"aws-s3", "azure-blob", "gcp-gcs", "google-drive"}, provider.EnumValues, "order is the default — aws-s3 must stay first")
		assert.True(t, fieldByName(t, in, "accessKey").IsSecretRef())
		assert.True(t, fieldByName(t, in, "secretKey").IsSecretRef())
		for _, name := range []string{"azureAccountName", "azureAccountKey", "gcpServiceAccountKey", "driveServiceAccountKey"} {
			f := fieldByName(t, in, name)
			assert.Truef(t, f.IsSecretRef(), "%s must be a secret ref", name)
			assert.NotEmptyf(t, f.Condition, "%s must declare which provider it's scoped to", name)
		}
		assert.Equal(t, registry.FieldKindString, fieldByName(t, in, "projectId").Kind)
		op := fieldByName(t, in, "operation")
		assert.Equal(t, registry.FieldKindEnum, op.Kind)
		assert.ElementsMatch(t, []string{"fetch", "upload", "delete"}, op.EnumValues)
		assert.True(t, op.Required)
		assert.Equal(t, registry.FieldKindTimestamp, fieldByName(t, out, "fetchedAt").Kind)
	})

	t.Run("send-email", func(t *testing.T) {
		t.Parallel()
		in, out := defs[registry.TypeSendEmail].Inputs, defs[registry.TypeSendEmail].Outputs
		provider := fieldByName(t, in, "provider")
		assert.Equal(t, registry.FieldKindEnum, provider.Kind)
		assert.False(t, provider.Required)
		assert.Equal(t, []string{"sendgrid", "aws-ses", "microsoft-365", "google-workspace"}, provider.EnumValues)
		assert.True(t, fieldByName(t, in, "apiKey").IsSecretRef())
		assert.True(t, fieldByName(t, in, "accessKey").IsSecretRef())
		assert.True(t, fieldByName(t, in, "secretKey").IsSecretRef())
		assert.True(t, fieldByName(t, in, "clientSecret").IsSecretRef())
		assert.True(t, fieldByName(t, in, "serviceAccountKey").IsSecretRef())
		assert.False(t, fieldByName(t, in, "tenantId").IsSecretRef(), "tenantId is an identifier, not a credential")
		assert.False(t, fieldByName(t, in, "clientId").IsSecretRef(), "clientId is an identifier, not a credential")
		for _, name := range []string{"apiKey", "accessKey", "secretKey", "region", "tenantId", "clientId", "clientSecret", "serviceAccountKey"} {
			assert.NotEmptyf(t, fieldByName(t, in, name).Condition, "%s must declare which provider it's scoped to", name)
		}
		ct := fieldByName(t, in, "contentType")
		assert.Equal(t, registry.FieldKindEnum, ct.Kind)
		assert.ElementsMatch(t, []string{"text/plain", "text/html"}, ct.EnumValues)
		assert.Equal(t, registry.FieldKindTimestamp, fieldByName(t, out, "sentAt").Kind)
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

	t.Run("chat-notify", func(t *testing.T) {
		t.Parallel()
		in := defs[registry.TypeChatNotify].Inputs
		assert.False(t, hasField(in, "provider"), "no real per-provider implementation exists yet")
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

func TestSecretRefDescriptions_DoNotClaimAuthorSupplied(t *testing.T) {
	for _, def := range registry.All() {
		for _, f := range def.Inputs {
			if f.IsSecretRef() {
				assert.NotContains(t, strings.ToLower(f.Description), "author-supplied", "%s.%s", def.Type, f.Name)
			}
		}
	}
}

func TestStorage_HardeningFields(t *testing.T) {
	t.Parallel()

	def := registry.All()[registry.TypeStorage]
	assert.Equal(t, "required for aws-s3", fieldByName(t, def.Inputs, "region").Condition)
	assert.Equal(t, "optional, gcp-gcs", fieldByName(t, def.Inputs, "projectId").Condition)

	in := fieldByName(t, def.Inputs, "contentEncoding")
	assert.Equal(t, registry.FieldKindEnum, in.Kind)
	assert.Equal(t, []string{"utf-8", "base64"}, in.EnumValues)

	out := fieldByName(t, def.Outputs, "contentEncoding")
	assert.Equal(t, registry.FieldKindEnum, out.Kind)
	assert.Equal(t, []string{"utf-8", "base64"}, out.EnumValues)
	assert.NotContains(t, def.Description, "S3-compatible")
}

// Every field a connector reads or returns is in its definition: the
// registry is the contract definition_service validates authored workflows
// against, so a field missing here could never be authored or mapped.
func TestDefinitions_CoverFieldsTheConnectorsUse(t *testing.T) {
	t.Parallel()

	storage := registry.All()[registry.TypeStorage]
	contentType := fieldByName(t, storage.Inputs, "contentType")
	assert.Equal(t, registry.FieldKindString, contentType.Kind)
	assert.Equal(t, "only for upload", contentType.Condition)

	email := registry.All()[registry.TypeSendEmail]
	warning := fieldByName(t, email.Outputs, "sendIntentWarning")
	assert.Equal(t, registry.FieldKindString, warning.Kind)
}

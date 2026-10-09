package documentextract_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documentextract"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

func TestDocumentExtract_Type(t *testing.T) {
	t.Parallel()
	assert.Equal(t, registry.TypeDocumentExtract, documentextract.New(nil).Type())
}

func TestDocumentExtract_ProviderError_IsUpstream(t *testing.T) {
	t.Parallel()

	client := documentextract.NewMockDocumentExtractClient()
	boom := errors.New("provider down")
	client.SetError(boom)
	_, err := documentextract.New(client).Execute(context.Background(), map[string]any{
		"documentLocation": "inline",
		"documentRef":      "doc-ref-1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrUpstream)
	assert.ErrorIs(t, err, boom)
}

func TestDocumentExtract_SignaturesAndQueries(t *testing.T) {
	t.Parallel()

	out, err := documentextract.New(documentextract.NewMockDocumentExtractClient()).Execute(context.Background(), map[string]any{
		"documentLocation":  "s3",
		"documentBucket":    "b",
		"documentName":      "n.pdf",
		"analyzeSignatures": true,
		"analyzeQueries":    true,
		"query":             "who signed?",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"mock_signature_1"}, out["signaturesDetected"])
	assert.Equal(t, []string{"mock answer for: who signed?"}, out["answers"])
	assert.Equal(t, "mock raw text for s3://b/n.pdf/", out["rawText"])
}

func TestDocumentExtract_MissingDocumentFields(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]map[string]any{
		"inline without ref": {"documentLocation": "inline"},
		"s3 without name":    {"documentLocation": "s3", "documentBucket": "b"},
		"s3 without bucket":  {"documentLocation": "s3", "documentName": "n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := documentextract.New(documentextract.NewMockDocumentExtractClient()).Execute(context.Background(), input)
			require.Error(t, err)
			assert.ErrorIs(t, err, shared.ErrValidation)
		})
	}
}

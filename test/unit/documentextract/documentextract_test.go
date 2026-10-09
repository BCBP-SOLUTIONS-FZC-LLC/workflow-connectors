package documentextract_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documentextract"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestDocumentExtract_AnalyzeForm(t *testing.T) {
	t.Parallel()

	conn := documentextract.New(documentextract.NewMockDocumentExtractClient())

	out, err := conn.Execute(context.Background(), map[string]any{
		"documentLocation": "inline",
		"documentRef":      "doc-ref-1",
		"analyzeForm":      true,
	})
	require.NoError(t, err)
	fields, ok := out["fields"].(map[string]string)
	require.True(t, ok)
	assert.Equal(t, "mock_value", fields["mock_field"])
	_, hasAnswers := out["answers"]
	assert.False(t, hasAnswers)
}

func TestDocumentExtract_AnalyzeQueries_RequiresQuery(t *testing.T) {
	t.Parallel()

	conn := documentextract.New(documentextract.NewMockDocumentExtractClient())

	_, err := conn.Execute(context.Background(), map[string]any{
		"documentLocation": "inline",
		"documentRef":      "doc-ref-1",
		"analyzeQueries":   true,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestDocumentExtract_S3Location(t *testing.T) {
	t.Parallel()

	conn := documentextract.New(documentextract.NewMockDocumentExtractClient())

	out, err := conn.Execute(context.Background(), map[string]any{
		"documentLocation": "s3",
		"documentBucket":   "bucket",
		"documentName":     "name",
		"documentVersion":  "v1",
	})
	require.NoError(t, err)
	assert.Contains(t, out["rawText"], "s3://bucket/name/v1")
}

func TestDocumentExtract_InvalidLocation(t *testing.T) {
	t.Parallel()

	conn := documentextract.New(documentextract.NewMockDocumentExtractClient())

	_, err := conn.Execute(context.Background(), map[string]any{"documentLocation": "ftp"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestMockDocumentExtractClient_SetErrorAndReset(t *testing.T) {
	t.Parallel()

	client := documentextract.NewMockDocumentExtractClient()
	client.SetError(errors.New("boom"))
	_, err := client.Analyze(context.Background(), documentextract.AnalyzeRequest{DocumentRef: "d1"})
	require.Error(t, err)

	client.Reset()
	_, err = client.Analyze(context.Background(), documentextract.AnalyzeRequest{DocumentRef: "d1"})
	require.NoError(t, err)
}

func TestDocumentExtract_NoProvider_IsValidationError(t *testing.T) {
	t.Parallel()

	conn := documentextract.New(nil)

	_, err := conn.Execute(context.Background(), map[string]any{"documentLocation": "inline", "documentRef": "r"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

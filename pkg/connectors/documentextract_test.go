package connectors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/connectors"
)

func TestDocumentExtract_AnalyzeForm(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(validConfig())
	require.NoError(t, err)

	out, err := all["document-extract"].Execute(context.Background(), map[string]any{
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

	all, err := connectors.New(validConfig())
	require.NoError(t, err)

	_, err = all["document-extract"].Execute(context.Background(), map[string]any{
		"documentLocation": "inline",
		"documentRef":      "doc-ref-1",
		"analyzeQueries":   true,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestDocumentExtract_S3Location(t *testing.T) {
	t.Parallel()

	all, err := connectors.New(validConfig())
	require.NoError(t, err)

	out, err := all["document-extract"].Execute(context.Background(), map[string]any{
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

	all, err := connectors.New(validConfig())
	require.NoError(t, err)

	_, err = all["document-extract"].Execute(context.Background(), map[string]any{"documentLocation": "ftp"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, connectors.ErrValidation))
}

func TestMockDocumentExtractClient_SetErrorAndReset(t *testing.T) {
	t.Parallel()

	client := connectors.NewMockDocumentExtractClient()
	client.SetError(errors.New("boom"))
	_, err := client.Analyze(context.Background(), connectors.AnalyzeRequest{DocumentRef: "d1"})
	require.Error(t, err)

	client.Reset()
	_, err = client.Analyze(context.Background(), connectors.AnalyzeRequest{DocumentRef: "d1"})
	require.NoError(t, err)
}

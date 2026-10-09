package documentextract

import (
	"context"
	"fmt"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

type ProviderClient interface {
	Analyze(ctx context.Context, req AnalyzeRequest) (AnalyzeResult, error)
}

type AnalyzeRequest struct {
	DocumentRef       string
	AnalyzeForm       bool
	AnalyzeSignatures bool
	AnalyzeLayout     bool
	AnalyzeQueries    bool
	Query             string
}

type AnalyzeResult struct {
	Fields             map[string]string
	RawText            string
	SignaturesDetected []string
	Answers            []string
	Confidence         map[string]float64
}

type Connector struct {
	client ProviderClient
}

// New builds the connector. A nil client is not replaced by the mock: every
// call then fails with ErrValidation (Decision #20). Pass
// NewMockDocumentExtractClient() explicitly in tests.
func New(client ProviderClient) Connector {
	return Connector{client: client}
}

func (Connector) Type() string { return registry.TypeDocumentExtract }

func (d Connector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	if d.client == nil {
		return nil, fmt.Errorf("%w: document-extract has no provider configured", shared.ErrValidation)
	}
	docRef, err := resolveDocumentRef(input)
	if err != nil {
		return nil, err
	}

	req := AnalyzeRequest{
		DocumentRef:       docRef,
		AnalyzeForm:       shared.BoolField(input, "analyzeForm"),
		AnalyzeSignatures: shared.BoolField(input, "analyzeSignatures"),
		AnalyzeLayout:     shared.BoolField(input, "analyzeLayout"),
		AnalyzeQueries:    shared.BoolField(input, "analyzeQueries"),
		Query:             shared.StringField(input, "query"),
	}
	if req.AnalyzeQueries && req.Query == "" {
		return nil, fmt.Errorf("%w: query is required when analyzeQueries is true", shared.ErrValidation)
	}

	result, err := d.client.Analyze(ctx, req)
	if err != nil {
		return nil, shared.Classify("document-extract", err)
	}

	out := map[string]any{"rawText": result.RawText}
	if req.AnalyzeForm {
		out["fields"] = result.Fields
	}
	if req.AnalyzeSignatures {
		out["signaturesDetected"] = result.SignaturesDetected
	}
	if req.AnalyzeQueries {
		out["answers"] = result.Answers
	}
	if len(result.Confidence) > 0 {
		out["confidence"] = result.Confidence
	}
	return out, nil
}

func resolveDocumentRef(input map[string]any) (string, error) {
	switch shared.StringField(input, "documentLocation") {
	case "inline":
		ref := shared.StringField(input, "documentRef")
		if ref == "" {
			return "", fmt.Errorf("%w: documentRef is required when documentLocation=inline", shared.ErrValidation)
		}
		return ref, nil
	case "s3":
		bucket := shared.StringField(input, "documentBucket")
		name := shared.StringField(input, "documentName")
		if bucket == "" || name == "" {
			return "", fmt.Errorf("%w: documentBucket and documentName are required when documentLocation=s3", shared.ErrValidation)
		}
		version := shared.StringField(input, "documentVersion")
		return fmt.Sprintf("s3://%s/%s/%s", bucket, name, version), nil
	default:
		return "", fmt.Errorf("%w: documentLocation must be \"s3\" or \"inline\"", shared.ErrValidation)
	}
}

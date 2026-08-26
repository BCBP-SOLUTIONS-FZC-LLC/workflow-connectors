package connectors

import (
	"context"
	"fmt"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/pkg/registry"
)

type DocumentExtractProviderClient interface {
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

type documentExtractConnector struct {
	client DocumentExtractProviderClient
}

func newDocumentExtract(cfg Config) Connector {
	client := cfg.DocumentExtractClient
	if client == nil {
		client = NewMockDocumentExtractClient()
	}
	return documentExtractConnector{client: client}
}

func (documentExtractConnector) Type() string { return registry.TypeDocumentExtract }

func (d documentExtractConnector) Execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	docRef, err := resolveDocumentRef(input)
	if err != nil {
		return nil, err
	}

	req := AnalyzeRequest{
		DocumentRef:       docRef,
		AnalyzeForm:       boolField(input, "analyzeForm"),
		AnalyzeSignatures: boolField(input, "analyzeSignatures"),
		AnalyzeLayout:     boolField(input, "analyzeLayout"),
		AnalyzeQueries:    boolField(input, "analyzeQueries"),
		Query:             stringField(input, "query"),
	}
	if req.AnalyzeQueries && req.Query == "" {
		return nil, fmt.Errorf("%w: query is required when analyzeQueries is true", ErrValidation)
	}

	result, err := d.client.Analyze(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: document-extract: %s", ErrUpstream, err)
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
	switch stringField(input, "documentLocation") {
	case "inline":
		ref := stringField(input, "documentRef")
		if ref == "" {
			return "", fmt.Errorf("%w: documentRef is required when documentLocation=inline", ErrValidation)
		}
		return ref, nil
	case "s3":
		bucket := stringField(input, "documentBucket")
		name := stringField(input, "documentName")
		if bucket == "" || name == "" {
			return "", fmt.Errorf("%w: documentBucket and documentName are required when documentLocation=s3", ErrValidation)
		}
		version := stringField(input, "documentVersion")
		return fmt.Sprintf("s3://%s/%s/%s", bucket, name, version), nil
	default:
		return "", fmt.Errorf("%w: documentLocation must be \"s3\" or \"inline\"", ErrValidation)
	}
}

package connectors

import (
	"context"
	"fmt"
	"sync"
)

// MockDocumentExtractClient returns deterministic canned data keyed off the
// request's own analyze-flags, rather than a static empty struct — a test
// can assert the exact canned shape for a given flag combination, while it
// stays an obvious placeholder for the real OCR/extraction provider.
type MockDocumentExtractClient struct {
	mu  sync.Mutex
	err error
}

func NewMockDocumentExtractClient() *MockDocumentExtractClient {
	return &MockDocumentExtractClient{}
}

func (m *MockDocumentExtractClient) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockDocumentExtractClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = nil
}

func (m *MockDocumentExtractClient) Analyze(_ context.Context, req AnalyzeRequest) (AnalyzeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return AnalyzeResult{}, m.err
	}

	result := AnalyzeResult{RawText: "mock raw text for " + req.DocumentRef}
	if req.AnalyzeForm {
		result.Fields = map[string]string{"mock_field": "mock_value"}
		result.Confidence = map[string]float64{"mock_field": 0.99}
	}
	if req.AnalyzeSignatures {
		result.SignaturesDetected = []string{"mock_signature_1"}
	}
	if req.AnalyzeQueries {
		result.Answers = []string{fmt.Sprintf("mock answer for: %s", req.Query)}
	}
	return result, nil
}

var _ DocumentExtractProviderClient = (*MockDocumentExtractClient)(nil)

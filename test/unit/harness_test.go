// Package unit_test holds black-box scenario, contract and regression tests
// for the library's public API (pkg/connectors and pkg/registry). They need
// no Docker and run with `make test-unit` and in every CI run; tests that
// need PostgreSQL, Valkey or S3 live in test/postgres and test/integration
// (build tag integration). Per-package black-box tests live in subdirectories
// mirroring pkg/ (test/unit/storage, test/unit/sendemail, …); only white-box
// tests that need unexported code stay next to it in pkg/.
package unit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

// attempt is one Execute the simulated worker made, and what it decided.
type attempt struct {
	out      map[string]any
	err      error
	decision connectors.RetryDecision
}

// runTask is the connector worker's retry loop, reduced to its decision: run
// Execute, and re-run it only while connectors.DecideRetry allows, up to
// maxAttempts (the worker's attempt limit; backoff is irrelevant here).
func runTask(ctx context.Context, conn connectors.Connector, method string, input map[string]any, maxAttempts int) []attempt {
	var attempts []attempt
	for range maxAttempts {
		out, err := conn.Execute(ctx, input)
		a := attempt{out: out, err: err}
		if err != nil {
			a.decision = connectors.DecideRetry(conn.Type(), err, method)
		}
		attempts = append(attempts, a)
		if err == nil || !a.decision.Retry {
			break
		}
	}
	return attempts
}

func last(attempts []attempt) attempt { return attempts[len(attempts)-1] }

// emailStep is one scripted provider response.
type emailStep struct {
	err       error
	delivered bool // the provider accepted the message, whatever the caller saw
}

// scriptedEmail plays emailSteps in order (the last repeats) and records
// every message the provider actually accepted.
type scriptedEmail struct {
	mu        sync.Mutex
	steps     []emailStep
	calls     int
	delivered []sendemail.EmailMessage
}

func (s *scriptedEmail) Send(_ context.Context, msg sendemail.EmailMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step := s.steps[min(s.calls, len(s.steps)-1)]
	s.calls++
	if step.err == nil || step.delivered {
		s.delivered = append(s.delivered, msg)
	}
	if step.err != nil {
		return "", step.err
	}
	return "provider-msg-1", nil
}

func (s *scriptedEmail) deliveredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.delivered)
}

// scriptedBucket fails Fetch with errs in order, then serves the mock bucket.
type scriptedBucket struct {
	*storage.MockStorageClient
	mu    sync.Mutex
	errs  []error
	calls int
}

func (b *scriptedBucket) Fetch(ctx context.Context, bucket, key string, maxBytes int64) ([]byte, string, error) {
	b.mu.Lock()
	i := b.calls
	b.calls++
	b.mu.Unlock()
	if i < len(b.errs) {
		return nil, "", b.errs[i]
	}
	return b.MockStorageClient.Fetch(ctx, bucket, key, maxBytes)
}

// world is one worker's connectors over in-memory stores.
type world struct {
	byType  map[string]connectors.Connector
	email   *scriptedEmail
	bucket  *scriptedBucket
	intents *sendintent.MemoryStore
	docRefs *docref.Service
	content *docref.MemoryContent
}

func newWorld(t *testing.T, email *scriptedEmail, bucketErrs ...error) *world {
	t.Helper()
	w := &world{
		email:   email,
		bucket:  &scriptedBucket{MockStorageClient: storage.NewMockStorageClient(), errs: bucketErrs},
		intents: sendintent.NewMemoryStore(),
		content: docref.NewMemoryContent("docs"),
	}
	if w.email == nil {
		w.email = &scriptedEmail{steps: []emailStep{{}}}
	}
	w.docRefs = docref.NewService(docref.NewMemoryStore(), w.content)
	cfg := connectors.Config{
		InternalToken: "test-token",
		StorageProviders: map[string]storage.ProviderConstructor{
			"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return w.bucket, nil },
		},
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return w.email, nil },
		},
		DocRefs:     w.docRefs,
		SendIntents: w.intents,
	}
	var err error
	w.byType, err = connectors.New(cfg)
	require.NoError(t, err)
	return w
}

func tenantCtx() context.Context { return connectors.WithTenant(context.Background(), "tenant-1") }

func emailInput(extra ...any) map[string]any {
	in := map[string]any{"provider": "sendgrid", "senderEmail": "billing@example.com", "receiverEmail": "customer@example.com", "body": "Your invoice"}
	for i := 0; i+1 < len(extra); i += 2 {
		in[extra[i].(string)] = extra[i+1]
	}
	return in
}

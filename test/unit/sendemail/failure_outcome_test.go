package sendemail_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// Every failure output carries deliveryOutcome, as the runbook promises:
// input errors, a missing tenant, a document-ref outage and an intent-store
// failure all sent nothing (not_delivered).
func TestSendEmail_EveryFailure_ReportsDeliveryOutcome(t *testing.T) {
	t.Parallel()

	tenant := shared.WithTenant(context.Background(), "t")
	plain := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService())
	withIntents := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService(),
		sendemail.WithSendIntents(sendintent.NewMemoryStore()))
	refOutage := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()),
		docref.NewService(failingRefStore{err: docref.ErrUnavailable}, docref.NewMemoryContent("test-documents")))
	storeDown := sendemail.New(providersFor(sendemail.NewMockSendEmailClient()), docref.NewMemoryService(),
		sendemail.WithSendIntents(failingIntentStore{}))
	buildFails := sendemail.New(map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
			return nil, errors.New("boom")
		},
	}, docref.NewMemoryService())

	cases := []struct {
		name  string
		conn  *sendemail.Connector
		ctx   context.Context
		input map[string]any
	}{
		{"missing receiver", plain, tenant, emailInput("receiverEmail", "")},
		{"invalid sender", plain, tenant, emailInput("senderEmail", "a@x.com, b@y.com")},
		{"invalid receiver", plain, tenant, emailInput("receiverEmail", "not an address")},
		{"no body or template", plain, tenant, emailInput("body", "")},
		{"templateless provider without body", plain, tenant, emailInput("provider", "google-workspace", "body", "", "templateId", "x")},
		{"unresolvable attachment", plain, tenant, emailInput("attachments", []any{"docref:missing"})},
		{"attachments without tenant", plain, context.Background(), emailInput("attachments", []any{docref.NewID()})},
		{"messageKey without store", plain, tenant, emailInput("messageKey", "k")},
		{"messageKey without tenant", withIntents, context.Background(), emailInput("messageKey", "k")},
		{"missing provider", plain, tenant, emailInput("provider", "")},
		{"document-ref outage", refOutage, tenant, emailInput("attachments", []any{docref.NewID()})},
		{"intent-store failure", storeDown, tenant, emailInput("messageKey", "k")},
		{"client build failure", buildFails, tenant, emailInput()},
	}
	for _, tc := range cases {
		out, err := tc.conn.Execute(tc.ctx, tc.input)
		assert.Error(t, err, tc.name)
		assert.Equal(t, false, out["sent"], tc.name)
		assert.Equal(t, "not_delivered", out["deliveryOutcome"], tc.name)
	}
}

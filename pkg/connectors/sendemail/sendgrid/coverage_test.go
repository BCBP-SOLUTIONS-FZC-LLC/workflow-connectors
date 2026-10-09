package sendgrid

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestSendGridClient_Close_Variants(t *testing.T) {
	t.Parallel()

	client, err := NewProvider(context.Background(), map[string]any{"apiKey": "sg-key"})
	require.NoError(t, err)
	assert.NoError(t, client.(*sendGridClient).Close(), "the real API drops its idle connections")
	assert.NoError(t, (&sendGridClient{api: &fakeSendGridAPI{}}).Close(), "an API without idle connections")
}

func TestRealSendGridAPI_InvalidHost_IsNotDelivered(t *testing.T) {
	t.Parallel()

	client := &sendGridClient{api: newRealSendGridAPI("sg-key", "://no-scheme")}
	_, err := client.Send(context.Background(), sendemail.EmailMessage{SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, shared.ErrNotDelivered, "the request was never written")
}

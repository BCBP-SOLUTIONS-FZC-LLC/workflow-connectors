package ses

import (
	"context"
	"net/mail"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
)

func TestSESClient_Send_SenderNameIsEncoded(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Doe, John", "Zoë Müller", `Quote "Q" Name`} {
		fake := &fakeSESAPI{messageID: "id"}
		client := &sesClient{api: fake}

		_, err := client.Send(context.Background(), sendemail.EmailMessage{
			SenderName: name, SenderEmail: "a@example.com", ReceiverEmail: "b@example.com", Body: "x",
		})
		require.NoError(t, err)

		parsed, err := mail.ParseAddress(*fake.lastInput.FromEmailAddress)
		require.NoErrorf(t, err, "From %q must parse as one address", *fake.lastInput.FromEmailAddress)
		assert.Equal(t, name, parsed.Name)
		assert.Equal(t, "a@example.com", parsed.Address)
	}
}

func TestNewProvider_RegionRequired(t *testing.T) {
	t.Parallel()

	_, err := NewProvider(context.Background(), map[string]any{"accessKey": "a", "secretKey": "s"})
	require.Error(t, err)
}

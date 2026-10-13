package ses

import (
	"context"
	"net/mail"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
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

// The region is placed in the endpoint host name, so only one DNS label is
// accepted; anything that could point the signed request elsewhere is
// invalid input.
func TestNewProvider_RegionMustBeOneHostLabel(t *testing.T) {
	t.Parallel()

	for _, region := range []string{"evil.com/x", "us-east-1.attacker.example", "a@b", "US-EAST-1", "-us", "us-", "us east", strings.Repeat("a", 64)} {
		_, err := NewProvider(context.Background(), map[string]any{"accessKey": "a", "secretKey": "s", "region": region})
		assert.ErrorIsf(t, err, shared.ErrValidation, "region %q", region)
	}
	for _, region := range []string{"eu-west-1", "us-gov-west-1", "cn-north-1"} {
		_, err := NewProvider(context.Background(), map[string]any{"accessKey": "a", "secretKey": "s", "region": region})
		assert.NoErrorf(t, err, "region %q", region)
	}
}

package gmail

import (
	"bytes"
	"errors"
	"net/http"
	"net/mail"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// A display name can never add a recipient or change an address: From and To
// each parse as exactly one address, the one given, with the name intact.
func TestBuildRawMIME_DisplayNamesCannotInjectAddresses(t *testing.T) {
	t.Parallel()

	names := []string{
		"Attacker <evil@attacker.com>, Bob",
		"a, b",
		"<evil@attacker.com>",
		"x > y",
		`Say "hi" \ there`,
		"Jürgen Müller, 東京",
		"\"evil@attacker.com\" <evil@attacker.com>",
		"line\r\nBcc: evil@attacker.com",
	}
	for _, name := range names {
		raw := buildRawMIME(sendemail.EmailMessage{
			SenderName: name, SenderEmail: "noreply@corp.com",
			ReceiverName: name, ReceiverEmail: "victim@example.com",
			Subject: "hi", Body: "b",
		})
		msg, err := mail.ReadMessage(bytes.NewReader(raw))
		require.NoError(t, err, name)
		assert.Empty(t, msg.Header.Get("Bcc"), name)

		for header, want := range map[string]string{"From": "noreply@corp.com", "To": "victim@example.com"} {
			list, err := msg.Header.AddressList(header)
			require.NoError(t, err, "%s for name %q", header, name)
			require.Lenf(t, list, 1, "%s for name %q: %s", header, name, msg.Header.Get(header))
			assert.Equal(t, want, list[0].Address, name)
			assert.Equal(t, stripCRLF(name), list[0].Name, name)
		}
	}
}

// Gmail throttles with 403 and a rate-limit reason: not delivered and
// transient (retried), unlike any other 403 (permanent).
func TestClassifyError_RateLimit403_IsTransientNotDelivered(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{"rateLimitExceeded", "userRateLimitExceeded"} {
		apiErr := &googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{Reason: "other"}, {Reason: reason}}}
		err := classifyError(apiErr, nil)
		assert.ErrorIs(t, err, shared.ErrNotDelivered, reason)
		assert.True(t, shared.IsTransient(err), reason)
		_, why := shared.ClassOf(err)
		assert.Contains(t, why, reason)
		var got *googleapi.Error
		assert.True(t, errors.As(err, &got), "the API error stays reachable")
	}

	forbidden := classifyError(&googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{Reason: "insufficientPermissions"}}}, nil)
	assert.ErrorIs(t, forbidden, shared.ErrNotDelivered)
	assert.True(t, shared.IsPermanent(forbidden))

	// A rate-limit reason with another status keeps that status's class.
	unavailable := classifyError(&googleapi.Error{Code: http.StatusServiceUnavailable, Errors: []googleapi.ErrorItem{{Reason: "rateLimitExceeded"}}}, nil)
	assert.ErrorIs(t, unavailable, shared.ErrDeliveryUnknown)
}

func TestGmailClient_MaxAttachmentBytes(t *testing.T) {
	t.Parallel()
	var limiter sendemail.AttachmentLimiter = &gmailClient{}
	assert.Equal(t, int64(25<<20), limiter.MaxAttachmentBytes())
}

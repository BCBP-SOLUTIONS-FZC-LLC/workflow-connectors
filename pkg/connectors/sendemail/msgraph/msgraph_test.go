package msgraph

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

type fakeGraphAPI struct {
	lastSenderEmail string
	lastBody        []byte
	err             error
}

func (f *fakeGraphAPI) sendMail(_ context.Context, senderEmail string, body []byte) error {
	f.lastSenderEmail = senderEmail
	f.lastBody = body
	return f.err
}

func TestGraphClient_Send_BuildsRequestForSenderMailbox(t *testing.T) {
	t.Parallel()

	fake := &fakeGraphAPI{}
	client := &graphClient{api: fake}

	_, err := client.Send(context.Background(), sendemail.EmailMessage{
		SenderEmail:   "a@example.com",
		ReceiverEmail: "b@example.com",
		Subject:       "hi",
		Body:          "hello",
		ContentType:   "text/html",
		Attachments:   []sendemail.EmailAttachment{{Filename: "a.pdf", ContentType: "application/pdf", Content: []byte("x")}},
	})
	require.NoError(t, err)
	assert.Equal(t, "a@example.com", fake.lastSenderEmail)
	assert.Contains(t, string(fake.lastBody), `"contentType":"HTML"`)
	assert.Contains(t, string(fake.lastBody), `"@odata.type":"#microsoft.graph.fileAttachment"`)
}

func TestGraphClient_Send_APIError_Propagates(t *testing.T) {
	t.Parallel()

	client := &graphClient{api: &fakeGraphAPI{err: errors.New("boom")}}
	_, err := client.Send(context.Background(), sendemail.EmailMessage{SenderEmail: "a@example.com", Body: "x"})
	require.Error(t, err)
}

func TestNewGraphSendMailProvider_MissingCredentials_IsValidationError(t *testing.T) {
	t.Parallel()

	_, err := NewProvider(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, shared.ErrValidation))
}

func TestRealGraphAPI_SendMail_PostsToSenderMailbox(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	api := &realGraphAPI{httpClient: srv.Client(), baseURL: srv.URL}
	err := api.sendMail(context.Background(), "a@example.com", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "/users/a@example.com/sendMail", gotPath)
}

func TestRealGraphAPI_SendMail_NonSuccessStatus_IsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "forbidden")
	}))
	defer srv.Close()

	api := &realGraphAPI{httpClient: srv.Client(), baseURL: srv.URL}
	err := api.sendMail(context.Background(), "a@example.com", []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")
}

func TestNewGraphSendMailProvider_BuildsClient(t *testing.T) {
	t.Parallel()

	client, err := NewProvider(context.Background(), map[string]any{
		"tenantId":     "tenant-1",
		"clientId":     "client-1",
		"clientSecret": "secret",
	})
	require.NoError(t, err)
	assert.NotNil(t, client)
}

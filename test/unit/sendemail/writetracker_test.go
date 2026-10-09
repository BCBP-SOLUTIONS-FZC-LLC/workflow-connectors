package sendemail_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// post sends body to addr with a write tracker and classifies the failure as
// an adapter does.
func post(t *testing.T, addr string, body []byte) error {
	t.Helper()
	ctx, written := sendemail.TraceWrites(context.Background())
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/send", bytes.NewReader(body))
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected a transport failure")
	}
	return sendemail.ClassifyAfterSend("p", err, written)
}

// A request the server reset part-way through its body may have reached the
// provider (a provider can act on a partial body or have read it all before
// the reset): unknown, never not_delivered.
func TestTraceWrites_PartlyWrittenRequest_IsUnknown(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf) // the request line and headers, then reset
		_ = conn.(*net.TCPConn).SetLinger(0)
		_ = conn.Close()
	}()

	err = post(t, ln.Addr().String(), make([]byte, 32<<20))
	assert.ErrorIs(t, err, shared.ErrDeliveryUnknown)
}

// A connection that was never established wrote nothing: not delivered.
func TestTraceWrites_DialFailure_IsNotDelivered(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	err = post(t, addr, []byte("hello"))
	assert.ErrorIs(t, err, shared.ErrNotDelivered)
}

func TestWriteTracker_NilIsNotWritten(t *testing.T) {
	t.Parallel()
	var tracker *sendemail.WriteTracker
	assert.False(t, tracker.Wrote())
}

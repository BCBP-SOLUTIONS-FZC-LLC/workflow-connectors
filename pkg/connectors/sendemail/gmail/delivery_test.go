package gmail

import (
	"context"
	"errors"
	"net/http/httptrace"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestGmail_ClassifyError(t *testing.T) {
	t.Parallel()

	// A request that was fully written to the provider.
	ctx, written := sendemail.TraceWrites(context.Background())
	httptrace.ContextClientTrace(ctx).WroteRequest(httptrace.WroteRequestInfo{})
	_, notWritten := sendemail.TraceWrites(context.Background())

	assert.ErrorIs(t, classifyError(&googleapi.Error{Code: 400}, written), shared.ErrNotDelivered)
	assert.ErrorIs(t, classifyError(&googleapi.Error{Code: 429}, written), shared.ErrNotDelivered)
	assert.ErrorIs(t, classifyError(&googleapi.Error{Code: 500}, written), shared.ErrDeliveryUnknown)
	assert.ErrorIs(t, classifyError(&oauth2.RetrieveError{}, notWritten), shared.ErrNotDelivered, "a refused token means send was never called")
	assert.ErrorIs(t, classifyError(context.DeadlineExceeded, written), shared.ErrDeliveryUnknown, "written, then timed out")
	assert.ErrorIs(t, classifyError(errors.New("connection reset"), written), shared.ErrDeliveryUnknown)
	assert.ErrorIs(t, classifyError(context.DeadlineExceeded, notWritten), shared.ErrNotDelivered, "timed out before the request was written")
}

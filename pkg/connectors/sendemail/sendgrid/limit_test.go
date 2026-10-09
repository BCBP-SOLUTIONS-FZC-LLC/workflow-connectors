package sendgrid

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
)

func TestSendGridClient_MaxAttachmentBytes(t *testing.T) {
	t.Parallel()
	var limiter sendemail.AttachmentLimiter = &sendGridClient{}
	assert.Equal(t, int64(20<<20), limiter.MaxAttachmentBytes())
}

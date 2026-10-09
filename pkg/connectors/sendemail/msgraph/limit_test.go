package msgraph

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
)

// An inline sendMail request carries at most 3 MiB of raw attachments.
func TestGraphClient_MaxAttachmentBytes(t *testing.T) {
	t.Parallel()
	var limiter sendemail.AttachmentLimiter = &graphClient{}
	assert.Equal(t, int64(3<<20), limiter.MaxAttachmentBytes())
}

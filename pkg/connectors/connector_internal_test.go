package connectors

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/chatnotify"
)

func TestIndexByType_DuplicateTypePanics(t *testing.T) {
	t.Parallel()
	c := chatnotify.New(nil)
	assert.PanicsWithValue(t, `connectors: duplicate connector Type "chat-notify"`, func() {
		indexByType([]Connector{c, c})
	})
}

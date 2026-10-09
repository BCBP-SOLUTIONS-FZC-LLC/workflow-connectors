package sendemail_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

// A class carried by a SendError's cause never disagrees with SendError's own
// class: errors.As cannot reach the inner Classifier, errors.Is cannot match a
// class sentinel through it, and shared.ClassOf reports SendError's class.
func TestSendError_CauseClassNeverDisagrees(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{Err: "no such host", Name: "api.example.com"}
	cases := map[string]struct {
		err  error
		want shared.Class
	}{
		"transient cause under an unknown outcome": {
			err: sendemail.Unknown("p", 0, shared.Transient("token request failed", dnsErr)), want: shared.ClassUnknown,
		},
		"permanent cause under an unknown outcome": {
			err: sendemail.Unknown("p", 0, shared.Permanent("rejected", errors.New("x"))), want: shared.ClassUnknown,
		},
		"validation cause under an unrecorded intent": {
			err: &sendemail.SendError{Outcome: sendemail.OutcomeNotDelivered, Provider: "p", IntentUnrecorded: true,
				Err: fmt.Errorf("%w: bad", shared.ErrValidation)},
			want: shared.ClassUnknown,
		},
		"missing tenant under an unknown outcome": {
			err: sendemail.Unknown("p", 0, shared.ErrMissingTenant), want: shared.ClassUnknown,
		},
		"not-delivered SendError nested in an unknown one": {
			err: sendemail.Unknown("outer", 0, sendemail.NotDelivered("inner", 429, errors.New("throttled"))), want: shared.ClassUnknown,
		},
		"joined classified causes": {
			err: sendemail.Unknown("p", 0, errors.Join(errors.New("a"), shared.Transient("t", dnsErr))), want: shared.ClassUnknown,
		},
	}
	for name, tc := range cases {
		class, _ := shared.ClassOf(tc.err)
		assert.Equal(t, tc.want, class, name)

		var classified *shared.ClassifiedError
		assert.False(t, errors.As(tc.err, &classified), "%s: the inner ClassifiedError is hidden", name)
		var classifier shared.Classifier
		require.True(t, errors.As(tc.err, &classifier), name)
		var outer *sendemail.SendError
		require.True(t, errors.As(tc.err, &outer), name)
		assert.Same(t, outer, classifier, "%s: the only Classifier found is the SendError itself", name)

		for _, sentinel := range []error{shared.ErrValidation, shared.ErrMissingTenant, shared.ErrMissingInternalAuth, shared.ErrUpstream} {
			assert.False(t, errors.Is(tc.err, sentinel), "%s: %v", name, sentinel)
		}
		own, other := shared.ErrDeliveryUnknown, shared.ErrNotDelivered
		if outer.Outcome == sendemail.OutcomeNotDelivered {
			own, other = other, own
		}
		assert.True(t, errors.Is(tc.err, own), name)
		assert.False(t, errors.Is(tc.err, other), "%s: only the outer outcome matches", name)
	}
}

// Everything that is not a class stays reachable through the cause chain,
// including through a classified wrapper.
func TestSendError_CauseStaysReachable(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{Err: "no such host", Name: "api.example.com"}
	err := sendemail.NotDelivered("p", 0, shared.Transient("dns", fmt.Errorf("lookup: %w", dnsErr)))
	var gotDNS *net.DNSError
	require.True(t, errors.As(err, &gotDNS))
	assert.Same(t, dnsErr, gotDNS)
	var netErr net.Error
	assert.True(t, errors.As(err, &netErr), "an interface target matches too")
	assert.True(t, shared.IsTransient(err), "SendError's own class is the cause's class here")

	timeout := sendemail.Unknown("p", 0, fmt.Errorf("send: %w", context.DeadlineExceeded))
	assert.ErrorIs(t, timeout, context.DeadlineExceeded)

	custom := sendemail.Unknown("p", 0, matcher{})
	assert.ErrorIs(t, custom, errTarget, "a cause's own Is method is honoured")
	var target *asTarget
	assert.True(t, errors.As(custom, &target), "a cause's own As method is honoured")

	assert.False(t, errors.Is(sendemail.Unknown("p", 0, errors.New("x")), errTarget))
	assert.ErrorIs(t, sendemail.Unknown("p", 0, errors.Join(errors.New("a"), errTarget)), errTarget, "a joined cause is searched")
	joined := sendemail.Unknown("p", 0, errors.Join(errors.New("a"), errors.New("b")))
	assert.False(t, errors.As(joined, &gotDNS))
}

func TestSendError_NilCause(t *testing.T) {
	t.Parallel()
	err := &sendemail.SendError{Outcome: sendemail.OutcomeUnknown, Provider: "p"}
	assert.Nil(t, err.Unwrap())
}

// The view's As refuses a target errors.As would reject.
func TestSendError_UnwrapAsRejectsInvalidTargets(t *testing.T) {
	t.Parallel()
	view, ok := sendemail.Unknown("p", 0, errors.New("x")).(*sendemail.SendError).Unwrap().(interface{ As(any) bool })
	require.True(t, ok)
	assert.False(t, view.As(nil))
	assert.False(t, view.As((*error)(nil)))
	assert.Equal(t, "x", sendemail.Unknown("p", 0, errors.New("x")).(*sendemail.SendError).Unwrap().Error())
}

var errTarget = errors.New("target")

type asTarget struct{}

func (*asTarget) Error() string { return "as target" }

// matcher matches errTarget and *asTarget through its own methods.
type matcher struct{}

func (matcher) Error() string        { return "matcher" }
func (matcher) Is(target error) bool { return target == errTarget }
func (matcher) As(target any) bool {
	if p, ok := target.(**asTarget); ok {
		*p = &asTarget{}
		return true
	}
	return false
}

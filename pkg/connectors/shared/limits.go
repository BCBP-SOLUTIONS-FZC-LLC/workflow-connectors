package shared

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

const (
	// MaxObjectBytes caps one storage object read into memory.
	MaxObjectBytes int64 = 50 << 20
	// MaxInlineBytes caps a fetched object returned inline instead of as a
	// document ref.
	MaxInlineBytes int64 = 1 << 20
	// MaxResponseBytes caps an internal service response body.
	MaxResponseBytes int64 = 10 << 20
	// MaxAttachmentBytes caps the total size of one email's attachments.
	MaxAttachmentBytes int64 = 25 << 20
	// DefaultHTTPTimeout bounds an internal call whose alias sets no timeout
	// (see CallTimeout), and every provider OAuth token request.
	DefaultHTTPTimeout = 30 * time.Second
	// ProviderHTTPTimeout bounds one call to a provider API (an email send
	// with up to 25 MiB of attachments) when the caller's context has no
	// earlier deadline.
	ProviderHTTPTimeout = 2 * time.Minute
)

// ErrTooLarge matches a payload over its size limit (always together with
// ErrValidation: retrying cannot help).
var ErrTooLarge = errors.New("connectors: payload too large")

// TooLarge reports that what exceeds limit.
func TooLarge(what string, limit int64) error {
	return fmt.Errorf("%w: %w: %s exceeds the %d-byte limit", ErrValidation, ErrTooLarge, what, limit)
}

// ReadAllLimited reads r fully, failing with TooLarge once it holds more than
// limit bytes — so at most limit+1 bytes are ever held. what names the
// payload in the error.
func ReadAllLimited(r io.Reader, limit int64, what string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, TooLarge(what, limit)
	}
	return b, nil
}

// InternalHTTPClient returns the client rest-call and sql-query send with: a
// copy of c, or a new client when c is nil, that never follows a redirect
// and speaks HTTP/1.1 only.
//
// Redirects: Go re-sends custom headers on a redirect, so following one would
// hand x-internal-token and x-departments to whichever host the response
// names.
//
// HTTP/1.1: Go's HTTP/2 client may replay a request body after a stream
// reset that does not prove the request was unprocessed (see
// ProviderHTTPClient), which would repeat a non-idempotent rest-call. With c
// nil, or c.Transport nil, the transport is a clone of
// http.DefaultTransport; when c.Transport is an *http.Transport it is cloned
// (the caller's transport is never modified, and the copy has its own
// connection pool). Either way the clone is set to HTTP/1.1 only. Any other
// http.RoundTripper (a tracing or metrics wrapper) is used as-is: the caller
// owns its protocol choice and must not enable HTTP/2 under it.
//
// The nil-client default carries no client-wide timeout: each call is bounded
// by CallTimeout (its alias's timeout, else DefaultHTTPTimeout), so an alias
// timeout longer than the default is honoured rather than cut short. A
// caller-supplied client keeps its own Timeout.
func InternalHTTPClient(c *http.Client) *http.Client {
	var out http.Client
	if c != nil {
		out = *c
	}
	switch t := out.Transport.(type) {
	case nil:
		out.Transport = http1Only(http.DefaultTransport.(*http.Transport))
	case *http.Transport:
		out.Transport = http1Only(t)
	}
	out.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &out
}

// http1Only returns a clone of t that never negotiates HTTP/2.
func http1Only(t *http.Transport) *http.Transport {
	transport := t.Clone()
	transport.ForceAttemptHTTP2 = false
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	if tc := transport.TLSClientConfig; tc != nil && slices.Contains(tc.NextProtos, "h2") {
		// A TLS config offering "h2" would let the server pick HTTP/2 in the
		// handshake, which an HTTP/1.1-only transport cannot then speak.
		// Transport.Clone cloned the config, but tls.Config.Clone shares the
		// NextProtos array with the caller's: build a new slice.
		tc.NextProtos = slices.DeleteFunc(slices.Clone(tc.NextProtos), func(p string) bool { return p == "h2" })
	}
	return transport
}

// ProviderHTTPClient returns a client for provider API calls: its own
// transport (so a retired cached client can drop its idle connections),
// bounded by ProviderHTTPTimeout, and never following a redirect — a
// redirect would re-send the request, and with OAuth transports its bearer
// token, to whichever host the response names.
//
// It speaks HTTP/1.1 only. Go's HTTP/2 client silently replays a request
// body when the server resets the stream with PROTOCOL_ERROR — a reset that
// does not prove the request was unprocessed — so an email send could be
// delivered twice and reported once. Over HTTP/1.1 a request is replayed only
// when nothing of it was written.
func ProviderHTTPClient() *http.Client {
	return &http.Client{
		Transport:     http1Only(http.DefaultTransport.(*http.Transport)),
		Timeout:       ProviderHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// CallTimeout is the deadline for one internal call: the alias's timeout when
// it sets one, else DefaultHTTPTimeout.
func CallTimeout(aliasTimeout time.Duration) time.Duration {
	if aliasTimeout > 0 {
		return aliasTimeout
	}
	return DefaultHTTPTimeout
}

package connectors

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// ChatCall records one call made against MockChatNotifyClient — Method is
// the chat-notify method name, Args its positional string arguments (users
// joined in for invite-to-channel).
type ChatCall struct {
	Method string
	Args   []string
}

// MockChatNotifyClient records every call it receives — a mutex-guarded
// recorder mock (mirrors platform-events/pkg/events/mock's own pattern) so a
// test can assert exactly what would have been sent, without any real
// provider.
type MockChatNotifyClient struct {
	mu    sync.Mutex
	calls []ChatCall
	err   error
}

func NewMockChatNotifyClient() *MockChatNotifyClient {
	return &MockChatNotifyClient{}
}

func (m *MockChatNotifyClient) Calls() []ChatCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ChatCall, len(m.calls))
	copy(out, m.calls)
	return out
}

func (m *MockChatNotifyClient) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockChatNotifyClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.err = nil
}

func (m *MockChatNotifyClient) CreateChannel(_ context.Context, name, visibility string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, ChatCall{Method: "create-channel", Args: []string{name, visibility}})
	if m.err != nil {
		return "", m.err
	}
	return uuid.New().String(), nil
}

func (m *MockChatNotifyClient) InviteToChannel(_ context.Context, channelNameOrID string, users []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, ChatCall{Method: "invite-to-channel", Args: append([]string{channelNameOrID}, users...)})
	return m.err
}

func (m *MockChatNotifyClient) PostMessage(_ context.Context, channelOrUser, thread, message string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, ChatCall{Method: "post-message", Args: []string{channelOrUser, thread, message}})
	if m.err != nil {
		return "", m.err
	}
	return uuid.New().String(), nil
}

var _ ChatNotifyProviderClient = (*MockChatNotifyClient)(nil)

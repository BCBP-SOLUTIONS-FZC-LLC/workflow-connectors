package sendemail

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type MockSendEmailClient struct {
	mu   sync.Mutex
	sent []EmailMessage
	err  error
}

func NewMockSendEmailClient() *MockSendEmailClient {
	return &MockSendEmailClient{}
}

func (m *MockSendEmailClient) Send(_ context.Context, msg EmailMessage) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	m.sent = append(m.sent, msg)
	return uuid.New().String(), nil
}

func (m *MockSendEmailClient) Sent() []EmailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]EmailMessage, len(m.sent))
	copy(out, m.sent)
	return out
}

func (m *MockSendEmailClient) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockSendEmailClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = nil
	m.err = nil
}

var _ ProviderClient = (*MockSendEmailClient)(nil)

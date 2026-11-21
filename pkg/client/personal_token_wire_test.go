package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestPersonalCredentialRejectionIsNotTransportTrustOrCapacity(t *testing.T) {
	if !PersonalCredentialRejected(fmt.Errorf("wrapped: %w", &AuthenticationError{Code: 2, Message: "authentication failed"})) {
		t.Fatal("definitive rejection not recognized")
	}
	for _, err := range []error{nil, context.DeadlineExceeded, errors.New("authentication failed"), &AuthenticationError{Code: 3, Message: "internal error"}, &AuthenticationError{Code: 8, Message: "rate limited"}, &UntrustedServerError{}, &ServerIdentityChangedError{}} {
		if PersonalCredentialRejected(err) {
			t.Fatal("uncertain failure classified as definitive rejection")
		}
	}
}

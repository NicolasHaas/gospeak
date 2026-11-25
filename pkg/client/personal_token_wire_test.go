package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestPersonalCredentialAdmissionRefusalClassification(t *testing.T) {
	refusal := &AuthenticationError{Code: 3, Message: "server session capacity reached"}
	if !PersonalCredentialAdmissionRefused(fmt.Errorf("wrapped: %w", refusal)) || PersonalCredentialRejected(refusal) {
		t.Fatal("known admission refusal classification lost")
	}
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, errors.New(refusal.Message), &AuthenticationError{Code: 2, Message: refusal.Message}, &AuthenticationError{Code: 3, Message: "internal error"}, &AuthenticationError{Code: 8, Message: "rate limited"}, &UntrustedServerError{}, &ServerIdentityChangedError{}} {
		if PersonalCredentialAdmissionRefused(err) {
			t.Fatalf("unrelated failure classified as retryable admission: %T", err)
		}
	}
}

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

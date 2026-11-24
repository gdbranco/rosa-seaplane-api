package transport

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestRHSSOAuthorizationHeaderIsAddedBeforeSigV4Signing(t *testing.T) {
	captured := &captureTransport{}
	sigv4 := New(captured, staticConfig(), "us-east-1", "123456789012", "arn:aws:iam::123456789012:user/test")
	sourceCalls := 0
	rt := NewRHSSOAuthorizationRoundTripper(sigv4, func(context.Context) (string, error) {
		sourceCalls++
		return "access-token", nil
	})

	req, _ := http.NewRequest(http.MethodPost, "https://example.com/api/v0/cloud_discovery/associations", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if sourceCalls != 1 {
		t.Fatalf("token source called %d times, want 1", sourceCalls)
	}
	if got := captured.got.Header.Get(HeaderRHSSOAuthorization); got != "Bearer access-token" {
		t.Fatalf("%s = %q, want bearer access token", HeaderRHSSOAuthorization, got)
	}
	if !inSignedHeaders(captured.got.Header.Get("Authorization"), "x-rh-sso-authorization") {
		t.Fatalf("%s was not signed by SigV4", HeaderRHSSOAuthorization)
	}
}

func TestRHSSOAuthorizationHeaderCanBeOmitted(t *testing.T) {
	captured := &captureTransport{}
	rt := NewRHSSOAuthorizationRoundTripper(captured, func(context.Context) (string, error) {
		return "", nil
	})

	req, _ := http.NewRequest(http.MethodGet, "https://example.com/api/v0/clusters", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := captured.got.Header.Get(HeaderRHSSOAuthorization); got != "" {
		t.Fatalf("%s = %q, want omitted", HeaderRHSSOAuthorization, got)
	}
}

func TestRHSSOTokenSourceErrorStopsRequest(t *testing.T) {
	captured := &captureTransport{}
	rt := NewRHSSOAuthorizationRoundTripper(captured, func(context.Context) (string, error) {
		return "", errors.New("token refresh failed")
	})

	req, _ := http.NewRequest(http.MethodPost, "https://example.com/api/v0/cloud_discovery/associations", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected token source error")
	}
	if captured.got != nil {
		t.Fatal("request was forwarded after token source failure")
	}
}

func TestRHSSOAuthorizationHeaderRejectsWhitespaceToken(t *testing.T) {
	captured := &captureTransport{}
	rt := NewRHSSOAuthorizationRoundTripper(captured, func(context.Context) (string, error) {
		return "not a jwt", nil
	})

	req, _ := http.NewRequest(http.MethodPost, "https://example.com/api/v0/cloud_discovery/associations", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected malformed token error")
	}
	if captured.got != nil {
		t.Fatal("request was forwarded with malformed token")
	}
}

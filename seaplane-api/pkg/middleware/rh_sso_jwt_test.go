package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/api"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/config"
)

func rhSSOFixture(t *testing.T) (config.RHSSOConfig, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	exponent := bigIntBytes(privateKey.E)
	jwks, err := json.Marshal(rhSSOJWKS{Keys: []rhSSOJWK{{
		Kid: "test-key",
		Kty: "RSA",
		Use: "sig",
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(exponent),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	jwksFile := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(jwksFile, jwks, 0600); err != nil {
		t.Fatal(err)
	}
	aclFile := filepath.Join(t.TempDir(), "acl.yml")
	if err := os.WriteFile(aclFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return config.RHSSOConfig{
		JWKSFile: jwksFile,
		ACLFile:  aclFile,
		Issuer:   "https://sso.example.test/realms/redhat-external",
	}, privateKey
}

func bigIntBytes(value int) []byte {
	var bytes []byte
	for value > 0 {
		bytes = append([]byte{byte(value)}, bytes...)
		value >>= 8
	}
	return bytes
}

func rhSSOToken(t *testing.T, key *rsa.PrivateKey, overrides map[string]any) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":                "https://sso.example.test/realms/redhat-external",
		"aud":                "cloud-services",
		"azp":                "cloud-services",
		"typ":                "Bearer",
		"sub":                "rh-user-123",
		"account_id":         "rh-account-456",
		"org_id":             "rh-org-789",
		"is_org_admin":       true,
		"preferred_username": "test-user",
		"iat":                now.Unix(),
		"exp":                now.Add(5 * time.Minute).Unix(),
	}
	for key, value := range overrides {
		claims[key] = value
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAWSAndRHSSOJWTHandlerRequiresBothIdentities(t *testing.T) {
	cfg, privateKey := rhSSOFixture(t)
	called := false
	requireRHSSO, err := NewAWSAndRHSSOJWTMiddleware(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)), WithRHSSOOrgAdminRequired())
	if err != nil {
		t.Fatal(err)
	}
	handler := Identity(requireRHSSO(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		identity, ok := GetRHSSOIdentity(r.Context())
		if !ok || identity.OrganizationID != "rh-org-789" || identity.Subject != "rh-user-123" || identity.AccountID != "rh-account-456" {
			t.Errorf("unexpected RH SSO identity: %+v (present=%v)", identity, ok)
		}
		w.WriteHeader(http.StatusCreated)
	})))

	tests := []struct {
		name       string
		accountID  string
		callerARN  string
		token      string
		wantStatus int
		wantCalled bool
	}{
		{
			name:       "valid AWS and RH SSO identities",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			token:      rhSSOToken(t, privateKey, nil),
			wantStatus: http.StatusCreated,
			wantCalled: true,
		},
		{
			name:       "missing AWS identity",
			token:      rhSSOToken(t, privateKey, nil),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "missing RH SSO token",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid RH SSO signature",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			token:      "Bearer not-a-jwt",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "audience and authorized party are not checked",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			token:      rhSSOToken(t, privateKey, map[string]any{"aud": nil, "azp": "another-client"}),
			wantStatus: http.StatusCreated,
			wantCalled: true,
		},
		{
			name:       "missing organization claim",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			token:      rhSSOToken(t, privateKey, map[string]any{"org_id": nil}),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "non-admin organization member",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			token:      rhSSOToken(t, privateKey, map[string]any{"is_org_admin": false}),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "ID token is not accepted",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			token:      rhSSOToken(t, privateKey, map[string]any{"typ": "ID"}),
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodPost, api.CloudDiscoveryAssociationsPath, nil)
			if tc.accountID != "" {
				req.Header.Set(HeaderAccountID, tc.accountID)
			}
			if tc.callerARN != "" {
				req.Header.Set(HeaderCallerARN, tc.callerARN)
			}
			if tc.token != "" {
				if tc.name == "invalid RH SSO signature" {
					req.Header.Set(HeaderRHSSOAuthorization, tc.token)
				} else {
					req.Header.Set(HeaderRHSSOAuthorization, "Bearer "+tc.token)
				}
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if called != tc.wantCalled {
				t.Fatalf("next called = %v, want %v", called, tc.wantCalled)
			}
		})
	}
}

func TestRHSSOIdentityDoesNotRequireAudienceOrAuthorizedParty(t *testing.T) {
	cfg := config.RHSSOConfig{
		Issuer: "https://sso.example.test/realms/redhat-external",
	}
	claims := jwt.MapClaims{
		"iss":          "https://sso.example.test/realms/redhat-external",
		"sub":          "user-1",
		"org_id":       "org-1",
		"is_org_admin": true,
	}
	if _, err := rhSSOIdentityFromClaims(claims, cfg, true); err != nil {
		t.Fatalf("valid claims rejected: %v", err)
	}
	claims["aud"] = []string{"other-api"}
	claims["azp"] = "another-client"
	if _, err := rhSSOIdentityFromClaims(claims, cfg, true); err != nil {
		t.Fatalf("claims with other audience/authorized party rejected: %v", err)
	}
	claims["iss"] = "https://other.example.test"
	if _, err := rhSSOIdentityFromClaims(claims, cfg, true); err == nil {
		t.Fatal("unexpected issuer was accepted")
	}
	claims["iss"] = cfg.Issuer
	claims["is_org_admin"] = false
	if _, err := rhSSOIdentityFromClaims(claims, cfg, false); err != nil {
		t.Fatalf("non-admin claims rejected when org-admin check is disabled: %v", err)
	}
	if _, err := rhSSOIdentityFromClaims(claims, cfg, true); err == nil {
		t.Fatal("non-admin claims accepted when org-admin check is enabled")
	}
}

func TestAWSAndRHSSOJWTHandlerOrgAdminCheckIsOptIn(t *testing.T) {
	cfg, privateKey := rhSSOFixture(t)
	called := false
	rhSSO, err := NewAWSAndRHSSOJWTMiddleware(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler := Identity(rhSSO(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		identity, ok := GetRHSSOIdentity(r.Context())
		if !ok || identity.IsOrgAdmin {
			t.Errorf("expected verified non-admin identity, got %+v (present=%v)", identity, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodPost, "/api/v0/test", nil)
	req.Header.Set(HeaderAccountID, "123456789012")
	req.Header.Set(HeaderCallerARN, "arn:aws:iam::123456789012:user/test")
	req.Header.Set(HeaderRHSSOAuthorization, "Bearer "+rhSSOToken(t, privateKey, map[string]any{"is_org_admin": false}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent || !called {
		t.Fatalf("status = %d, called = %v, want 204 and handler called: %s", recorder.Code, called, recorder.Body.String())
	}
}

func TestValidateRHSSOConfigRequiresCompleteTrustConfiguration(t *testing.T) {
	if err := ValidateRHSSOConfig(config.RHSSOConfig{}); err != nil {
		t.Fatalf("disabled configuration should be accepted: %v", err)
	}
	if err := ValidateRHSSOConfig(config.RHSSOConfig{JWKSURL: "http://not-tls"}); err == nil {
		t.Fatal("incomplete/insecure RH SSO configuration was accepted")
	}
	cfg, _ := rhSSOFixture(t)
	if err := os.WriteFile(cfg.ACLFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRHSSOConfig(cfg); err != nil {
		t.Fatalf("empty production ACL should be allowed alongside explicit claim checks: %v", err)
	}
}

func TestRHSSOJWTVerifierUsesFileWhenJWKSURLIsUnavailable(t *testing.T) {
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer jwksServer.Close()

	cfg, _ := rhSSOFixture(t)
	cfg.JWKSURL = jwksServer.URL
	verifier := &rhSSOJWTVerifier{
		cfg:    cfg,
		client: jwksServer.Client(),
		keys:   make(map[string]*rsa.PublicKey),
	}
	if _, err := verifier.key(context.Background(), "test-key"); err != nil {
		t.Fatalf("file JWKS fallback was not used: %v", err)
	}
}

func TestRHSSOJWTVerifierFailsWhenJWKSURLAndFileAreUnavailable(t *testing.T) {
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer jwksServer.Close()

	verifier := &rhSSOJWTVerifier{
		cfg:    config.RHSSOConfig{JWKSURL: jwksServer.URL},
		client: jwksServer.Client(),
		keys:   make(map[string]*rsa.PublicKey),
	}
	if _, err := verifier.key(context.Background(), "unknown"); err == nil {
		t.Fatal("validation continued without a working JWKS source")
	}
}

func TestRHSSOContextIdentity(t *testing.T) {
	identity := RHSSOIdentity{Subject: "subject", OrganizationID: "org"}
	ctx := WithRHSSOIdentity(context.Background(), identity)
	got, ok := GetRHSSOIdentity(ctx)
	if !ok || got != identity {
		t.Fatalf("identity = %+v, present=%v, want %+v", got, ok, identity)
	}
}

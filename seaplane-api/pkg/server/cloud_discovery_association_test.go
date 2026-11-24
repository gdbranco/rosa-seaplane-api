package server

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	seaplanev1alpha1 "github.com/openshift-online/rosa-seaplane-api/api/v1alpha1"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/api"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/clients/seaplanedb"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/config"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/middleware"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCloudDiscoveryAssociationRouteRequiresBothAuthMethods(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.NewConfig()
	cfg.RHSSO = serverRHSSOConfig(t)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := seaplanev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	db := seaplanedb.NewClientFrom(fake.NewClientBuilder().WithScheme(scheme).Build(), logger)
	srv, err := newConfiguredServer(t, cfg, db, logger)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		accountID  string
		callerARN  string
		wantStatus int
	}{
		{name: "missing SigV4 identity", wantStatus: http.StatusForbidden},
		{
			name:       "missing RH SSO token",
			accountID:  "123456789012",
			callerARN:  "arn:aws:iam::123456789012:user/test",
			wantStatus: http.StatusUnauthorized,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, api.CloudDiscoveryAssociationsPath, nil)
			if tc.accountID != "" {
				req.Header.Set(middleware.HeaderAccountID, tc.accountID)
			}
			if tc.callerARN != "" {
				req.Header.Set(middleware.HeaderCallerARN, tc.callerARN)
			}
			recorder := httptest.NewRecorder()
			srv.apiServer.Handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
		})
	}

	// The RH SSO middleware is attached only to the association subrouter;
	// ordinary SigV4/Cedar routes remain usable without an RH SSO token.
	req := httptest.NewRequest(http.MethodGet, "/api/v0/clusters", nil)
	req.Header.Set(middleware.HeaderAccountID, "123456789012")
	req.Header.Set(middleware.HeaderCallerARN, "arn:aws:iam::123456789012:user/test")
	recorder := httptest.NewRecorder()
	srv.apiServer.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ordinary SigV4 route status = %d, want 200 without RH SSO token: %s", recorder.Code, recorder.Body.String())
	}
}

func serverRHSSOConfig(t *testing.T) config.RHSSOConfig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(map[string]any{
		"keys": []map[string]string{{
			"kid": "server-test-key",
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jwksPath := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(jwksPath, jwks, 0600); err != nil {
		t.Fatal(err)
	}
	aclPath := filepath.Join(t.TempDir(), "acl.yml")
	if err := os.WriteFile(aclPath, []byte("- claim: azp\n  pattern: ^cloud-services$\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return config.RHSSOConfig{
		JWKSFile:        jwksPath,
		ACLFile:         aclPath,
		Issuer:          "https://sso.example.test/realms/redhat-external",
		Audience:        "cloud-services",
		AuthorizedParty: "cloud-services",
	}
}

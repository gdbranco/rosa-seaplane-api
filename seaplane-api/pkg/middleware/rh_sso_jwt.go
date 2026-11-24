package middleware

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/api"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/authz"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/config"
	"gopkg.in/yaml.v3"
)

const (
	// HeaderRHSSOAuthorization carries a normal RH SSO bearer JWT separately
	// from the Authorization header used by API Gateway for SigV4.
	HeaderRHSSOAuthorization = "X-RH-SSO-Authorization"
	rhSSOMaxJWKSBytes        = 1 << 20
	rhSSOMaxTokenBytes       = 16 << 10
	rhSSORefreshInterval     = time.Minute
)

const (
	rhSSOErrorCodeAWSIdentityRequired = "AWS-AUTH-001"
	rhSSOErrorCodeTokenRequired       = "RHSSO-AUTH-001"
	rhSSOErrorCodeClaimsDenied        = "RHSSO-AUTH-002"

	rhSSOErrorMessageAWSIdentityRequired = "A valid AWS account identity is required"
	rhSSOErrorMessageTokenRequired       = "A valid RH SSO JWT is required"
	rhSSOErrorMessageClaimsDenied        = "RH SSO JWT is not authorized for this route"
)

var (
	errAWSIdentityRequired = api.APIError{
		Code: rhSSOErrorCodeAWSIdentityRequired, HTTPStatus: http.StatusForbidden,
		Message: rhSSOErrorMessageAWSIdentityRequired,
	}
	errRHSSOTokenRequired = api.APIError{
		Code: rhSSOErrorCodeTokenRequired, HTTPStatus: http.StatusUnauthorized,
		Message: rhSSOErrorMessageTokenRequired,
	}
	errRHSSOClaimsDenied = api.APIError{
		Code: rhSSOErrorCodeClaimsDenied, HTTPStatus: http.StatusForbidden,
		Message: rhSSOErrorMessageClaimsDenied,
	}
)

const (
	rhSSOClaimIssuer               = "iss"
	rhSSOClaimSubject              = "sub"
	rhSSOClaimType                 = "typ"
	rhSSOClaimImpersonated         = "impersonated"
	rhSSOClaimExpiration           = "exp"
	rhSSOClaimIssuedAt             = "iat"
	rhSSOClaimOrganizationAdmin    = "is_org_admin"
	rhSSOClaimOrganizationID       = "org_id"
	rhSSOClaimOrganization         = "organization"
	rhSSOClaimOrganizationNestedID = "id"
	rhSSOClaimRHOrganizationID     = "rh-org-id"
	rhSSOClaimAccountID            = "account_id"
	rhSSOClaimUsername             = "username"
	rhSSOClaimPreferredUsername    = "preferred_username"
	rhSSOAuthorizationScheme       = "Bearer"
	rhSSOJWTKeyIDHeader            = "kid"
)

type rhSSOIdentityKey struct{}

// RHSSOIdentity contains only identity claims from a validated RH SSO JWT.
type RHSSOIdentity struct {
	Subject        string
	AccountID      string
	OrganizationID string
	Username       string
	IsOrgAdmin     bool
}

type RHSSOOption func(*rhSSOOptions)

type rhSSOOptions struct {
	requireOrgAdmin bool
}

// WithRHSSOOrgAdminRequired configures a route to require the verified
// is_org_admin claim to be true.
func WithRHSSOOrgAdminRequired() RHSSOOption {
	return func(options *rhSSOOptions) {
		options.requireOrgAdmin = true
	}
}

func WithRHSSOIdentity(ctx context.Context, identity RHSSOIdentity) context.Context {
	return context.WithValue(ctx, rhSSOIdentityKey{}, identity)
}

func GetRHSSOIdentity(ctx context.Context) (RHSSOIdentity, bool) {
	identity, ok := ctx.Value(rhSSOIdentityKey{}).(RHSSOIdentity)
	return identity, ok
}

type rhSSOACLRule struct {
	Claim   string `yaml:"claim"`
	Pattern string `yaml:"pattern"`
}

type rhSSOACLRuleCompiled struct {
	claim   string
	pattern *regexp.Regexp
}

type rhSSOJWKS struct {
	Keys []rhSSOJWK `json:"keys"`
}

type rhSSOJWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type rhSSOJWTVerifier struct {
	cfg     config.RHSSOConfig
	rules   []rhSSOACLRuleCompiled
	client  *http.Client
	mu      sync.RWMutex
	refresh sync.Mutex
	keys    map[string]*rsa.PublicKey
	loaded  time.Time
}

// ValidateRHSSOConfig validates the JWT trust configuration before the API
// server starts. A mounted ACL file may be empty, as in UHC's authentication
// handler. Issuer, subject, and organization checks are enforced in code;
// audience and authorized-party claims are not required.
func ValidateRHSSOConfig(cfg config.RHSSOConfig) error {
	if !cfg.Enabled() {
		return nil
	}
	if cfg.JWKSURL == "" && cfg.JWKSFile == "" {
		return fmt.Errorf("RH SSO requires %s or %s", config.RHSSOJWKSURLEnvName, config.RHSSOJWKSFileEnvName)
	}
	if cfg.ACLFile == "" {
		return fmt.Errorf("RH SSO requires %s", config.RHSSOACLFileEnvName)
	}
	if cfg.Issuer == "" {
		return fmt.Errorf("RH SSO requires %s", config.RHSSOIssuerEnvName)
	}
	if cfg.JWKSURL != "" {
		parsed, err := url.Parse(cfg.JWKSURL)
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
			return fmt.Errorf("%s must be an HTTPS URL", config.RHSSOJWKSURLEnvName)
		}
	}
	if cfg.JWKSFile != "" {
		info, err := os.Stat(cfg.JWKSFile)
		if err != nil {
			return fmt.Errorf("%s: %w", config.RHSSOJWKSFileEnvName, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", config.RHSSOJWKSFileEnvName)
		}
	}
	if _, err := loadRHSSOACL(cfg.ACLFile); err != nil {
		return fmt.Errorf("%s: %w", config.RHSSOACLFileEnvName, err)
	}
	return nil
}

func loadRHSSOACL(path string) ([]rhSSOACLRuleCompiled, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []rhSSOACLRule
	if err := yaml.Unmarshal(content, &rules); err != nil {
		return nil, err
	}
	compiled := make([]rhSSOACLRuleCompiled, 0, len(rules))
	for i, rule := range rules {
		if rule.Claim == "" || rule.Pattern == "" {
			return nil, fmt.Errorf("rule %d requires claim and pattern", i)
		}
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %d has invalid pattern: %w", i, err)
		}
		compiled = append(compiled, rhSSOACLRuleCompiled{claim: rule.Claim, pattern: pattern})
	}
	return compiled, nil
}

// NewAWSAndRHSSOJWTMiddleware creates middleware requiring both API Gateway's
// AWS identity and a normal RH SSO bearer JWT. API Gateway verifies SigV4; this
// middleware validates the forwarded AWS identity shape and independently
// verifies the JWT signature and claims.
func NewAWSAndRHSSOJWTMiddleware(cfg config.RHSSOConfig, logger *slog.Logger, options ...RHSSOOption) (func(http.Handler) http.Handler, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	rhSSOOptions := rhSSOOptions{}
	for _, option := range options {
		if option != nil {
			option(&rhSSOOptions)
		}
	}
	verifier, err := newRHSSOJWTVerifier(cfg)
	if err != nil {
		return nil, err
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			awsIdentity := authz.Identity{
				AccountID: GetAccountID(r.Context()),
				CallerARN: GetCallerARN(r.Context()),
			}
			if awsIdentity.AccountID == "" || awsIdentity.CallerARN == "" || awsIdentity.Validate() != nil {
				writeRHSSOAuthError(w, errAWSIdentityRequired)
				return
			}

			values := r.Header.Values(HeaderRHSSOAuthorization)
			if len(values) != 1 {
				writeRHSSOAuthError(w, errRHSSOTokenRequired)
				return
			}
			parts := strings.Fields(values[0])
			if len(parts) != 2 || !strings.EqualFold(parts[0], rhSSOAuthorizationScheme) || len(parts[1]) > rhSSOMaxTokenBytes {
				writeRHSSOAuthError(w, errRHSSOTokenRequired)
				return
			}

			claims, err := verifier.Verify(r.Context(), parts[1])
			if err != nil {
				logger.Info("RH SSO JWT validation failed", "path", r.URL.Path)
				writeRHSSOAuthError(w, errRHSSOTokenRequired)
				return
			}
			identity, err := rhSSOIdentityFromClaims(claims, cfg, rhSSOOptions.requireOrgAdmin)
			if err != nil {
				logger.Info("RH SSO JWT lacks account-association claims", "path", r.URL.Path)
				writeRHSSOAuthError(w, errRHSSOClaimsDenied)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithRHSSOIdentity(r.Context(), identity)))
		})
	}, nil
}

func newRHSSOJWTVerifier(cfg config.RHSSOConfig) (*rhSSOJWTVerifier, error) {
	if !cfg.Enabled() {
		return nil, fmt.Errorf("RH SSO JWT authentication is not configured")
	}
	if err := ValidateRHSSOConfig(cfg); err != nil {
		return nil, err
	}
	rules, err := loadRHSSOACL(cfg.ACLFile)
	if err != nil {
		return nil, fmt.Errorf("load RH SSO ACL: %w", err)
	}
	verifier := &rhSSOJWTVerifier{
		cfg:    cfg,
		rules:  rules,
		client: &http.Client{Timeout: 5 * time.Second},
		keys:   make(map[string]*rsa.PublicKey),
	}
	if err := verifier.refreshKeys(context.Background()); err != nil {
		return nil, fmt.Errorf("load RH SSO JWKS: %w", err)
	}
	return verifier, nil
}

func (v *rhSSOJWTVerifier) Verify(ctx context.Context, raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() && token.Method.Alg() != jwt.SigningMethodRS512.Alg() {
			return nil, fmt.Errorf("unsupported JWT signing algorithm")
		}
		kid, ok := token.Header[rhSSOJWTKeyIDHeader].(string)
		if !ok || kid == "" {
			return nil, fmt.Errorf("JWT kid is required")
		}
		return v.key(ctx, kid)
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg(), jwt.SigningMethodRS512.Alg()}))
	if err != nil || token == nil || !token.Valid {
		return nil, fmt.Errorf("invalid JWT")
	}
	if tokenType, exists := claims[rhSSOClaimType]; exists {
		value, ok := tokenType.(string)
		if !ok || !strings.EqualFold(value, rhSSOAuthorizationScheme) {
			return nil, fmt.Errorf("JWT type is invalid")
		}
	}
	if impersonated, exists := claims[rhSSOClaimImpersonated]; exists {
		value, ok := impersonated.(bool)
		if !ok || value {
			return nil, fmt.Errorf("impersonated JWTs are not accepted")
		}
	}
	if _, ok := claims[rhSSOClaimExpiration]; !ok || !claims.VerifyExpiresAt(time.Now().Unix(), true) {
		return nil, fmt.Errorf("JWT expiration is invalid")
	}
	if _, ok := claims[rhSSOClaimIssuedAt]; !ok || !claims.VerifyIssuedAt(time.Now().Unix(), true) {
		return nil, fmt.Errorf("JWT issued-at time is invalid")
	}
	if claims.Valid() != nil {
		return nil, fmt.Errorf("JWT time claims are invalid")
	}
	if !v.matchesACL(claims) {
		return nil, fmt.Errorf("JWT does not match the configured ACL")
	}
	return claims, nil
}

func (v *rhSSOJWTVerifier) matchesACL(claims jwt.MapClaims) bool {
	if len(v.rules) == 0 {
		return true
	}
	for _, rule := range v.rules {
		value, ok := claims[rule.claim].(string)
		if ok && rule.pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func (v *rhSSOJWTVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key := v.keys[kid]
	loaded := v.loaded
	v.mu.RUnlock()

	if key != nil && time.Since(loaded) < 5*time.Minute {
		return key, nil
	}
	if key == nil && time.Since(loaded) < rhSSORefreshInterval {
		return nil, fmt.Errorf("unknown JWT key id")
	}
	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	key = v.keys[kid]
	v.mu.RUnlock()
	if key == nil {
		return nil, fmt.Errorf("unknown JWT key id")
	}
	return key, nil
}

func (v *rhSSOJWTVerifier) refreshKeys(ctx context.Context) error {
	v.refresh.Lock()
	defer v.refresh.Unlock()

	if v.cfg.JWKSURL != "" {
		keys, err := v.loadJWKSURL(ctx)
		if err == nil {
			if len(keys) == 0 {
				return fmt.Errorf("JWKS endpoint contains no supported signing keys")
			}
			v.replaceKeys(keys)
			return nil
		}
		if v.cfg.JWKSFile == "" {
			return err
		}
		fallback, fallbackErr := loadRHSSOKeysFile(v.cfg.JWKSFile)
		if fallbackErr != nil {
			return fmt.Errorf("JWKS endpoint failed (%v) and file fallback failed: %w", err, fallbackErr)
		}
		if len(fallback) == 0 {
			return fmt.Errorf("JWKS endpoint failed (%v) and file fallback has no supported signing keys", err)
		}
		v.replaceKeys(fallback)
		return nil
	}

	keys, err := loadRHSSOKeysFile(v.cfg.JWKSFile)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("JWKS contains no supported signing keys")
	}
	v.replaceKeys(keys)
	return nil
}

func (v *rhSSOJWTVerifier) loadJWKSURL(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned HTTP %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, rhSSOMaxJWKSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > rhSSOMaxJWKSBytes {
		return nil, fmt.Errorf("JWKS response exceeds size limit")
	}
	keys := make(map[string]*rsa.PublicKey)
	if err := addRHSSOJWKS(keys, content); err != nil {
		return nil, fmt.Errorf("parse JWKS response: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS endpoint contains no supported signing keys")
	}
	return keys, nil
}

func loadRHSSOKeysFile(path string) (map[string]*rsa.PublicKey, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey)
	if err := addRHSSOJWKS(keys, content); err != nil {
		return nil, fmt.Errorf("parse JWKS file: %w", err)
	}
	return keys, nil
}

func (v *rhSSOJWTVerifier) replaceKeys(keys map[string]*rsa.PublicKey) {
	v.mu.Lock()
	v.keys = keys
	v.loaded = time.Now()
	v.mu.Unlock()
}

func addRHSSOJWKS(dst map[string]*rsa.PublicKey, content []byte) error {
	var document rhSSOJWKS
	if err := json.Unmarshal(content, &document); err != nil {
		return err
	}
	for _, jwk := range document.Keys {
		if jwk.Kid == "" || jwk.Kty != "RSA" || jwk.Use != "sig" {
			continue
		}
		if jwk.Alg != jwt.SigningMethodRS256.Alg() && jwk.Alg != jwt.SigningMethodRS512.Alg() {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil || len(nBytes) == 0 {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil || len(eBytes) == 0 {
			continue
		}
		exponent := new(big.Int).SetBytes(eBytes)
		if !exponent.IsInt64() {
			continue
		}
		e := exponent.Int64()
		if e < 3 || e%2 == 0 || e > int64(int(^uint(0)>>1)) {
			continue
		}
		key := &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e)}
		if key.N.Sign() <= 0 {
			continue
		}
		if previous, exists := dst[jwk.Kid]; exists && (previous.N.Cmp(key.N) != 0 || previous.E != key.E) {
			return fmt.Errorf("JWKS contains conflicting keys for kid %q", jwk.Kid)
		}
		dst[jwk.Kid] = key
	}
	return nil
}

func rhSSOIdentityFromClaims(claims jwt.MapClaims, cfg config.RHSSOConfig, requireOrgAdmin bool) (RHSSOIdentity, error) {
	issuer, _ := claims[rhSSOClaimIssuer].(string)
	if issuer == "" || issuer != cfg.Issuer {
		return RHSSOIdentity{}, fmt.Errorf("unexpected issuer")
	}
	subject, _ := claims[rhSSOClaimSubject].(string)
	if subject == "" {
		return RHSSOIdentity{}, fmt.Errorf("missing subject")
	}
	orgAdmin, hasOrgAdminClaim := claims[rhSSOClaimOrganizationAdmin].(bool)
	if requireOrgAdmin && (!hasOrgAdminClaim || !orgAdmin) {
		return RHSSOIdentity{}, fmt.Errorf("organization administrator is required")
	}
	organizationID, _ := claims[rhSSOClaimOrganizationID].(string)
	if organizationID == "" {
		if organization, ok := claims[rhSSOClaimOrganization].(map[string]interface{}); ok {
			organizationID, _ = organization[rhSSOClaimOrganizationNestedID].(string)
		}
	}
	if organizationID == "" {
		organizationID, _ = claims[rhSSOClaimRHOrganizationID].(string)
	}
	if organizationID == "" {
		return RHSSOIdentity{}, fmt.Errorf("missing organization")
	}
	accountID, _ := claims[rhSSOClaimAccountID].(string)
	username, _ := claims[rhSSOClaimUsername].(string)
	if username == "" {
		username, _ = claims[rhSSOClaimPreferredUsername].(string)
	}
	return RHSSOIdentity{
		Subject:        subject,
		AccountID:      accountID,
		OrganizationID: organizationID,
		Username:       username,
		IsOrgAdmin:     orgAdmin,
	}, nil
}

func writeRHSSOAuthError(w http.ResponseWriter, apiError api.APIError) {
	_ = api.WriteError(w, apiError)
}

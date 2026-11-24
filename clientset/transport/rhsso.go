/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package transport

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// HeaderRHSSOAuthorization carries an RH SSO bearer token independently of
// Authorization, which SigV4 uses.
const HeaderRHSSOAuthorization = "X-RH-SSO-Authorization"

// RHSSOTokenSource returns a current RH SSO access token. The returned value
// may be a raw JWT or a "Bearer <JWT>" value.
type RHSSOTokenSource func(context.Context) (string, error)

type rhSSOAuthorizationRoundTripper struct {
	inner  http.RoundTripper
	source RHSSOTokenSource
}

// NewRHSSOAuthorizationRoundTripper adds the RH SSO bearer header and forwards
// the request to inner. Place this outside the SigV4 transport so the added
// header is included in SigV4's SignedHeaders list.
func NewRHSSOAuthorizationRoundTripper(inner http.RoundTripper, source RHSSOTokenSource) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	if source == nil {
		return inner
	}
	return &rhSSOAuthorizationRoundTripper{inner: inner, source: source}
}

func (t *rhSSOAuthorizationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header = req.Header.Clone()

	token, err := t.source(req.Context())
	if err != nil {
		return nil, fmt.Errorf("rh-sso: retrieving access token: %w", err)
	}
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("bearer "):])
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("rh-sso: access token must be a single JWT")
	}
	if token == "" {
		req.Header.Del(HeaderRHSSOAuthorization)
	} else {
		req.Header.Set(HeaderRHSSOAuthorization, "Bearer "+token)
	}
	return t.inner.RoundTrip(req)
}

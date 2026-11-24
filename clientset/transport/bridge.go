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
	"net/http"
	"strconv"
	"strings"
)

// Adapter wraps an inner RoundTripper. It adjusts generated resource paths and
// pagination query parameters, then forwards requests and responses unchanged
// (the API speaks K8s-native JSON on both sides, so no body translation is needed).
type Adapter struct {
	inner         http.RoundTripper
	resourcePaths map[string]string
}

// NewAdapter returns an Adapter that wraps inner.
func NewAdapter(inner http.RoundTripper) *Adapter {
	return NewAdapterWithResourcePaths(inner, nil)
}

// NewAdapterWithResourcePaths returns an Adapter that also maps generated
// single-segment resource names to nested Platform API paths.
func NewAdapterWithResourcePaths(inner http.RoundTripper, resourcePaths map[string]string) *Adapter {
	paths := make(map[string]string, len(resourcePaths))
	for resource, path := range resourcePaths {
		paths[resource] = path
	}
	return &Adapter{inner: inner, resourcePaths: paths}
}

// RoundTrip implements http.RoundTripper. It adjusts generated resource paths
// and pagination query parameters, then forwards the request and response
// unchanged. The seaplane-api speaks K8s-native JSON end-to-end.
func (a *Adapter) RoundTrip(req *http.Request) (*http.Response, error) {
	req = a.adaptResourcePath(req)
	req = a.adaptListQuery(req)
	return a.inner.RoundTrip(req)
}

// adaptResourcePath translates generated single-segment resource paths into
// nested Platform API paths. This runs before the inner transport signs the
// request, so SigV4 covers the final URL.
func (a *Adapter) adaptResourcePath(req *http.Request) *http.Request {
	if len(a.resourcePaths) == 0 {
		return req
	}
	segments := strings.Split(req.URL.Path, "/")
	changed := false
	for i, segment := range segments {
		if path, ok := a.resourcePaths[segment]; ok {
			segments[i] = path
			changed = true
		}
	}
	if !changed {
		return req
	}
	req = req.Clone(req.Context())
	req.URL.Path = strings.Join(segments, "/")
	req.URL.RawPath = ""
	return req
}

// adaptListQuery translates the Kubernetes-style ?continue=N query parameter to
// the platform API's ?offset=N parameter for GET (list) requests.
//
// The Kubernetes generated client serializes metav1.ListOptions.Continue as the
// "continue" query parameter. The Seaplane platform API uses offset-based
// pagination instead, accepting an integer "offset" parameter. platform.ListOptions
// encodes the integer offset as a numeric string in ListOptions.Continue before
// calling the inner client; this method rewrites the parameter accordingly.
//
// Non-numeric "continue" values are passed through unchanged.
func (a *Adapter) adaptListQuery(req *http.Request) *http.Request {
	if req.Method != http.MethodGet {
		return req
	}
	q := req.URL.Query()
	cont := q.Get("continue")
	if cont == "" {
		return req
	}
	if _, err := strconv.ParseInt(cont, 10, 64); err != nil {
		return req
	}
	req = req.Clone(req.Context())
	q.Del("continue")
	q.Set("offset", cont)
	req.URL.RawQuery = q.Encode()
	return req
}

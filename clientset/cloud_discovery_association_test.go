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

package seaplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openshift-online/rosa-seaplane-api/api/v1alpha1/public"
	"github.com/openshift-online/rosa-seaplane-api/clientset/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGeneratedCloudDiscoveryAssociationClientUsesNestedRoute(t *testing.T) {
	const (
		roleARN = "arn:aws:iam::123456789012:role/SeaplaneCloudDiscovery"
		token   = "test-access-token"
	)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != http.MethodPost {
			t.Errorf("request method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/prod/api/v0/cloud_discovery/associations" {
			t.Errorf("request path = %s, want /prod/api/v0/cloud_discovery/associations", r.URL.Path)
		}
		if got := r.Header.Get("X-RH-SSO-Authorization"); got != "Bearer "+token {
			t.Errorf("RH SSO authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "AWS4-HMAC-SHA256 ") {
			t.Errorf("Authorization = %q, want SigV4", got)
		}

		var body struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				RoleARN string `json:"roleArn"`
			} `json:"spec"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body.Metadata.Name != "role-association" || body.Spec.RoleARN != roleARN {
			t.Errorf("request body = %+v, want metadata.name and spec.roleArn", body)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{
			"apiVersion":"seaplane.io/v1alpha1",
			"kind":"CloudDiscoveryAssociation",
			"metadata":{"name":"role-association","creationTimestamp":"2026-11-24T12:00:00Z"},
			"spec":{"roleArn":"`+roleARN+`"},
			"status":{"organizationId":"org-123"}
		}`)
	}))
	defer server.Close()

	cfg := validConfig()
	cfg.Host = server.URL + "/prod"
	cfg.Region = "us-east-1"
	cfg.RHSSOTokenSource = func(context.Context) (string, error) { return token, nil }
	client, err := NewForConfig(cfg)
	if err != nil {
		t.Fatalf("NewForConfig(): %v", err)
	}

	association, err := client.SeaplaneV1alpha1().CloudDiscoveryAssociations().Create(
		context.Background(),
		&public.CloudDiscoveryAssociation{
			ObjectMeta: metav1.ObjectMeta{Name: "role-association"},
			Spec:       public.CloudDiscoveryAssociationSpec{RoleARN: roleARN},
		},
		platform.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
	if association.Name != "role-association" ||
		association.Spec.RoleARN != roleARN ||
		association.Status.OrganizationID != "org-123" ||
		association.CreationTimestamp.IsZero() {
		t.Fatalf("unexpected association response: %+v", association)
	}
}

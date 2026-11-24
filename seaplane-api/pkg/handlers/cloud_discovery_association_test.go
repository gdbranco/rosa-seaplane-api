package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	seaplanev1alpha1 "github.com/openshift-online/rosa-seaplane-api/api/v1alpha1"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/api"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/clients/seaplanedb"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/middleware"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func cloudDiscoveryAssociationFixture(t *testing.T) (*CloudDiscoveryAssociationHandler, *seaplanedb.Client) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := seaplanedb.NewClientFrom(
		fake.NewClientBuilder().WithScheme(newTestScheme()).Build(),
		logger,
	)
	return NewCloudDiscoveryAssociationHandler(db, logger), db
}

func TestCloudDiscoveryAssociationCreateAndSingleAssociation(t *testing.T) {
	handler, db := cloudDiscoveryAssociationFixture(t)
	const awsAccountID = "123456789012"
	request := func(roleARN string) *httptest.ResponseRecorder {
		body := `{"metadata":{"name":"role-association"},"spec":{"roleArn":"` + roleARN + `"}}`
		req := httptest.NewRequest(http.MethodPost, api.CloudDiscoveryAssociationsPath, strings.NewReader(body))
		ctx := testContext(awsAccountID)
		ctx = middleware.WithRHSSOIdentity(ctx, middleware.RHSSOIdentity{
			Subject:        "rh-user-123",
			AccountID:      "rh-account-456",
			OrganizationID: "rh-org-789",
			Username:       "alice",
			IsOrgAdmin:     true,
		})
		recorder := httptest.NewRecorder()
		handler.Create(recorder, req.WithContext(ctx))
		return recorder
	}

	roleARN := "arn:aws:iam::" + awsAccountID + ":role/SeaplaneCloudDiscovery"
	created := request(roleARN)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", created.Code, created.Body.String())
	}
	var response struct {
		Metadata struct {
			Name              string    `json:"name"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			RoleARN string `json:"roleArn"`
		} `json:"spec"`
		Status struct {
			OrganizationID string `json:"organizationId"`
		} `json:"status"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if response.Metadata.Name != "role-association" ||
		response.Metadata.CreationTimestamp.IsZero() ||
		response.Spec.RoleARN != roleARN ||
		response.Status.OrganizationID != "rh-org-789" {
		t.Fatalf("unexpected create response: %+v", response)
	}

	stored, err := db.GetCloudDiscoveryAssociation(testContext(awsAccountID))
	if err != nil {
		t.Fatalf("get stored association: %v", err)
	}
	if stored.Namespace != "account-"+awsAccountID ||
		stored.Labels[seaplanev1alpha1.AccountIDLabel] != awsAccountID ||
		stored.Spec.RoleARN != roleARN ||
		stored.Status.OrganizationID != "rh-org-789" ||
		stored.CreationTimestamp.IsZero() {
		t.Fatalf("unexpected stored association: %+v", stored)
	}

	conflict := request(roleARN)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("second create status = %d, want 409: %s", conflict.Code, conflict.Body.String())
	}
}

func TestCloudDiscoveryAssociationRejectsCrossAccountRole(t *testing.T) {
	handler, db := cloudDiscoveryAssociationFixture(t)
	ctx := middleware.WithRHSSOIdentity(testContext("123456789012"), middleware.RHSSOIdentity{
		Subject:        "rh-user-123",
		OrganizationID: "rh-org-789",
		IsOrgAdmin:     true,
	})
	req := httptest.NewRequest(
		http.MethodPost,
		api.CloudDiscoveryAssociationsPath,
		strings.NewReader(`{"metadata":{"name":"role-association"},"spec":{"roleArn":"arn:aws:iam::999999999999:role/SeaplaneCloudDiscovery"}}`),
	)
	recorder := httptest.NewRecorder()
	handler.Create(recorder, req.WithContext(ctx))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-account role status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := db.GetCloudDiscoveryAssociation(testContext("123456789012")); !seaplanedb.IsNotFound(err) {
		t.Fatalf("cross-account attempt created association: err=%v", err)
	}
}

func TestCloudDiscoveryAssociationRejectsInvalidBodyAndRole(t *testing.T) {
	handler, _ := cloudDiscoveryAssociationFixture(t)
	ctx := middleware.WithRHSSOIdentity(testContext("123456789012"), middleware.RHSSOIdentity{
		Subject:        "rh-user-123",
		OrganizationID: "rh-org-789",
		IsOrgAdmin:     true,
	})
	for _, body := range []string{
		`{"metadata":{"name":"role-association"},"spec":{"roleArn":"arn:aws:iam::123456789012:role/one","awsAccountId":"123456789012"}}`,
		`{"metadata":{"name":"role-association"},"spec":{"roleArn":"not-an-arn"}}`,
		`{"metadata":{"name":"role-association"},"spec":{"roleArn":"arn:aws:iam::123456789012:user/not-a-role"}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, api.CloudDiscoveryAssociationsPath, strings.NewReader(body))
		recorder := httptest.NewRecorder()
		handler.Create(recorder, req.WithContext(ctx))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400: %s", body, recorder.Code, recorder.Body.String())
		}
	}
}

func TestCloudDiscoveryAssociationRejectsMissingSSOIdentity(t *testing.T) {
	handler, _ := cloudDiscoveryAssociationFixture(t)
	req := httptest.NewRequest(
		http.MethodPost,
		api.CloudDiscoveryAssociationsPath,
		strings.NewReader(`{"metadata":{"name":"role-association"},"spec":{"roleArn":"arn:aws:iam::123456789012:role/SeaplaneCloudDiscovery"}}`),
	)
	recorder := httptest.NewRecorder()
	handler.Create(recorder, req.WithContext(testContext("123456789012")))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
}

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/openshift-online/rosa-seaplane-api/api/v1alpha1"
	public "github.com/openshift-online/rosa-seaplane-api/api/v1alpha1/public"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/api"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/clients/seaplanedb"
	conversionv1alpha1 "github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/conversion/v1alpha1"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/middleware"
)

const (
	awsCommercialPartition = "aws"
	awsIAMService          = "iam"
	iamRoleResourcePrefix  = "role/"
	iamRolePathPattern     = `^[A-Za-z0-9+=,.@_-]{1,64}$`

	errAssociationJSONRequired = "JSON object is required"
	errAssociationSingleJSON   = "single JSON object is required"

	logAssociationCreateFailed        = "failed to create cloud discovery role association"
	logAssociationResponseWriteFailed = "failed to write cloud discovery association response"
	logFieldAWSAccountID              = "aws_account_id"
	logFieldOrganizationID            = "organization_id"
	logFieldError                     = "error"
)

type CloudDiscoveryAssociationHandler struct {
	db     *seaplanedb.Client
	logger *slog.Logger
}

func NewCloudDiscoveryAssociationHandler(db *seaplanedb.Client, logger *slog.Logger) *CloudDiscoveryAssociationHandler {
	return &CloudDiscoveryAssociationHandler{db: db, logger: logger}
}

// Create handles POST /api/v0/cloud_discovery/associations. The AWS account
// and RH SSO organization are derived from authenticated request context.
func (h *CloudDiscoveryAssociationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req public.CloudDiscoveryAssociation
	if err := decodeCloudDiscoveryAssociationRequest(r, &req); err != nil ||
		strings.TrimSpace(req.Spec.RoleARN) != req.Spec.RoleARN ||
		(req.Name != "" && req.Name != "role-association") ||
		req.Namespace != "" ||
		!req.CreationTimestamp.IsZero() ||
		req.Status.OrganizationID != "" {
		writeAPIError(w, ErrCloudDiscoveryAssociationInvalidBody, h.logger)
		return
	}

	awsAccountID := middleware.GetAccountID(r.Context())
	rhSSOIdentity, ok := middleware.GetRHSSOIdentity(r.Context())
	if awsAccountID == "" || !ok {
		writeAPIError(w, ErrCloudDiscoveryAssociationIdentity, h.logger)
		return
	}
	roleAccountID, validRole := roleARNAccountID(req.Spec.RoleARN)
	if !validRole {
		writeAPIError(w, ErrCloudDiscoveryAssociationInvalidRole, h.logger)
		return
	}
	if roleAccountID != awsAccountID {
		writeAPIError(w, ErrCloudDiscoveryAssociationCrossAccount, h.logger)
		return
	}
	if h.db == nil {
		writeAPIError(w, ErrCloudDiscoveryAssociationCreateFailed, h.logger)
		return
	}

	spec := v1alpha1.CloudDiscoveryAssociationSpec{
		RoleARN: req.Spec.RoleARN,
	}
	status := v1alpha1.CloudDiscoveryAssociationStatus{
		OrganizationID: rhSSOIdentity.OrganizationID,
	}
	association, err := h.db.CreateCloudDiscoveryAssociation(r.Context(), spec, status)
	if seaplanedb.IsAlreadyExists(err) {
		writeAPIError(w, ErrCloudDiscoveryAssociationConflict, h.logger)
		return
	}
	if err != nil {
		h.logger.Error(logAssociationCreateFailed,
			logFieldAWSAccountID, awsAccountID,
			logFieldOrganizationID, rhSSOIdentity.OrganizationID,
			logFieldError, err,
		)
		writeAPIError(w, ErrCloudDiscoveryAssociationCreateFailed, h.logger)
		return
	}

	if err := api.Write(w, http.StatusCreated, conversionv1alpha1.ProjectCloudDiscoveryAssociation(association)); err != nil {
		h.logger.Error(logAssociationResponseWriteFailed,
			logFieldAWSAccountID, awsAccountID,
			logFieldError, err,
		)
	}
}

func decodeCloudDiscoveryAssociationRequest(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if string(raw) == "null" {
		return errors.New(errAssociationJSONRequired)
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New(errAssociationSingleJSON)
	}
	return nil
}

func roleARNAccountID(value string) (string, bool) {
	parsed, err := arn.Parse(value)
	if err != nil || parsed.Partition != awsCommercialPartition || parsed.Service != awsIAMService || parsed.Region != "" {
		return "", false
	}
	if !accountIDPattern.MatchString(parsed.AccountID) || !strings.HasPrefix(parsed.Resource, iamRoleResourcePrefix) {
		return "", false
	}
	roleName := strings.TrimPrefix(parsed.Resource, iamRoleResourcePrefix)
	if len(roleName) > 512 || roleName == "" {
		return "", false
	}
	for _, part := range strings.Split(roleName, "/") {
		if part == "." || part == ".." || !iamRolePathSegmentPattern.MatchString(part) {
			return "", false
		}
	}
	return parsed.AccountID, true
}

var iamRolePathSegmentPattern = regexp.MustCompile(iamRolePathPattern)

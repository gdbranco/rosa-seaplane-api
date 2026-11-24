# Cloud discovery role association POC

This POC exposes `POST /api/v0/cloud_discovery/associations`. It creates one
association per AWS account between the account and a customer-supplied IAM
role ARN. It does not yet assume or validate the role with STS.

## Request and identity

The generated client sends a resource-shaped request:

```json
{
  "metadata": {
    "name": "role-association"
  },
  "spec": {
    "roleArn": "arn:aws:iam::123456789012:role/SeaplaneCloudDiscovery"
  }
}
```

The public resource is generated from the internal API type. `client-gen`
generates the typed Create operation, and `bridge-gen` exposes it as
`CloudDiscoveryAssociations().Create(...)`. The client uses the single-segment
resource name `cloud_discovery_associations`; the generated transport adapter
rewrites that segment to `cloud_discovery/associations` before the RH SSO and
SigV4 transports run, preserving the nested route for this and future
cloud-discovery resources. The 201 response is the same resource shape, with
`metadata.creationTimestamp` and `status.organizationId` set by the API.

Run `make generate` in `rosa-seaplane-api` to regenerate the public API types,
clientset, OpenAPI schemas, and pathbind draft. OpenAPI schemas are generated
from the public types; do not edit the generated OpenAPI file by hand. When the
API/clientset candidate is available, ROSA consumes it and runs `make generate`
to refresh its pathbind helpers and mocks.

The route requires two independent credentials:

- API Gateway verifies SigV4 and supplies `X-Amz-Account-Id` and
  `X-Amz-Caller-Arn`.
- The client sends an RH SSO **access token** in
  `X-RH-SSO-Authorization: Bearer <JWT>`. SigV4 remains in the ordinary
  `Authorization` header.

The account ID is derived only from the verified gateway identity; it is not
accepted in the body or URL. The role ARN must be an IAM role in that exact
AWS account. The JWT signature must validate against the configured JWKS and
the `iss` claim must match the configured issuer. A nonempty `sub` and an
organization claim (`org_id`, `organization.id`, or `rh-org-id`) are required.
The API does not validate `aud` or `azp`, matching UHC Clusters Service. For
ROSA's OCM access token, `org_id` and `organization.id` identify the
organization's external ID, not its internal AMS ID. The verified external ID
is persisted as the association's target; other user identity claims are not
stored. No Accounts Management lookup is performed.

Org-admin enforcement is a functional middleware option. It is disabled on the
association route while testing with a non-admin account. A route that requires
an org admin passes `middleware.WithRHSSOOrgAdminRequired()` to
`NewAWSAndRHSSOJWTMiddleware`, then attaches it with the subrouter's `.Use()`;
the middleware then requires `is_org_admin: true`
before calling its handler.

The decoded ID-token example with `typ: "ID"` is not accepted. Send the normal
bearer access token used by Clusters Service. The API does not require that the
token contain `aud` or `azp`.

## RH SSO configuration

Set the following flags (or corresponding environment variables) to enable the
route:

| Flag | Environment variable | Purpose |
| --- | --- | --- |
| `--rh-sso-jwks-url` | `RH_SSO_JWKS_URL` | HTTPS RH SSO certs/JWKS endpoint |
| `--rh-sso-jwks-file` | `RH_SSO_JWKS_FILE` | Local JWKS file; can be used with the URL |
| `--rh-sso-acl-file` | `RH_SSO_ACL_FILE` | UHC-style claim ACL file; an empty file is allowed |
| `--rh-sso-jwt-issuer` | `RH_SSO_JWT_ISSUER` | Exact token `iss` |

The API Helm chart supplies these settings under `platformApi.app.rhSso` and
enables the POC by default. Its defaults use the Red Hat external realm JWKS
URL and issuer, and an empty ACL file. The ACL file must exist, but an empty
ACL means no additional claim-pattern restriction. Deployments can override
these values through Helm values, including the regional-cluster values in
`rosa-seaplane`.

The route is not registered if no RH SSO configuration is supplied. Partial or
invalid configuration fails startup. JWKS errors fail closed, including
refresh failures when no local snapshot is configured. The URL is authoritative
when it returns a valid JWKS; the local file is used only if the URL is
unavailable, and its keys are not merged with a healthy URL response. RSA
`RS256` and `RS512` signing keys are accepted.
ACL rules follow UHC semantics: a token is ACL-allowed if any configured rule
matches. Issuer, subject, and organization checks remain mandatory; `aud` and
`azp` are not checked. Org-admin checking is independently enabled per route
through the middleware option.

## Storage and limits

The record is stored as an internal typed seaplane-db object in
`account-<aws-account-id>`, with a fixed name. The database primary key makes
creation atomic: the first request succeeds; later creates return `409`.
The public collection path currently implements only `POST`; there are no
get/list, update, or delete routes in this POC.

The POC stores the role ARN in `spec`, the organization external ID in
server-managed `status.organizationId`, and the creation time in
`metadata.creationTimestamp`. The AWS account is represented by the account-
scoped namespace and label. It does not store RH SSO user identifiers, JWTs, or
AWS credentials. Trust-policy validation, an STS assume-role check, role
rotation, and revocation are future work. The role ARN is create-only; the POC
has no update or delete operation.

## Planned operator access model

The account association is intended to identify the customer-side role that the
operator assumes. It is separate from the operator pod's own AWS identity:

```text
seaplane-operator ServiceAccount
  → EKS Pod Identity
  → Seaplane regional IAM role (source)
  → STS AssumeRole using this association's roleArn
  → customer IAM role (target)
  → customer AWS APIs
```

### Seaplane source role

The regional EKS deployment binds the `seaplane` namespace's
`seaplane-operator` ServiceAccount to a regional IAM role with an EKS Pod
Identity association. The role trusts the `pods.eks.amazonaws.com` service
principal for `sts:AssumeRole` and `sts:TagSession`. This binding is provisioned
in the `rosa-seaplane` repository's Terraform configuration
(`terraform/config/regional-cluster/main.tf`), not by this API resource or a
Helm-created AWS association. Helm must still deploy the ServiceAccount with
the matching namespace and name. The EKS Pod Identity Agent must be installed.
The AWS SDK default credential chain in the operator obtains and refreshes the
pod's temporary source credentials; no static keys or customer role ARN are
injected into the pod.

The source role needs permission to call `sts:AssumeRole` on approved customer
roles, as well as the permissions it needs for Seaplane-owned resources. The
regional Terraform policy in the `rosa-seaplane` repository limits target roles
using the `seaplane-access=true` IAM role tag. If that constraint is retained,
customer roles must carry the tag.

### Customer target role

`spec.roleArn` is the customer-side target role. On any operator AWS call, the
operator must resolve the association from the resource's owning AWS account
and verify that the account ID in the role ARN matches the Cluster's
service-set account ID and account namespace/label. A NodePool resolves the
same association through its UID-validated parent Cluster. Reject mismatches
before calling STS or making customer-account API calls.

The customer role's trust policy must name the Seaplane source role and require
the exact, service-owned AWS STS ExternalId for this account association. For
example:

```json
{
  "Effect": "Allow",
  "Principal": {
    "AWS": "arn:aws:iam::<regional-account>:role/<regional-id>-seaplane-operator"
  },
  "Action": "sts:AssumeRole",
  "Condition": {
    "StringEquals": {
      "sts:ExternalId": "<stable-service-issued-id>"
    }
  }
}
```

The AWS STS ExternalId is distinct from `status.organizationId`, which is the
organization identifier derived from RH SSO. The ExternalId must be stable for
the shared account association, passed in every AssumeRole request, and
available to the customer when they configure the role trust. It is not an AWS
credential. Account-ID validation is still required; ExternalId does not prove
that a role belongs to the Cluster's account.

The customer role's permission policy must cover the approved consumers of the
association. Examples include EC2 describe operations for preflight and VPC
discovery, `secretsmanager:GetSecretValue` scoped to an unmanaged OIDC
configuration's `secretArn`, and `iam:GetRole` for the planned trust-policy
check. Any write-probe permissions must be explicitly reviewed and bounded.
The operator uses the customer role to read the customer's OIDC key; storing
the copied key in Seaplane's Secrets Manager remains an operation under the
Seaplane source role.

The operator should cache refreshable STS credentials per association and
refresh them through the AWS SDK. Do not persist credentials or session tokens.
The preflight gate must inspect the full customer role trust policy and reject
wildcard, alternate-principal, or unconditional allows; a successful
AssumeRole call alone does not prove that the policy is sufficiently
restricted.

### Relationship to OIDC configs and association lifecycle

The intended model is for an unmanaged `OidcConfig` to retain its customer
`secretArn` and resolve the customer role from the account association, rather
than storing a separate `installerRoleArn` on each OIDC config. The association
can then provide the same account role for preflight, cloud discovery, OIDC
secret reads, and other approved operator tasks. This is a planned integration:
the current OIDC API still requires `installerRoleArn`, and this association
POC does not yet perform the STS call or role validation.

Using the association as shared account access requires a read path for the
operator and explicit update/revocation semantics for role rotation. Changes
must re-enqueue affected Clusters and NodePools and invalidate preflight
results that were checked against the old association revision. The current
create-only endpoint does not yet provide that lifecycle.

## ROSA login and request flow

`rosa login --use-auth-code` uses the OCM CLI OAuth client (`ocm-cli`) with
authorization code + PKCE. The CLI stores the resulting refresh token, then
uses the OCM SDK connection to obtain and persist an access token. The
`rosa link aws-role-association` command sends that **access token**, not the
ID token from the browser's OIDC response. Tokens with a `typ: ID` claim are
rejected. The existing `rosa link ocm-role` command remains the separate OCM
account-management workflow.

The command reads the current OCM organization's `ExternalID` only to show it
in the confirmation prompt. It does not send an organization ID; the API
derives the external ID from the verified access token.

Example:

```sh
rosa link aws-role-association \
  --role-arn arn:aws:iam::123456789012:role/SeaplaneCloudDiscovery
```

For this command, ROSA initializes its OCM connection before `WithSeaplane`.
The Seaplane client gets a token source backed by the OCM connection; it obtains
a current access token for each request and adds
`X-RH-SSO-Authorization: Bearer <access-token>` before SigV4 signing. This
preserves `Authorization` for SigV4 and includes the RH SSO header in
`SignedHeaders`. Other Seaplane commands that do not initialize OCM remain
AWS/SigV4-only. `rosa login --seaplane-url` by itself does not perform OCM
login, so this command also requires an OCM login.

```mermaid
flowchart TD
    Login["ROSA login obtains RH SSO access token"]
    Client["Generated Create sends resource body with SigV4 and RH SSO token"]
    Gateway["API Gateway verifies SigV4 and derives AWS account identity"]
    Auth["Seaplane verifies RH SSO JWT using JWKS and configured claim checks"]
    SameAccount["Check role ARN account matches SigV4 account"]
    Store["Store spec.roleArn, status.organizationId, and metadata.creationTimestamp"]
    Result["Return 201, or 409 if already associated"]

    Login --> Client --> Gateway --> Auth --> SameAccount --> Store --> Result
```

* Request headers: `Authorization: AWS4-HMAC-SHA256 ...` carries SigV4;
  `X-RH-SSO-Authorization: Bearer <access-token>` carries RH SSO. Add the RH
  SSO header before SigV4 signing so it is covered by `SignedHeaders`. Do not
  send the AWS account ID in the request body.

The sample `typ: ID` token is not forwarded: the client uses the access token
issued/refreshed through the OCM SDK connection.

## Validation boundary and token acquisition

The API does not exchange credentials with RH SSO, call token introspection,
or make a live SSO authorization request for each association request. It
validates the access token locally: the JWT signature must verify against a
trusted RH SSO public signing key, and its issuer, time claims, ACL, subject,
and organization claims must pass the configured checks. Audience and
authorized-party claims are not checked. Org-admin status is additionally
required only when the route enables the middleware option.

The API does retrieve public JWKS keys from the configured RH SSO certs URL at
startup and refresh, using the mounted JWKS file only when that URL is
unavailable. That is key distribution, not per-token validation or token
exchange; no SSO client secret is needed by the API for JWT verification.

Obtaining and refreshing the access token belongs to the caller tooling:
`rosacli`, Terraform, OCM SDK clients, or another client. For example, ROSA CLI
can use browser-based authorization code with PKCE; other clients may use
their supported OAuth flow with a client ID/secret. The client then forwards
the resulting **access token** in `X-RH-SSO-Authorization`. The API only
validates that token; it does not participate in the login or token exchange.

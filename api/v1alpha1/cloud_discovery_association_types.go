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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// CloudDiscoveryAssociationSpec is the customer-provided role for an AWS account.
type CloudDiscoveryAssociationSpec struct {
	// +required
	// +seaplane:write-mode=immutable
	RoleARN string `json:"roleArn"`
}

// CloudDiscoveryAssociationStatus contains server-derived association details.
type CloudDiscoveryAssociationStatus struct {
	// OrganizationID is derived from the verified RH SSO access token.
	// +optional
	OrganizationID string `json:"organizationId,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +genclient:noStatus
// +resourceName=cloud_discovery_associations
// +bridge:watch=disabled
// +bridge:resource-path=cloud_discovery/associations

// CloudDiscoveryAssociation is a public resource backed by an internal
// seaplane-db record. The account is derived from SigV4; the organization is
// derived from the verified RH SSO access token.
type CloudDiscoveryAssociation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudDiscoveryAssociationSpec   `json:"spec"`
	Status            CloudDiscoveryAssociationStatus `json:"status,omitzero"`
}

func (in *CloudDiscoveryAssociation) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	return in.DeepCopy()
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &CloudDiscoveryAssociation{})
		return nil
	})
}

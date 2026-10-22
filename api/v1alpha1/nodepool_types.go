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
	hypershiftv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NodePoolPhase represents the lifecycle phase of a NodePool.
// +kubebuilder:validation:Enum=WaitingForCluster;Provisioning;Ready;Deleting
type NodePoolPhase string

const (
	NodePoolPhaseWaitingForCluster NodePoolPhase = "WaitingForCluster"
	NodePoolPhaseProvisioning      NodePoolPhase = "Provisioning"
	NodePoolPhaseReady             NodePoolPhase = "Ready"
	NodePoolPhaseDeleting          NodePoolPhase = "Deleting"
)

// EC2InstanceMetadataHttpTokens controls which EC2 instance metadata protocol
// versions are accepted by worker instances.
// +kubebuilder:validation:Enum=required;optional
type EC2InstanceMetadataHttpTokens string

const (
	EC2InstanceMetadataHttpTokensRequired EC2InstanceMetadataHttpTokens = "required"
	EC2InstanceMetadataHttpTokensOptional EC2InstanceMetadataHttpTokens = "optional"
)

// The CEL rules on NodePoolSpec enforce the cross-field constraints on the
// upgrade disruption budget. maxUnavailable and maxSurge are int-or-string, so
// the zero checks go through string() to catch both 0 and "0%" without relying
// on comparing a dyn value against a literal of the other type. The rules also
// have to fold in the value each field takes when it is omitted —
// maxUnavailable 0 and maxSurge 1 — or a pool with only maxSurge: 0 set would
// slip past the both-zero check and stall on its first upgrade.

// NodePoolSpec defines the desired state of a NodePool.
// The parent Cluster is resolved from the cluster-name prefix in metadata.name within the account namespace.
// +kubebuilder:validation:XValidation:rule="!((!has(self.maxUnavailable) || string(self.maxUnavailable) == '0' || string(self.maxUnavailable) == '0%') && has(self.maxSurge) && (string(self.maxSurge) == '0' || string(self.maxSurge) == '0%'))",message="maxUnavailable and maxSurge cannot both be 0: an upgrade can neither remove an existing node nor add a new one, so it would never make progress. maxUnavailable defaults to 0 when omitted"
// +kubebuilder:validation:XValidation:rule="!has(self.maxUnavailable) || type(self.maxUnavailable) == string || !has(self.nodePool) || !has(self.nodePool.replicas) || self.maxUnavailable <= self.nodePool.replicas",message="maxUnavailable cannot exceed spec.nodePool.replicas"
// +kubebuilder:validation:XValidation:rule="!has(self.maxSurge) || !has(self.nodePool) || !has(self.nodePool.management) || self.nodePool.management.upgradeType == 'Replace'",message="maxSurge is only valid when the upgrade type is Replace: InPlace upgrades never provision additional nodes"
type NodePoolSpec struct {
	// AutoRepair controls whether unhealthy nodes are automatically replaced.
	// +seaplane:write-mode=mutable
	// +optional
	AutoRepair *bool `json:"autoRepair,omitempty"`

	// EC2InstanceMetadataHttpTokens configures the HTTP token requirement for
	// the EC2 instance metadata service. "required" enforces IMDSv2, while
	// "optional" permits IMDSv1. If omitted when creating a NodePool, it defaults
	// to "required".
	// +kubebuilder:default="required"
	// +seaplane:write-mode=immutable
	// +optional
	EC2InstanceMetadataHttpTokens EC2InstanceMetadataHttpTokens `json:"ec2InstanceMetadataHttpTokens,omitempty"`

	// Labels are customer-defined labels applied to nodes in this pool.
	// +seaplane:write-mode=mutable
	// +seaplane:resettable=true
	// +kubebuilder:validation:MaxProperties=100
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Tags are customer-defined AWS tags for resources created for this NodePool.
	// They combine with the parent Cluster's tags, with NodePool values taking
	// precedence when keys overlap. Reserved system tags share the rendered AWS
	// resource tag budget.
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:MaxProperties=25
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// MaxUnavailable is the largest number of nodes in this pool that may be
	// unavailable at the same time while the pool is upgrading. It applies to
	// both the Replace and the InPlace upgrade type.
	//
	// The value is either an absolute node count (for example 2) or a
	// percentage of the desired replica count (for example "30%"), which is
	// rounded down. An absolute value may not exceed spec.nodePool.replicas.
	//
	// Omitting the field means 0, the behavior node pools had before the
	// field existed: no node is taken out of service until its replacement is
	// ready.
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:XValidation:rule="type(self) == int ? self >= 0 : self.matches('^(0|[1-9][0-9]?|100)%$')",message="maxUnavailable must be a non-negative whole number of nodes or a percentage from 0% to 100%"
	// +optional
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// MaxSurge is the largest number of nodes that may be provisioned above
	// the desired replica count while the pool is upgrading. It is only valid
	// with the Replace upgrade type, because an InPlace upgrade updates the
	// existing instances and never creates extra ones.
	//
	// The value is either an absolute node count (for example 2) or a
	// percentage of the desired replica count (for example "30%"), which is
	// rounded up.
	//
	// Omitting the field means 1, the behavior node pools had before the
	// field existed: one replacement node is built at a time.
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:XValidation:rule="type(self) == int ? self >= 0 : self.matches('^(0|[1-9][0-9]*)%$')",message="maxSurge must be a non-negative whole number of nodes or a percentage"
	// +optional
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// AccountID is the AWS account that owns this node pool.
	// +k8s:openapi-gen=false
	// +seaplane:write-mode=service-set
	// +optional
	AccountID string `json:"accountId,omitempty"`

	// InternalPoolID is a platform-assigned unique identifier.
	// +k8s:openapi-gen=false
	// +seaplane:write-mode=service-set
	// +optional
	InternalPoolID string `json:"internalPoolId,omitempty"`

	// NodePool contains the upstream HyperShift fields, mirrored as
	// passthrough types with per-field visibility and write-mode markers.
	// +kubebuilder:validation:Required
	NodePool NodePoolSpecPassthrough `json:"nodePool"`
}

// NodePoolStatus defines the observed state of a NodePool.
type NodePoolStatus struct {
	// Replicas is the latest observed number of nodes in the pool.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Conditions represent the latest observations of the node pool's state.
	// Known condition types: Synced, Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase summarizes the node pool's lifecycle state.
	// +optional
	Phase NodePoolPhase `json:"phase,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +bridge:watch=disabled
// +bridge:wait
// +bridge:parent=Cluster,label=seaplane.io/cluster-uid
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=hfnp
// +kubebuilder:validation:XValidation:rule=`self.metadata.name.matches("^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?[.][a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$")`,message="metadata.name must be <cluster>.<child> with DNS labels of at most 63 characters each"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// NodePool is the Schema for the nodepools API.
// It represents a set of worker nodes for a Cluster.
// metadata.Namespace is account-<accountID>. metadata.Name is the client-selected
// <cluster>.<child> name.
// The parent Cluster name is the prefix of metadata.name; the server sets its
// ownerReference and cluster-uid label after resolving that Cluster by account namespace/name.
type NodePool struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec NodePoolSpec `json:"spec"`

	// +optional
	Status NodePoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NodePoolList contains a list of NodePool.
type NodePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NodePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &NodePool{}, &NodePoolList{})
		return nil
	})
}

// NodePoolPlatform mirrors NodePoolPlatform from upstream HyperShift, exposing only
// the AWS platform. Other platforms are intentionally omitted.
// +seaplane:upstream-reduced-object=hypershiftv1beta1.NodePoolPlatform
// +k8s:openapi-gen=true
type NodePoolPlatform struct {
	// type specifies the platform type for this NodePool.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=immutable
	// +required
	Type hypershiftv1beta1.PlatformType `json:"type"`

	// aws specifies the platform-specific AWS configuration for this NodePool.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +optional
	AWS *AWSNodePoolPlatform `json:"aws,omitempty"`
}

// AWSNodePoolPlatform mirrors upstream HyperShift's type, reduced further so RootVolume's sub-fields
// can carry independent write-modes (see Volume); enforcement happens via FieldValidator at request time.
// +seaplane:upstream-reduced-object=hypershiftv1beta1.AWSNodePoolPlatform
// +k8s:openapi-gen=true
type AWSNodePoolPlatform struct {
	// instanceType is an ec2 instance type for node instances (e.g. m5.large).
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:MaxLength=255
	// +required
	InstanceType string `json:"instanceType"`

	// instanceProfile is the AWS EC2 instance profile, which is a container for an IAM role that the EC2 instance uses.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:MaxLength=255
	// +optional
	InstanceProfile string `json:"instanceProfile,omitempty"`

	// subnet is the subnet to use for node instances.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:XValidation:rule="has(self.id) && self.id.startsWith('subnet-') ? !has(self.filters) : size(self.filters) > 0", message="subnet is invalid, a valid subnet id or filters must be set, but not both"
	// +required
	Subnet hypershiftv1beta1.AWSResourceReference `json:"subnet"`

	// ami is the image id to use for node instances. If unspecified, the default
	// is chosen based on the NodePool release payload image.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:MaxLength=255
	// +optional
	AMI string `json:"ami,omitempty"`

	// imageType selects the operating system image family for node instances,
	// either "Linux" (RHCOS, the default when unset) or "Windows".
	//
	// Windows node pools are License Included: HyperShift resolves a Windows
	// AMI for the pool's region and OpenShift version, and the Windows Server
	// license is billed through the EC2 instance cost rather than brought by
	// the customer. Setting ami overrides that automatic selection. Windows is
	// only supported on amd64, which is the architecture every pool gets.
	//
	// The image family is baked into every instance the pool launches and an
	// existing node cannot be converted between Linux and Windows, so this
	// field is immutable once the pool is created. Add Windows capacity to an
	// existing cluster by creating a second node pool.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=immutable
	// +kubebuilder:validation:Enum=Windows;Linux
	// +optional
	ImageType hypershiftv1beta1.ImageType `json:"imageType,omitempty"`

	// securityGroups is an optional set of security groups to associate with node
	// instances.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:MaxItems=50
	// +optional
	SecurityGroups []hypershiftv1beta1.AWSResourceReference `json:"securityGroups,omitempty"`

	// rootVolume specifies configuration for the root volume of node instances.
	// Reduced one level further — see the Volume type.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +optional
	RootVolume *Volume `json:"rootVolume,omitempty"`

	// resourceTags is an optional list of additional tags to apply to AWS node
	// instances.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +kubebuilder:validation:MaxItems=25
	// +optional
	ResourceTags []hypershiftv1beta1.AWSResourceTag `json:"resourceTags,omitempty"`

	// placement specifies the placement options for the EC2 instances.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=mutable
	// +optional
	Placement *hypershiftv1beta1.PlacementOptions `json:"placement,omitempty"`
}

// Volume mirrors upstream HyperShift's Volume type. Write-modes match v1: only size is customer-settable
// (immutable after creation); type/iops/encrypted/encryptionKey are service-set, enforced by FieldValidator.
// +seaplane:upstream-reduced-object=hypershiftv1beta1.Volume
// +k8s:openapi-gen=true
type Volume struct {
	// size is the size of the volume in gibibytes (GiB).
	// Settable on create only — matches v1, which rejects changing this on update.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=immutable
	// +kubebuilder:validation:Minimum=8
	// +required
	Size int64 `json:"size"`

	// type is computed by the platform, matching v1 (always gp3). A customer-supplied non-default value is
	// rejected at request validation rather than hidden from the schema.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=service-set
	// +kubebuilder:validation:MaxLength=255
	Type string `json:"type"`

	// iops is computed by the platform, matching v1 (always 0 for gp2/gp3). A customer-supplied non-default
	// value is rejected at request validation rather than hidden from the schema.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=service-set
	IOPS int64 `json:"iops,omitempty"`

	// encrypted is computed by the platform, matching v1 (always true). A customer-supplied non-default
	// value is rejected at request validation rather than hidden from the schema.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=service-set
	Encrypted *bool `json:"encrypted,omitempty"`

	// encryptionKey is computed by the platform, matching v1 (never set). A customer-supplied non-default
	// value is rejected at request validation rather than hidden from the schema.
	// +k8s:openapi-gen=true
	// +seaplane:write-mode=service-set
	// +kubebuilder:validation:MaxLength=2048
	EncryptionKey string `json:"encryptionKey,omitempty"`
}

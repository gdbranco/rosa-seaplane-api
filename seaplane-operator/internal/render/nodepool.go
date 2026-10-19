package render

import (
	"fmt"
	"strings"

	hypershiftv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	seaplanev1alpha1 "github.com/openshift-online/rosa-seaplane-api/api/v1alpha1"
)

const (
	defaultRootVolumeSizeGiB                int64 = 300
	ec2InstanceMetadataHttpTokensAnnotation       = "hypershift.openshift.io/ec2-instance-metadata-http-tokens"
)

// NodePoolResource generates the HyperShift NodePool resource for the MC.
func NodePoolResource(nodePool *seaplanev1alpha1.NodePool, cluster *seaplanev1alpha1.Cluster) (Resource, error) {
	clusterID, ns, err := managementClusterIdentity(cluster)
	if err != nil {
		return Resource{}, err
	}
	clusterName := cluster.Name // human-readable
	hostedClusterName := HostedClusterName(cluster)
	if hostedClusterName == "" {
		return Resource{}, fmt.Errorf("parent Cluster %s/%s has no valid HostedCluster name", cluster.Namespace, cluster.Name)
	}
	parentName, npName, ok := strings.Cut(nodePool.Name, ".")
	if !ok || parentName != clusterName || npName == "" || strings.Contains(npName, ".") {
		return Resource{}, fmt.Errorf("NodePool name %q must be <cluster>.<child> for parent Cluster %q", nodePool.Name, clusterName)
	}
	var annotations map[string]string
	if nodePool.Spec.EC2InstanceMetadataHttpTokens != "" {
		annotations = map[string]string{
			ec2InstanceMetadataHttpTokensAnnotation: string(nodePool.Spec.EC2InstanceMetadataHttpTokens),
		}
	}

	npSpec, err := toNodePoolSpec(&nodePool.Spec.NodePool)
	if err != nil {
		return Resource{}, fmt.Errorf("converting NodePoolSpec for nodepool %s/%s: %w", ns, npName, err)
	}
	npSpec.ClusterName = hostedClusterName
	// NodePools without an explicit release follow the parent cluster's.
	if npSpec.Release.Image == "" {
		npSpec.Release.Image = cluster.Spec.HostedCluster.Release.Image
	}

	if npSpec.Management.UpgradeType == "" {
		npSpec.Management.UpgradeType = hypershiftv1beta1.UpgradeTypeReplace
	}
	if nodePool.Spec.AutoRepair != nil {
		npSpec.Management.AutoRepair = *nodePool.Spec.AutoRepair
	} else {
		npSpec.Management.AutoRepair = true
	}
	applyUpgradeBudget(npSpec, nodePool.Spec.MaxUnavailable, nodePool.Spec.MaxSurge)

	npSpec.NodeLabels = nodePool.Spec.Labels

	if npSpec.Replicas == nil && npSpec.AutoScaling == nil {
		npSpec.Replicas = ptr.To(int32(2))
	}

	if npSpec.Platform.AWS != nil {
		if npSpec.Platform.AWS.InstanceType == "" {
			npSpec.Platform.AWS.InstanceType = "t3a.xlarge"
		}
		if npSpec.Platform.AWS.RootVolume == nil {
			npSpec.Platform.AWS.RootVolume = &hypershiftv1beta1.Volume{Size: defaultRootVolumeSizeGiB, Type: "gp3", Encrypted: ptr.To(true)}
		} else {
			if npSpec.Platform.AWS.RootVolume.Size == 0 {
				npSpec.Platform.AWS.RootVolume.Size = defaultRootVolumeSizeGiB
			}
			if npSpec.Platform.AWS.RootVolume.Type == "" {
				npSpec.Platform.AWS.RootVolume.Type = "gp3"
			}
			if npSpec.Platform.AWS.RootVolume.Encrypted == nil {
				npSpec.Platform.AWS.RootVolume.Encrypted = ptr.To(true)
			}
		}
		// The worker EC2 instances are created by the NodePool, not the
		// HostedCluster, so the current cluster tags must be propagated here for
		// both initial creation and later tag updates.
		npSpec.Platform.AWS.ResourceTags = appendSystemTags(npSpec.Platform.AWS.ResourceTags, "", cluster.Spec.Tags)
	}

	return Resource{
		Group: "hypershift.openshift.io", Version: "v1beta1", Resource: "nodepools",
		Name: npName, Namespace: ns,
		Object: &hypershiftv1beta1.NodePool{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "hypershift.openshift.io/v1beta1",
				Kind:       "NodePool",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:        npName,
				Namespace:   ns,
				Labels:      clusterLabels(clusterID),
				Annotations: annotations,
			},
			Spec: *npSpec,
		},
	}, nil
}

// applyUpgradeBudget maps the pool's maxUnavailable/maxSurge onto the
// HyperShift management block. Seaplane exposes them as two flat spec fields
// because that is the only upgrade knob customers get, whereas HyperShift
// splits them across management.replace.rollingUpdate and management.inPlace
// depending on the upgrade type, so the destination has to be chosen here.
//
// Only the fields the customer actually set are written: HyperShift's own CRD
// default for management.replace is {strategy: RollingUpdate, rollingUpdate:
// {maxSurge: 1, maxUnavailable: 0}}, and leaving an unset field alone is what
// keeps existing node pools on the behavior they had before these fields
// existed. Values are copied rather than aliased so the rendered object does
// not share memory with the cached NodePool.
func applyUpgradeBudget(npSpec *hypershiftv1beta1.NodePoolSpec, maxUnavailable, maxSurge *intstr.IntOrString) {
	if npSpec.Management.UpgradeType == hypershiftv1beta1.UpgradeTypeInPlace {
		// InPlace never provisions extra nodes, so maxSurge has no meaning
		// here; the API rejects it for this upgrade type.
		if maxUnavailable == nil {
			return
		}
		if npSpec.Management.InPlace == nil {
			npSpec.Management.InPlace = &hypershiftv1beta1.InPlaceUpgrade{}
		}
		npSpec.Management.InPlace.MaxUnavailable = ptr.To(*maxUnavailable)
		return
	}

	if maxUnavailable == nil && maxSurge == nil {
		return
	}
	if npSpec.Management.Replace == nil {
		npSpec.Management.Replace = &hypershiftv1beta1.ReplaceUpgrade{}
	}
	if npSpec.Management.Replace.Strategy == "" {
		npSpec.Management.Replace.Strategy = hypershiftv1beta1.UpgradeStrategyRollingUpdate
	}
	// Upstream rejects rollingUpdate on any other strategy, and OnDelete
	// replaces nodes only as they are manually deleted, so there is no budget
	// to apply.
	if npSpec.Management.Replace.Strategy != hypershiftv1beta1.UpgradeStrategyRollingUpdate {
		return
	}
	if npSpec.Management.Replace.RollingUpdate == nil {
		npSpec.Management.Replace.RollingUpdate = &hypershiftv1beta1.RollingUpdate{}
	}
	if maxUnavailable != nil {
		npSpec.Management.Replace.RollingUpdate.MaxUnavailable = ptr.To(*maxUnavailable)
	}
	if maxSurge != nil {
		npSpec.Management.Replace.RollingUpdate.MaxSurge = ptr.To(*maxSurge)
	}
}

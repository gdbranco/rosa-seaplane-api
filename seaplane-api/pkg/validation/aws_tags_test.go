package validation

import (
	"fmt"
	"strings"
	"testing"

	hypershiftv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"
)

func TestValidateCustomerAWSTags(t *testing.T) {
	validTags := map[string]string{
		"cost-center":        "cc-1234",
		"kubernetes.io/role": "worker+1@eu=west",
	}
	if errs := ValidateCustomerAWSTags(validTags); len(errs) != 0 {
		t.Fatalf("valid tags returned errors: %v", errs)
	}

	maximumTags := generatedAWSTags(maxAWSResourceTags - hostedClusterReservedSystemTagCount)
	if errs := ValidateCustomerAWSTags(maximumTags); len(errs) != 0 {
		t.Fatalf("maximum supported tags returned errors: %v", errs)
	}

	tests := []struct {
		name string
		tags map[string]string
		want string
	}{
		{
			name: "too many tags",
			tags: generatedAWSTags(maxAWSResourceTags - hostedClusterReservedSystemTagCount + 1),
			want: "available budget after reserving capacity for system tags",
		},
		{
			name: "empty key",
			tags: map[string]string{"": "value"},
			want: "must not be empty",
		},
		{
			name: "empty value",
			tags: map[string]string{"key": ""},
			want: "must not be empty",
		},
		{
			name: "reserved prefix",
			tags: map[string]string{"AWS:reserved": "value"},
			want: "reserved for AWS",
		},
		{
			name: "invalid key characters",
			tags: map[string]string{"bad!key": "value"},
			want: "tag key",
		},
		{
			name: "invalid value characters",
			tags: map[string]string{"key": "bad!value"},
			want: "tag value",
		},
		{
			name: "key too long",
			tags: map[string]string{strings.Repeat("k", 129): "value"},
			want: "tag key",
		},
		{
			name: "value too long",
			tags: map[string]string{"key": strings.Repeat("v", 257)},
			want: "tag value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := ValidateCustomerAWSTags(test.tags)
			if len(errs) == 0 {
				t.Fatal("expected validation error")
			}
			if errs[0].Field != "spec.tags" {
				t.Errorf("error field = %q, want spec.tags", errs[0].Field)
			}
			if !strings.Contains(errs[0].Reason, test.want) {
				t.Errorf("error reason = %q, want substring %q", errs[0].Reason, test.want)
			}
		})
	}
}

func TestValidateClusterAWSTagBudget(t *testing.T) {
	maxClusterTags := generatedAWSTags(maxAWSResourceTags - hostedClusterReservedSystemTagCount)
	if errs := ValidateClusterAWSTagBudget(maxClusterTags, nil); len(errs) != 0 {
		t.Fatalf("maximum rendered cluster tags returned errors: %v", errs)
	}

	tooMany := ValidateClusterAWSTagBudget(maxClusterTags, []hypershiftv1beta1.AWSResourceTag{{Key: "passthrough", Value: "value"}})
	if len(tooMany) != 1 {
		t.Fatalf("expected one budget error, got %v", tooMany)
	}
	if tooMany[0].Field != "spec.tags" {
		t.Errorf("error field = %q, want spec.tags", tooMany[0].Field)
	}

	// A tag key shared with the passthrough list renders once.
	withCollision := map[string]string{"shared": "customer", "other": "customer"}
	passthrough := []hypershiftv1beta1.AWSResourceTag{{Key: "shared", Value: "passthrough"}}
	if errs := ValidateClusterAWSTagBudget(withCollision, passthrough); len(errs) != 0 {
		t.Fatalf("duplicate tag key should count once: %v", errs)
	}

	tooManyPassthrough := ValidateClusterAWSTagBudget(nil, generatedAWSResourceTagList(maxAWSResourceTags-hostedClusterReservedSystemTagCount+1))
	if len(tooManyPassthrough) != 1 {
		t.Fatalf("expected passthrough budget error, got %v", tooManyPassthrough)
	}
	if tooManyPassthrough[0].Field != "spec.hostedCluster.platform.aws.resourceTags" {
		t.Errorf("error field = %q, want HostedCluster resourceTags", tooManyPassthrough[0].Field)
	}
}

func TestValidateNodePoolAWSTagBudget(t *testing.T) {
	field := "spec.nodePool.platform.aws.resourceTags"
	clusterTags := generatedAWSTags(maxAWSResourceTags - nodePoolReservedSystemTagCount - 1)
	poolTags := []hypershiftv1beta1.AWSResourceTag{{Key: "pool-only", Value: "value"}}
	if errs := ValidateNodePoolAWSTagBudget(clusterTags, poolTags, field); len(errs) != 0 {
		t.Fatalf("maximum rendered NodePool tags returned errors: %v", errs)
	}

	tooMany := ValidateNodePoolAWSTagBudget(clusterTags, append(poolTags,
		hypershiftv1beta1.AWSResourceTag{Key: "another-pool-tag", Value: "value"}), field)
	if len(tooMany) != 1 {
		t.Fatalf("expected NodePool budget error, got %v", tooMany)
	}
	if tooMany[0].Field != field {
		t.Errorf("error field = %q, want %q", tooMany[0].Field, field)
	}

	// A pool tag that overrides an inherited key does not consume another slot.
	collidingPoolTags := []hypershiftv1beta1.AWSResourceTag{{Key: "shared", Value: "pool"}}
	if errs := ValidateNodePoolAWSTagBudget(map[string]string{"shared": "cluster"}, collidingPoolTags, field); len(errs) != 0 {
		t.Fatalf("duplicate NodePool tag key should count once: %v", errs)
	}
}

func generatedAWSTags(count int) map[string]string {
	tags := make(map[string]string, count)
	for i := range count {
		tags[fmt.Sprintf("tag-%d", i)] = "value"
	}
	return tags
}

func generatedAWSResourceTagList(count int) []hypershiftv1beta1.AWSResourceTag {
	tags := make([]hypershiftv1beta1.AWSResourceTag, count)
	for i := range count {
		tags[i] = hypershiftv1beta1.AWSResourceTag{Key: fmt.Sprintf("tag-%d", i), Value: "value"}
	}
	return tags
}

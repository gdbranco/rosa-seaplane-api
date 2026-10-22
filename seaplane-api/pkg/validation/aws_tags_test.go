package validation

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateCustomerAWSTags(t *testing.T) {
	validTags := map[string]string{
		"cost-center":        "cc-1234",
		"kubernetes.io/role": "worker+1@eu=west",
		"team name":          "platform team",
	}
	if errs := ValidateCustomerAWSTags(validTags, "spec.tags"); len(errs) != 0 {
		t.Fatalf("valid tags returned errors: %v", errs)
	}

	maximumTags := generatedAWSTags(maxAWSResourceTags - hostedClusterReservedSystemTagCount)
	if errs := ValidateCustomerAWSTags(maximumTags, "spec.tags"); len(errs) != 0 {
		t.Fatalf("maximum supported tags returned errors: %v", errs)
	}

	tests := []struct {
		name string
		tags map[string]string
		want string
	}{
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
			name: "comma in key",
			tags: map[string]string{"bad,key": "value"},
			want: "tag key",
		},
		{
			name: "invalid value characters",
			tags: map[string]string{"key": "bad!value"},
			want: "tag value",
		},
		{
			name: "comma in value",
			tags: map[string]string{"key": "bad,value"},
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
			errs := ValidateCustomerAWSTags(test.tags, "spec.tags")
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
	if errs := ValidateClusterAWSTagBudget(maxClusterTags); len(errs) != 0 {
		t.Fatalf("maximum rendered cluster tags returned errors: %v", errs)
	}

	tooMany := ValidateClusterAWSTagBudget(generatedAWSTags(maxAWSResourceTags - hostedClusterReservedSystemTagCount + 1))
	if len(tooMany) != 1 {
		t.Fatalf("expected one budget error, got %v", tooMany)
	}
	if tooMany[0].Field != "spec.tags" {
		t.Errorf("error field = %q, want spec.tags", tooMany[0].Field)
	}
}

func TestValidateNodePoolAWSTagBudget(t *testing.T) {
	field := "spec.tags"
	clusterTags := generatedAWSTags(maxAWSResourceTags - nodePoolReservedSystemTagCount - 1)
	poolTags := map[string]string{"pool-only": "value"}
	if errs := ValidateNodePoolAWSTagBudget(clusterTags, poolTags, field); len(errs) != 0 {
		t.Fatalf("maximum rendered NodePool tags returned errors: %v", errs)
	}

	tooMany := ValidateNodePoolAWSTagBudget(clusterTags, map[string]string{
		"pool-only":        "value",
		"another-pool-tag": "value",
	}, field)
	if len(tooMany) != 1 {
		t.Fatalf("expected NodePool budget error, got %v", tooMany)
	}
	if tooMany[0].Field != field {
		t.Errorf("error field = %q, want %q", tooMany[0].Field, field)
	}

	// A NodePool tag that overrides an inherited key does not consume another slot.
	collidingPoolTags := map[string]string{"shared": "pool"}
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

package validation

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	hypershiftv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"
)

const (
	maxAWSResourceTags                  = 25
	hostedClusterReservedSystemTagCount = 2
	nodePoolReservedSystemTagCount      = 1
	redHatManagedTagKey                 = "red-hat-managed"
	clusterOwnershipBudgetTagKey        = "\x00cluster-ownership-budget"
)

var (
	awsResourceTagKeyPattern   = regexp.MustCompile(`^[0-9A-Za-z_.:/=+-@]{1,128}$`)
	awsResourceTagValuePattern = regexp.MustCompile(`^[0-9A-Za-z_.:/=+-@]{1,256}$`)
)

// ValidateCustomerAWSTags checks the shared Cluster.spec.tags map against the
// tag constraints that apply to native AWS resourceTags fields.
func ValidateCustomerAWSTags(tags map[string]string) ValidationErrors {
	if len(tags) > maxAWSResourceTags-hostedClusterReservedSystemTagCount {
		return ValidationErrors{&ValidationError{
			Field:  "spec.tags",
			Reason: fmt.Sprintf("too many customer AWS tags for the available budget after reserving capacity for system tags; got %d", len(tags)),
		}}
	}

	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	var errs ValidationErrors
	for _, key := range keys {
		value := tags[key]
		switch {
		case key == "" || value == "":
			errs = append(errs, &ValidationError{
				Field:  "spec.tags",
				Reason: "tag key and value must not be empty",
			})
		case strings.HasPrefix(strings.ToLower(key), "aws:"):
			errs = append(errs, &ValidationError{
				Field:  "spec.tags",
				Reason: fmt.Sprintf("tag key %q uses a prefix reserved for AWS", key),
			})
		case !awsResourceTagKeyPattern.MatchString(key):
			errs = append(errs, &ValidationError{
				Field:  "spec.tags",
				Reason: fmt.Sprintf("tag key %q does not match the AWS resource tag key constraints", key),
			})
		case !awsResourceTagValuePattern.MatchString(value):
			errs = append(errs, &ValidationError{
				Field:  "spec.tags",
				Reason: fmt.Sprintf("tag value for key %q does not match the AWS resource tag value constraints", key),
			})
		}
	}
	return errs
}

// ValidateClusterAWSTagBudget validates tags rendered onto the HostedCluster,
// including tags set through the passthrough platform field and reserved
// system tags.
func ValidateClusterAWSTagBudget(customerTags map[string]string, passthroughTags []hypershiftv1beta1.AWSResourceTag) ValidationErrors {
	count := renderedAWSTagCount(
		[]string{redHatManagedTagKey, clusterOwnershipBudgetTagKey},
		passthroughTags,
		customerTags,
	)
	if count <= maxAWSResourceTags {
		return nil
	}

	field := "spec.tags"
	if len(customerTags) == 0 {
		field = "spec.hostedCluster.platform.aws.resourceTags"
	}
	return ValidationErrors{&ValidationError{
		Field:  field,
		Reason: fmt.Sprintf("rendered AWS resource tags would contain %d entries including reserved system tags; maximum is %d", count, maxAWSResourceTags),
	}}
}

// ValidateNodePoolAWSTagBudget validates the inherited Cluster tags and the
// NodePool-specific tags together with the reserved NodePool system tags.
func ValidateNodePoolAWSTagBudget(clusterTags map[string]string, nodePoolTags []hypershiftv1beta1.AWSResourceTag, field string) ValidationErrors {
	count := renderedAWSTagCount(
		[]string{redHatManagedTagKey},
		nodePoolTags,
		clusterTags,
	)
	if count <= maxAWSResourceTags {
		return nil
	}
	return ValidationErrors{&ValidationError{
		Field:  field,
		Reason: fmt.Sprintf("rendered AWS resource tags would contain %d entries including reserved system tags; maximum is %d", count, maxAWSResourceTags),
	}}
}

func renderedAWSTagCount(systemTagKeys []string, existing []hypershiftv1beta1.AWSResourceTag, customerTags map[string]string) int {
	seen := make(map[string]struct{}, len(systemTagKeys)+len(existing)+len(customerTags))
	count := 0
	add := func(key string) {
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		count++
	}

	for _, key := range systemTagKeys {
		add(key)
	}
	for _, tag := range existing {
		add(tag.Key)
	}
	for key := range customerTags {
		add(key)
	}
	return count
}

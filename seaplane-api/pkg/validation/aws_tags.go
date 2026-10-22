package validation

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	maxAWSResourceTags                  = 25
	hostedClusterReservedSystemTagCount = 2
	nodePoolReservedSystemTagCount      = 1
	redHatManagedTagKey                 = "red-hat-managed"
	clusterOwnershipBudgetTagKey        = "\x00cluster-ownership-budget"
)

var (
	awsResourceTagKeyPattern   = regexp.MustCompile(`^[\pL\pZ\pN_.:/=+\-@]{1,128}$`)
	awsResourceTagValuePattern = regexp.MustCompile(`^[\pL\pZ\pN_.:/=+\-@]{1,256}$`)
)

// ValidateCustomerAWSTags checks customer tag key and value formats. Budget
// checks are performed separately using the Cluster and NodePool context.
func ValidateCustomerAWSTags(tags map[string]string, field string) ValidationErrors {
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
				Field:  field,
				Reason: "tag key and value must not be empty",
			})
		case strings.HasPrefix(strings.ToLower(key), "aws:"):
			errs = append(errs, &ValidationError{
				Field:  field,
				Reason: fmt.Sprintf("tag key %q uses a prefix reserved for AWS", key),
			})
		case !awsResourceTagKeyPattern.MatchString(key):
			errs = append(errs, &ValidationError{
				Field:  field,
				Reason: fmt.Sprintf("tag key %q does not match the AWS resource tag key constraints", key),
			})
		case !awsResourceTagValuePattern.MatchString(value):
			errs = append(errs, &ValidationError{
				Field:  field,
				Reason: fmt.Sprintf("tag value for key %q does not match the AWS resource tag value constraints", key),
			})
		}
	}
	return errs
}

// ValidateClusterAWSTagBudget validates the root-level customer tags rendered
// onto the HostedCluster together with capacity reserved for system tags.
func ValidateClusterAWSTagBudget(customerTags map[string]string) ValidationErrors {
	count := renderedAWSTagCount(
		[]string{redHatManagedTagKey, clusterOwnershipBudgetTagKey},
		customerTags,
	)
	if count <= maxAWSResourceTags {
		return nil
	}

	return ValidationErrors{&ValidationError{
		Field:  "spec.tags",
		Reason: fmt.Sprintf("rendered AWS resource tags would contain %d entries including reserved system tags; maximum is %d", count, maxAWSResourceTags),
	}}
}

// ValidateNodePoolAWSTagBudget validates the inherited Cluster tags and the
// NodePool-specific tags together with the reserved NodePool system tags.
func ValidateNodePoolAWSTagBudget(clusterTags, nodePoolTags map[string]string, field string) ValidationErrors {
	count := renderedAWSTagCount(
		[]string{redHatManagedTagKey},
		clusterTags,
		nodePoolTags,
	)
	if count <= maxAWSResourceTags {
		return nil
	}
	return ValidationErrors{&ValidationError{
		Field:  field,
		Reason: fmt.Sprintf("rendered AWS resource tags would contain %d entries including reserved system tags; maximum is %d", count, maxAWSResourceTags),
	}}
}

func renderedAWSTagCount(systemTagKeys []string, customerTagMaps ...map[string]string) int {
	capacity := len(systemTagKeys)
	for _, tags := range customerTagMaps {
		capacity += len(tags)
	}
	seen := make(map[string]struct{}, capacity)
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
	for _, tags := range customerTagMaps {
		for key := range tags {
			add(key)
		}
	}
	return count
}

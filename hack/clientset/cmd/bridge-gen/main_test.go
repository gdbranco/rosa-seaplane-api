package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformGenerationUsesCRDParentMarkers(t *testing.T) {
	inputDir := t.TempDir()
	outputDir := t.TempDir()
	source := `package v1alpha1

// +bridge:wait
// +bridge:parent=Cluster,label=seaplane.io/cluster-uid
type NodePool struct{}

// +bridge:wait
// +bridge:parent=Widget,label=example.io/widget-uid
type Gadget struct{}
`
	if err := os.WriteFile(filepath.Join(inputDir, "resources.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	resourceTypes := collectResourceTypes(inputDir)
	if len(resourceTypes) != 2 {
		t.Fatalf("collectResourceTypes returned %d resources, want 2", len(resourceTypes))
	}
	byName := map[string]resourceType{}
	for _, rt := range resourceTypes {
		byName[rt.Name] = rt
	}
	if nodePool := byName["NodePool"]; nodePool.ParentKind != "Cluster" || nodePool.ParentUIDOption != "ClusterUID" {
		t.Fatalf("NodePool parent metadata = %+v", nodePool)
	}
	if gadget := byName["Gadget"]; gadget.ParentKind != "Widget" || gadget.ParentUIDOption != "WidgetUID" {
		t.Fatalf("Gadget parent metadata = %+v", gadget)
	}

	generatePlatform(inputDir, outputDir, "platform", "example.test/client", "example.test/api", "V1alpha1Public", "")
	generated, err := os.ReadFile(filepath.Join(outputDir, "bridge_wrappers_generated.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"type NodePoolListOptions struct",
		"ClusterUID string",
		`mo.LabelSelector = "seaplane.io/cluster-uid=" + opts.ClusterUID`,
		"type GadgetListOptions struct",
		"WidgetUID string",
		`mo.LabelSelector = "example.io/widget-uid=" + opts.WidgetUID`,
	} {
		if !strings.Contains(string(generated), expected) {
			t.Errorf("generated wrappers do not contain %q", expected)
		}
	}
}

func TestPlatformGenerationEmitsNestedResourcePathRewrite(t *testing.T) {
	inputDir := t.TempDir()
	outputDir := t.TempDir()
	source := `package v1alpha1

// +genclient:nonNamespaced
// +resourceName=cloud_discovery_associations
// +bridge:watch=disabled
// +bridge:resource-path=cloud_discovery/associations
type CloudDiscoveryAssociation struct{}
`
	if err := os.WriteFile(filepath.Join(inputDir, "resources.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	resourceTypes := collectResourceTypes(inputDir)
	if len(resourceTypes) != 1 {
		t.Fatalf("collectResourceTypes returned %d resources, want 1", len(resourceTypes))
	}
	resource := resourceTypes[0]
	if resource.ResourceName != "cloud_discovery_associations" ||
		resource.ResourcePath != "cloud_discovery/associations" {
		t.Fatalf("resource route metadata = %+v", resource)
	}

	generatePlatform(inputDir, outputDir, "platform", "example.test/client", "example.test/api", "V1alpha1Public", "")
	generated, err := os.ReadFile(filepath.Join(outputDir, "bridge_wrappers_generated.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"func ResourcePathRewrites() map[string]string",
		`"cloud_discovery_associations": "cloud_discovery/associations"`,
	} {
		if !strings.Contains(string(generated), expected) {
			t.Errorf("generated wrappers do not contain %q", expected)
		}
	}
}

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/authz"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/config"
	"github.com/openshift-online/rosa-seaplane-api/seaplane-api/pkg/middleware"
	"github.com/spf13/cobra"
)

const errInvalidRHSSOConfig = "invalid RH SSO authentication configuration"

// loadStartupConfig loads service configuration and fixes authorization to the
// deployment region for policy attachment selection, Cedar context, and resource identities.
func loadStartupConfig(cmd *cobra.Command, region string) (*config.Config, *authz.Authorizer, error) {
	cfg := config.NewConfig()
	cfg.Regional.AWSRegion = region
	value, err := cmd.Flags().GetString("authz-config-file")
	if err != nil {
		return nil, nil, err
	}
	// Changed distinguishes an explicit empty flag from an absent flag.
	if !cmd.Flags().Changed("authz-config-file") {
		if env, exists := os.LookupEnv("AUTHZ_CONFIG_FILE"); exists {
			value = env
		}
	}
	cfg.Authz.ConfigFile = value
	if strings.TrimSpace(cfg.Authz.ConfigFile) == "" {
		return nil, nil, fmt.Errorf("--authz-config-file or AUTHZ_CONFIG_FILE is required")
	}
	authorizer, err := authz.LoadConfig(cfg.Authz.ConfigFile, region)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load authorization config: %w", err)
	}

	for _, input := range []struct {
		flag   string
		env    string
		target *string
	}{
		{config.RHSSOJWKSURLFlagName, config.RHSSOJWKSURLEnvName, &cfg.RHSSO.JWKSURL},
		{config.RHSSOJWKSFileFlagName, config.RHSSOJWKSFileEnvName, &cfg.RHSSO.JWKSFile},
		{config.RHSSOACLFileFlagName, config.RHSSOACLFileEnvName, &cfg.RHSSO.ACLFile},
		{config.RHSSOIssuerFlagName, config.RHSSOIssuerEnvName, &cfg.RHSSO.Issuer},
	} {
		value, err := stringFlagOrEnv(cmd, input.flag, input.env)
		if err != nil {
			return nil, nil, err
		}
		*input.target = value
	}
	if err := middleware.ValidateRHSSOConfig(cfg.RHSSO); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", errInvalidRHSSOConfig, err)
	}

	for _, input := range []struct {
		env    string
		target *string
	}{
		{"API_BIND_ADDRESS", &cfg.Server.APIBindAddress},
		{"HEALTH_BIND_ADDRESS", &cfg.Server.HealthBindAddress},
		{"METRICS_BIND_ADDRESS", &cfg.Server.MetricsBindAddress},
	} {
		if value := os.Getenv(input.env); value != "" {
			*input.target = value
		}
	}
	return cfg, authorizer, nil
}

// stringFlagOrEnv uses an explicitly set flag over its environment fallback.
// Missing flags are allowed for startup helpers that construct minimal commands.
func stringFlagOrEnv(cmd *cobra.Command, flag, env string) (string, error) {
	if cmd.Flags().Lookup(flag) == nil {
		return os.Getenv(env), nil
	}
	value, err := cmd.Flags().GetString(flag)
	if err != nil {
		return "", err
	}
	if !cmd.Flags().Changed(flag) {
		if environmentValue, exists := os.LookupEnv(env); exists {
			value = environmentValue
		}
	}
	return value, nil
}

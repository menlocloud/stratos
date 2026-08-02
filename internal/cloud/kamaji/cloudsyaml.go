package kamaji

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/menlocloud/stratos/internal/cloud/client"
)

// CloudsYAML renders the clouds.yaml CAPO + OCCM/cinder-csi consume, from the tenant-scoped
// OpenStack client config (ClientConfigForProject — admin creds scoped to the CUSTOMER's own
// keystone project, plan D4). The secret this lands in exists ONLY on the management cluster
// (D7) — the customer cluster never carries any OpenStack credential.
//
// ponytail: v1 uses the provider admin credential scoped to the tenant; minting a per-cluster
// application credential instead (bounded blast radius) is the plan's hardening follow-up —
// swap the auth block here when the keystone appcred leg lands.
func CloudsYAML(cfg client.Config) (string, error) {
	auth := map[string]any{"auth_url": cfg.AuthURL}
	if cfg.AppCredID != "" {
		auth["application_credential_id"] = cfg.AppCredID
		auth["application_credential_secret"] = cfg.AppCredSecret
	} else {
		auth["username"] = cfg.Username
		auth["password"] = cfg.Password
		if cfg.UserDomainName != "" {
			auth["user_domain_name"] = cfg.UserDomainName
		}
		if cfg.ProjectID != "" {
			auth["project_id"] = cfg.ProjectID
		} else if cfg.ProjectName != "" {
			auth["project_name"] = cfg.ProjectName
		}
		if cfg.ProjectDomainName != "" {
			auth["project_domain_name"] = cfg.ProjectDomainName
		}
	}
	doc := map[string]any{
		"clouds": map[string]any{
			"openstack": map[string]any{
				"auth":                 auth,
				"region_name":          cfg.Region,
				"interface":            "public",
				"identity_api_version": 3,
			},
		},
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("clouds.yaml: %w", err)
	}
	return string(b), nil
}

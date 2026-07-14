package externalservice

// kamaji.go — typed accessors for the "kamaji" Managed-Kubernetes provider kind
// (config.provider == "kamaji"; ceph-s3 precedent). Document shape:
//
//	{
//	  "name": "Kamaji AZ1", "type": "CLOUD", "status": "PRIVATE",
//	  "config": {
//	    "provider": "kamaji",
//	    "regions":  {"az1": {}},
//	    "services": {"kubernetes": {"az1": true}},
//	    "argocd": {                       // delivery plane (plan D3)
//	      "namespace":    "argocd",
//	      "project":      "stratos-k8s",  // AppProject guardrail
//	      "chartRepo":    "ghcr.io/menlocloud/charts",
//	      "chartName":    "openstack-kamaji-cluster",
//	      "chartVersion": "0.2.3"         // pinned — never latest (plan §9)
//	    },
//	    "cluster": {                      // chart-value defaults every cluster inherits
//	      "dataStoreName":     "default",
//	      "floatingNetworkId": "…",       // Octavia floating net for the API LB
//	      "externalNetworkId": "…",       // CAPO external network
//	      "dnsZone":           "k8s.example.com",
//	      "versions":          {"1.35.4": "<glance-image-id>"}  // curated version→image matrix
//	    }
//	  },
//	  "secret": {"kubeconfig": "<management-cluster kubeconfig>"}  // encrypted at rest
//	}
//
// A kamaji provider is OpenStack-adjacent, not OpenStack-direct: cluster CONTROL PLANES live on
// the management cluster; worker VMs land in the customer's own keystone tenant of the project's
// OPENSTACK service (plan D4) — so a project must be attached to both.

import "github.com/menlocloud/stratos/internal/cloud/kamaji"

// IsKamaji reports whether this is a Kamaji Managed-Kubernetes provider (config.provider == "kamaji").
func (e *ExternalService) IsKamaji() bool { return e.Provider() == "kamaji" }

// KamajiRegion is config.region, falling back to the first configured region (ceph precedent).
func (e *ExternalService) KamajiRegion() string {
	if r := str(e.Config["region"]); r != "" {
		return r
	}
	if rs := e.RegionNames(); len(rs) > 0 {
		return rs[0]
	}
	return ""
}

// KamajiConfig assembles the kamaji.Config for kamaji.New: the decrypted management kubeconfig
// (secret.kubeconfig) + the argocd/cluster config blocks with their defaults.
func (e *ExternalService) KamajiConfig() kamaji.Config {
	argo, _ := e.Config["argocd"].(map[string]any)
	cl, _ := e.Config["cluster"].(map[string]any)

	versions := map[string]string{}
	if vs, ok := cl["versions"].(map[string]any); ok {
		for k, v := range vs {
			if s := str(v); s != "" {
				versions[k] = s
			}
		}
	}
	cfg := kamaji.Config{
		Kubeconfig:    str(e.secretMap()["kubeconfig"]),
		Region:        e.KamajiRegion(),
		ArgoNamespace: str(argo["namespace"]),
		ArgoProject:   str(argo["project"]),
		ChartRepo:     str(argo["chartRepo"]),
		ChartName:     str(argo["chartName"]),
		ChartVersion:  str(argo["chartVersion"]),
		Defaults: kamaji.ClusterDefaults{
			DataStoreName:     str(cl["dataStoreName"]),
			FloatingNetworkID: str(cl["floatingNetworkId"]),
			ExternalNetworkID: str(cl["externalNetworkId"]),
			DNSZone:           str(cl["dnsZone"]),
			Versions:          versions,
		},
	}
	if cfg.ArgoNamespace == "" {
		cfg.ArgoNamespace = "argocd"
	}
	if cfg.ChartName == "" {
		cfg.ChartName = "openstack-kamaji-cluster"
	}
	return cfg
}

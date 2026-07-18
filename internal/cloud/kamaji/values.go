package kamaji

import "strings"

// values.go is the SINGLE place the `openstack-kamaji-cluster` chart values contract lives.
//
// ⚠ CONTRACT UNVERIFIED (plan §3.0 Phase-0 groundwork): the chart source is OCI-only
// (ghcr.io/menlocloud/charts, not vendored in git). The keys below mirror the subchart values
// observed in infra-ops kamaji-cluster-az1 wrappers (stag-cluster/values.yaml: kubernetesVersion
// :42, oidc :103-138, kamajiControlPlane :148-213, nodeGroups :449-464) — but the node-group
// field spelling and the clouds.yaml secret knob MUST be verified against the chart source
// before the first live drill. Fix HERE only; nothing else in stratos knows chart keys.

// BuildValues renders the full chart values for a cluster. Full values (not a delta) by design:
// chart-default changes must never silently mutate a customer cluster (plan §9).
func BuildValues(cfg Config, spec ClusterSpec) map[string]any {
	d := cfg.Defaults

	cp := map[string]any{
		"replicas": 1,
	}
	if spec.HA {
		cp["replicas"] = 3
	}
	if d.DataStoreName != "" {
		cp["dataStoreName"] = d.DataStoreName
	}
	network := map[string]any{
		"serviceType": "LoadBalancer",
	}
	ann := map[string]any{}
	if d.FloatingNetworkID != "" {
		ann["loadbalancer.openstack.org/floating-network-id"] = d.FloatingNetworkID
	}
	if len(spec.AllowedCIDRs) > 0 {
		ann["loadbalancer.openstack.org/allowed-cidrs"] = strings.Join(spec.AllowedCIDRs, ",")
	}
	if d.DNSZone != "" {
		fqdn := spec.ID + "." + d.DNSZone
		ann["external-dns.alpha.kubernetes.io/hostname"] = fqdn
		network["certSANs"] = []any{fqdn}
	}
	if len(ann) > 0 {
		network["serviceAnnotations"] = ann
	}
	cp["network"] = network

	values := map[string]any{
		"kubernetesVersion":  spec.Version,
		"kamajiControlPlane": cp,
		// ⚠ UNVERIFIED knob: how the chart is told which secret holds clouds.yaml. The infra-ops
		// wrappers create it via an ExternalSecret template; the per-cluster secret stratos applies
		// is CloudSecretName(spec.ID) in the project namespace (mgmt-side only — D7).
		"cloudCredentialsSecretName": CloudSecretName(spec.ID),
	}
	if d.ExternalNetworkID != "" {
		values["clusterNetworking"] = map[string]any{"externalNetworkId": d.ExternalNetworkID}
	}
	if oidc := OIDCValues(spec.OIDC); oidc != nil {
		values["oidc"] = oidc
	}

	values["nodeGroups"] = NodeGroupValues(d, spec.Version, spec.NodeGroups)
	return values
}

// OIDCValues renders the chart's oidc block from the customer-supplied config — shared by
// BuildValues and the SET_OIDC action. nil (= OIDC disabled) when issuerUrl is empty.
func OIDCValues(oidc map[string]string) map[string]any {
	issuer := oidc["issuerUrl"]
	if issuer == "" {
		return nil
	}
	out := map[string]any{"issuerUrl": issuer}
	for _, k := range []string{"clientId", "usernameClaim", "usernamePrefix", "groupsClaim", "groupsPrefix", "signingAlgs"} {
		if v := oidc[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

// NodeGroupValues renders the chart's nodeGroups value — shared by BuildValues and the
// SET_NODE_GROUPS action (which swaps ONLY this key on the live Application values).
func NodeGroupValues(d ClusterDefaults, version string, groups []NodeGroup) []any {
	out := make([]any, 0, len(groups))
	for _, ng := range groups {
		img := ng.ImageID
		if img == "" {
			img = d.Versions[version]
		}
		g := map[string]any{
			"name":    ng.Name,
			"flavor":  ng.FlavorID,
			"imageId": img,
		}
		if ng.Autoscale {
			g["autoscale"] = true
			g["min"] = ng.Min
			g["max"] = ng.Max
		} else {
			g["count"] = ng.Count
		}
		if len(ng.Labels) > 0 {
			labels := map[string]any{}
			for k, v := range ng.Labels {
				labels[k] = v
			}
			g["nodeLabels"] = labels
		}
		if len(ng.Taints) > 0 {
			taints := make([]any, 0, len(ng.Taints))
			for _, t := range ng.Taints {
				taints = append(taints, t)
			}
			g["nodeTaints"] = taints
		}
		out = append(out, g)
	}
	return out
}

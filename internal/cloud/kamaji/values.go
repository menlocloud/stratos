package kamaji

import (
	"fmt"
	"strings"
)

// values.go is the SINGLE place the `openstack-kamaji-cluster` chart values contract lives.
//
// CONTRACT VERIFIED 2026-07-19 against the chart's default-values snapshot
// `values.upstream.yaml` (chart 0.2.3) vendored in the infra-ops wrappers
// (kubernetes/clusters/kamaji-cluster-az1/charts/{dev,sysadmin,stag,prod}-cluster) plus the live
// wrapper values. Confirmed keys: cloudCredentialsSecretName + cloudName ("openstack" default,
// matches CloudsYAML), kubernetesVersion, kamajiControlPlane.{dataStoreName,replicas,
// network.{serviceType,serviceAnnotations,certSANs}}, inline oidc.* (NO helm `lookup` — that is
// only the oidc.existingSecret mode we never use, so ArgoCD templating is safe),
// clusterNetworking.externalNetworkId (internalNetwork empty by default → CAPO creates a
// per-cluster network in the tenant), nodeGroups[].{name,machineFlavor,machineImageId,
// machineCount,machineCountMin,machineCountMax,autoscale,nodeLabels,taints(objects)},
// autoscaler.image.tag (MUST match the cluster's k8s minor — upstream comment), and
// addons.{enabled,openstack.enabled} with the wrapper's KAMAJI-FIX tolerations (with Kamaji all
// nodes are workers carrying uninitialized taints at bootstrap — CNI/CCM/CSI must tolerate them
// or the cluster deadlocks). STILL UNVERIFIED (needs the live drill / chart templates, which are
// not anonymously pullable from ghcr): MachineDeployment naming, autoscale annotation mechanics,
// upgrade rotation semantics. Fix HERE only; nothing else in stratos knows chart keys.

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

	// Cluster-autoscaler minor MUST match the cluster's Kubernetes minor (upstream values
	// comment); the default tag would drift as soon as we offer a different minor.
	if maj, min, _, err := ParseVersion(spec.Version); err == nil {
		values["autoscaler"] = map[string]any{
			"image": map[string]any{"tag": fmt.Sprintf("v%d.%d.0", maj, min)},
		}
	}

	// KAMAJI-FIX addon values, lifted from the infra-ops wrappers: with Kamaji every node is a
	// worker carrying the CAPI/cloud-provider uninitialized taints during bootstrap, so the CNI
	// and the OpenStack CCM/CSI-controller must tolerate everything (and the CSI controller must
	// not require a control-plane node) — without these the first node never becomes Ready.
	// openstack.enabled stays true: v1 runs OCCM/cinder-csi worker-side with the CUSTOMER's own
	// tenant-scoped credential (plan D4 — nothing to hide from the customer); the mgmt-side
	// placement (plan D7) is the documented follow-up.
	values["addons"] = map[string]any{
		"enabled": true,
		"cni": map[string]any{
			"enabled": true,
			"type":    "cilium",
			"cilium": map[string]any{
				"release": map[string]any{
					"values": map[string]any{
						"tolerations": []any{map[string]any{"operator": "Exists"}},
						"operator": map[string]any{
							"tolerations": []any{map[string]any{"operator": "Exists"}},
						},
					},
				},
			},
		},
		"openstack": map[string]any{
			"enabled": true,
			"csiCinder": map[string]any{
				"values": map[string]any{
					"csi": map[string]any{
						"plugin": map[string]any{
							"controllerPlugin": map[string]any{
								"nodeSelector": nil,
								"tolerations":  []any{map[string]any{"operator": "Exists"}},
							},
						},
					},
				},
			},
			"ccm": map[string]any{
				"values": map[string]any{
					"nodeSelector": map[string]any{"kubernetes.io/os": "linux"},
					"tolerations":  []any{map[string]any{"operator": "Exists"}},
				},
			},
		},
	}
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
// SET_NODE_GROUPS action (which swaps ONLY this key on the live Application values). Field
// spellings follow the verified chart contract (wrapper shape): machineCount is always set (the
// autoscaler's initial size for an autoscale group), min==max==count pins a fixed group.
func NodeGroupValues(d ClusterDefaults, version string, groups []NodeGroup) []any {
	out := make([]any, 0, len(groups))
	for _, ng := range groups {
		img := ng.ImageID
		if img == "" {
			img = d.Versions[version]
		}
		g := map[string]any{
			"name":           ng.Name,
			"machineFlavor":  ng.FlavorID,
			"machineImageId": img,
		}
		if ng.Autoscale {
			g["autoscale"] = true
			g["machineCount"] = ng.Min
			g["machineCountMin"] = ng.Min
			g["machineCountMax"] = ng.Max
		} else {
			g["autoscale"] = false
			g["machineCount"] = ng.Count
			g["machineCountMin"] = ng.Count
			g["machineCountMax"] = ng.Count
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
				if obj := taintToObject(t); obj != nil {
					taints = append(taints, obj)
				}
			}
			if len(taints) > 0 {
				g["taints"] = taints
			}
		}
		out = append(out, g)
	}
	return out
}

// taintToObject converts the kubeadm-style "key=value:Effect" (or "key:Effect") string the API
// accepts into the chart's taint object {key, value, effect}. nil for an unparseable string —
// ClusterSpec.Validate rejects those at the API boundary, so nil here is belt-and-braces.
func taintToObject(s string) map[string]any {
	kv, effect, ok := strings.Cut(s, ":")
	if !ok || effect == "" || kv == "" {
		return nil
	}
	key, value, _ := strings.Cut(kv, "=")
	if key == "" {
		return nil
	}
	obj := map[string]any{"key": key, "effect": effect}
	if value != "" {
		obj["value"] = value
	}
	return obj
}

// taintToString is the inverse mapping (chart taint object → "key=value:Effect"), used by the
// sync to keep the cache/UI representation in the string form the API accepts.
func taintToString(obj map[string]any) string {
	key, _ := obj["key"].(string)
	effect, _ := obj["effect"].(string)
	if key == "" || effect == "" {
		return ""
	}
	if value, _ := obj["value"].(string); value != "" {
		return key + "=" + value + ":" + effect
	}
	return key + ":" + effect
}

// ValidTaintEffects are the effects the kubelet accepts.
var ValidTaintEffects = map[string]bool{"NoSchedule": true, "PreferNoSchedule": true, "NoExecute": true}

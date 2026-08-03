package dbaas

// values.go — the SINGLE source of truth for the `database-cluster` chart values contract
// (deploy/charts/database-cluster). Every key BuildValues emits is a key the chart reads;
// TestBuildValues pins all of them per engine, so a rename on either side is a test failure,
// not a silently ignored value (the kamaji values.go lesson). No credentials in values, EVER —
// the Application CR is readable by anyone with argocd read; passwords ride in Secrets only.

// BuildValues renders the full chart values for one database. The generated values live inline
// on the Application (spec.source.helm.valuesObject) — the DB cluster is the state store,
// stratos stays stateless about desired spec.
func BuildValues(cfg Config, spec DatabaseSpec) map[string]any {
	storage := map[string]any{"sizeGi": spec.StorageGiB}
	if spec.StorageClass != "" {
		storage["storageClassName"] = spec.StorageClass
	}
	cidrs := make([]any, 0, len(spec.AllowedCIDRs))
	for _, c := range spec.AllowedCIDRs {
		cidrs = append(cidrs, c)
	}
	network := map[string]any{
		// The Octavia annotation feed: the LB VIP lands on the CUSTOMER's subnet, its
		// members pool on the DB cluster's node subnet (provider config).
		"networkId":      spec.NetworkID,
		"subnetId":       spec.SubnetID,
		"memberSubnetId": cfg.MemberSubnetID,
		"allowedCidrs":   cidrs,
	}
	// The chart derives every hostname (<id>.<zone>, -dash, -b<N>) from this one key — the
	// Config.HostnameFor twin. Only present when the provider runs the DNS feature, so older
	// databases' values stay byte-identical.
	if cfg.DNSZone != "" {
		network["dnsZone"] = cfg.DNSZone
	}
	values := map[string]any{
		"engine":        spec.Engine,
		"engineVersion": spec.Version,
		"instances":     spec.Replicas,
		"resources":     map[string]any{"cpu": spec.CPU, "memoryGi": spec.MemoryGiB},
		"storage":       storage,
		"network":       network,
		"stratos": map[string]any{
			"projectId":   spec.ProjectID,
			"resourceId":  spec.ID,
			"displayName": spec.DisplayName,
		},
	}
	// Emitted for opensearch only, and only the keys that are on: the chart defaults everything
	// off, and SET_SSO/SET_CUSTOM_DOMAIN patch this same block later — unconditional keys here
	// would fight those patches.
	if spec.Engine == EngineOpenSearch {
		osBlock := map[string]any{}
		if spec.DashboardsEnabled {
			osBlock["dashboards"] = map[string]any{"enabled": true}
		}
		// Real certs need both halves: a name to certify (dnsZone) and an issuer that can
		// DNS-01 it. Either alone keeps the operator's self-signed pair.
		if cfg.DNSZone != "" && cfg.CertIssuer != "" {
			osBlock["certIssuer"] = cfg.CertIssuer
		}
		if len(osBlock) > 0 {
			values["opensearch"] = osBlock
		}
	}
	return values
}

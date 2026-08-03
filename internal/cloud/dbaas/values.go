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
		// SSO rides Dashboards (the chart reads the sso block only under dashboards.enabled),
		// so asking for SSO at create implies them — same rule SET_SSO applies later.
		if spec.DashboardsEnabled || len(spec.SSO) > 0 {
			osBlock["dashboards"] = map[string]any{"enabled": true}
		}
		if len(spec.SSO) > 0 {
			sso := map[string]any{"enabled": true}
			for k, v := range spec.SSO {
				sso[k] = v
			}
			osBlock["sso"] = sso
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

// databasesValues / usersValues render the customer-managed access lists into the values
// contract the chart's per-engine templates read (postgresql/mariadb databases + roles,
// opensearch users + role bindings). NEVER a password — only the NAME of the Secret stratos
// wrote, which the engine CR references.
func databasesValues(dbs []DBDatabase, users []DBUser) []any {
	owners := effectiveOwners(dbs, users)
	out := make([]any, 0, len(dbs))
	for _, d := range dbs {
		entry := map[string]any{"name": d.Name}
		if o := owners[d.Name]; o != "" {
			entry["owner"] = o
		}
		out = append(out, entry)
	}
	return out
}

// effectiveOwners fills in the owner of every database that did not name one: the FIRST user
// that asked for it. Load-bearing for postgres, where "user may use database" is expressed as
// membership of the database's owner role — leaving an owner blank would hand the database to
// the engine's own app role and silently grant the customer's users nothing at all. A database
// nobody listed keeps a blank owner and falls back to the app user in the chart.
func effectiveOwners(dbs []DBDatabase, users []DBUser) map[string]string {
	out := make(map[string]string, len(dbs))
	for _, d := range dbs {
		out[d.Name] = d.Owner
	}
	for _, u := range users {
		for _, name := range u.Databases {
			if cur, known := out[name]; known && cur == "" {
				out[name] = u.Name
			}
		}
	}
	return out
}

// osRolesValues renders customer-defined roles. The chart turns each into an OpensearchRole CR
// whose NAME is the role name OpenSearch sees (prefixed, like users — one namespace holds every
// database in the project), so `role` echoes the effective name back for display.
func osRolesValues(roles []OSRole) []any {
	out := make([]any, 0, len(roles))
	for _, r := range roles {
		patterns := make([]any, 0, len(r.IndexPatterns))
		for _, p := range r.IndexPatterns {
			patterns = append(patterns, p)
		}
		actions := make([]any, 0, len(r.Actions))
		for _, a := range r.Actions {
			actions = append(actions, a)
		}
		out = append(out, map[string]any{
			"name":          r.Name,
			"indexPatterns": patterns,
			"actions":       actions,
		})
	}
	return out
}

// osIndexPoliciesValues renders retention policies. Only the customer-facing knobs travel; the
// chart expands them into the real ISM state machine.
func osIndexPoliciesValues(policies []OSIndexPolicy) []any {
	out := make([]any, 0, len(policies))
	for _, p := range policies {
		patterns := make([]any, 0, len(p.IndexPatterns))
		for _, pat := range p.IndexPatterns {
			patterns = append(patterns, pat)
		}
		entry := map[string]any{
			"name":            p.Name,
			"indexPatterns":   patterns,
			"deleteAfterDays": p.DeleteAfterDays,
		}
		if p.RolloverGiB > 0 {
			entry["rolloverGiB"] = p.RolloverGiB
		}
		if p.RolloverDays > 0 {
			entry["rolloverDays"] = p.RolloverDays
		}
		out = append(out, entry)
	}
	return out
}

func usersValues(dbs []DBDatabase, users []DBUser) []any {
	// A postgres role reaches a database by being a member of that database's owner role, so
	// each user's grants become inRoles on the owners of the databases it listed.
	owner := effectiveOwners(dbs, users)
	out := make([]any, 0, len(users))
	for _, u := range users {
		entry := map[string]any{"name": u.Name}
		if len(u.Databases) > 0 {
			names := make([]any, 0, len(u.Databases))
			owners := make([]any, 0, len(u.Databases))
			seen := map[string]bool{}
			for _, d := range u.Databases {
				names = append(names, d)
				if o := owner[d]; o != "" && o != u.Name && !seen[o] {
					seen[o] = true
					owners = append(owners, o)
				}
			}
			entry["databases"] = names
			if len(owners) > 0 {
				entry["inRoles"] = owners
			}
		}
		if len(u.Roles) > 0 {
			roles := make([]any, 0, len(u.Roles))
			for _, r := range u.Roles {
				roles = append(roles, r)
			}
			entry["roles"] = roles
		}
		out = append(out, entry)
	}
	return out
}

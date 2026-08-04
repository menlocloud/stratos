package dbaas

import (
	"encoding/json"
	"os"
	"testing"
)

// TestDumpValuesForHelm is a dev tool, skipped unless DUMP_VALUES names a directory. It writes
// the REAL BuildValues output per engine so the chart can be rendered and dry-run against the
// live operator webhooks:
//
//	DUMP_VALUES=/tmp go test ./internal/cloud/dbaas/ -run TestDumpValuesForHelm
//	helm template t deploy/charts/database-cluster -f /tmp/gen-mysql.json | \
//	  kubectl -n scratch apply --dry-run=server --server-side -f -
//
// Hand-written values files drift from the contract and quietly render a DIFFERENT object than
// production would; every latent CR bug this feature has shipped was found this way and none of
// them by reading the operator's docs.
func TestDumpValuesForHelm(t *testing.T) {
	out := os.Getenv("DUMP_VALUES")
	if out == "" {
		t.Skip("set DUMP_VALUES")
	}
	cfg := testConfig()
	cfg.Backup = BackupConfig{Endpoint: "https://s3.example", Bucket: "backups",
		AccessKey: "AKIA", SecretKey: "sec", PathStyle: true}
	cfg.DNSZone = "db.example.com"
	for engine, v := range map[string]string{EnginePostgreSQL: "17", EngineMySQL: "8.4", EngineMariaDB: "11.4", EngineFerretDB: "2.7"} {
		b, _ := json.MarshalIndent(BuildValues(cfg, testSpec(engine, v)), "", " ")
		if err := os.WriteFile(out+"/gen-"+engine+".json", b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

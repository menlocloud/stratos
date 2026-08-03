{{/*
Base name for every object this chart renders: the stratos resource id
(std-xxxxxxxx), NOT the release name. internal/cloud/dbaas derives secret and
service names from the resource id (ConnectionSecret tuples, <id>-lb), so the
release name must stay an ArgoCD implementation detail.
*/}}
{{- define "database-cluster.name" -}}
{{- required "stratos.resourceId is required (stamped by internal/cloud/dbaas/values.go BuildValues)" .Values.stratos.resourceId -}}
{{- end }}

{{/*
Fail-fast contract validation. Included from service-lb.yaml (which is
evaluated for every engine — for kafka the Service body is skipped but this
include still runs), so a values document missing required keys can never
produce a half-formed database. Mirrors the Validate() checks in
internal/cloud/dbaas — this side is the last line of defence, not the UX.
*/}}
{{- define "database-cluster.validate" -}}
{{- $engines := list "postgresql" "mysql" "mariadb" "valkey" "ferretdb" "opensearch" "kafka" -}}
{{- if not .Values.engine -}}
{{- fail "engine is required (postgresql|mysql|mariadb|valkey|ferretdb|opensearch|kafka)" -}}
{{- end -}}
{{- if not (has .Values.engine $engines) -}}
{{- fail (printf "unknown engine %q (want postgresql|mysql|mariadb|valkey|ferretdb|opensearch|kafka)" .Values.engine) -}}
{{- end -}}
{{- if not .Values.engineVersion -}}
{{- fail "engineVersion is required" -}}
{{- end -}}
{{- if not .Values.instances -}}
{{- fail "instances is required (>= 1)" -}}
{{- end -}}
{{- if not .Values.resources.cpu -}}
{{- fail "resources.cpu is required" -}}
{{- end -}}
{{- if not .Values.resources.memoryGi -}}
{{- fail "resources.memoryGi is required" -}}
{{- end -}}
{{- if and (ne .Values.engine "ferretdb") (not .Values.storage.sizeGi) -}}
{{- fail "storage.sizeGi is required" -}}
{{- end -}}
{{- if not .Values.network.networkId -}}
{{- fail "network.networkId is required" -}}
{{- end -}}
{{- if not .Values.network.subnetId -}}
{{- fail "network.subnetId is required" -}}
{{- end -}}
{{- if not .Values.network.memberSubnetId -}}
{{- fail "network.memberSubnetId is required" -}}
{{- end -}}
{{- if not .Values.stratos.projectId -}}
{{- fail "stratos.projectId is required" -}}
{{- end -}}
{{- if not .Values.stratos.resourceId -}}
{{- fail "stratos.resourceId is required" -}}
{{- end -}}
{{- end }}

{{/*
Common labels. app.kubernetes.io/managed-by is the stratos ownership marker the
sync provider filters on; stratos.io/project ties every object back to the
owning project. app.kubernetes.io/instance doubles as the NetworkPolicy pod
selector — the engine templates stamp it onto their pods (CNPG via
inheritedMetadata; the operators propagate the CR name as instance by
convention).
*/}}
{{- define "database-cluster.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: stratos
app.kubernetes.io/instance: {{ include "database-cluster.name" . }}
stratos.io/project: {{ required "stratos.projectId is required" .Values.stratos.projectId | quote }}
{{- end }}

{{/*
Common annotations. Display name is an annotation, not a label — it is
free-form user input and label values have a restricted charset.
*/}}
{{- define "database-cluster.annotations" -}}
{{- with .Values.stratos.displayName }}
stratos.io/display-name: {{ . | quote }}
{{- end }}
{{- end }}

{{/*
Platform DNS name of the main endpoint: <resourceId>.<dnsZone>. Empty string
when the provider has no zone — callers gate on it with `with`/`if`. The
Dashboards (-dash) and kafka broker (-b<N>) names are derived inline at their
templates from the same two parts. Derivation twin:
internal/cloud/dbaas/config.go HostnameFor — change both together.
*/}}
{{- define "database-cluster.dnsName" -}}
{{- if .Values.network.dnsZone -}}
{{- printf "%s.%s" (include "database-cluster.name" .) .Values.network.dnsZone -}}
{{- end -}}
{{- end }}

{{/*
Engine -> client port. The single port the LB Service exposes (for kafka —
whose LB Services are Strimzi-owned, not chart-owned — this is the external
listener port, used by the NetworkPolicy).
*/}}
{{- define "database-cluster.port" -}}
{{- $ports := dict "postgresql" 5432 "mysql" 3306 "mariadb" 3306 "valkey" 6379 "ferretdb" 27017 "opensearch" 9200 "kafka" 9094 -}}
{{- index $ports .Values.engine -}}
{{- end }}

{{/*
engineVersion -> image, per engine. Ops-updatable per chart release: bump a
tag here -> bump Chart.yaml version -> re-pin providers. A version with no
mapping fails the render — better a loud sync error than a silently wrong
major. Every tag below was verified to exist against its registry's tags API
on 2026-08-03 (Docker Hub / ghcr), and the mysql tags are exactly the builds
Percona documents as tested with ps-operator 1.2.0.

opensearch/kafka have NO map here BY DESIGN: both operators resolve images
from the version field natively (OpenSearchCluster spec.general.version,
Kafka spec.kafka.version), so their templates never call this helper — the
guard below turns an accidental future call into a loud render failure.
*/}}
{{- define "database-cluster.image" -}}
{{- if has .Values.engine (list "opensearch" "kafka") -}}
{{- fail (printf "database-cluster.image must not be called for %s — its operator resolves the image from engineVersion natively" .Values.engine) -}}
{{- end -}}
{{- $v := .Values.engineVersion | toString -}}
{{- $maps := dict
      "postgresql" (dict
        "14" "ghcr.io/cloudnative-pg/postgresql:14.23"
        "15" "ghcr.io/cloudnative-pg/postgresql:15.18"
        "16" "ghcr.io/cloudnative-pg/postgresql:16.14"
        "17" "ghcr.io/cloudnative-pg/postgresql:17.10"
        "18" "ghcr.io/cloudnative-pg/postgresql:18.4")
      "mysql" (dict
        "8.0" "docker.io/percona/percona-server:8.0.46-37"
        "8.4" "docker.io/percona/percona-server:8.4.10-10")
      "mariadb" (dict
        "10.6" "docker.io/library/mariadb:10.6.27"
        "10.11" "docker.io/library/mariadb:10.11.18"
        "11.4" "docker.io/library/mariadb:11.4.12"
        "11.8" "docker.io/library/mariadb:11.8.8")
      "valkey" (dict
        "7.2" "docker.io/valkey/valkey:7.2.14"
        "8.0" "docker.io/valkey/valkey:8.0.10"
        "8.1" "docker.io/valkey/valkey:8.1.9")
      "ferretdb" (dict
        "2.5" "ghcr.io/ferretdb/ferretdb:2.5.0"
        "2.7" "ghcr.io/ferretdb/ferretdb:2.7.0")
-}}
{{- $m := index $maps .Values.engine -}}
{{- $img := index $m $v -}}
{{- if not $img -}}
{{- fail (printf "no image mapping for %s %q — extend database-cluster.image in _helpers.tpl (and release a new chart version)" .Values.engine $v) -}}
{{- end -}}
{{- $img -}}
{{- end }}

{{/*
FerretDB BACKEND image (the DocumentDB-flavoured postgres CNPG runs). It is a MATCHED PAIR
with the frontend — a frontend on 2.7 against a 2.5 backend is unsupported — so it is mapped
off the SAME engineVersion instead of being pinned in values. That pairing is what makes
UPGRADE safe for ferretdb: one version bump moves both halves together.
An explicit values.ferretdb.postgresImage still wins, for pinning during an incident.
*/}}
{{- define "database-cluster.ferretdbPostgresImage" -}}
{{- if .Values.ferretdb.postgresImage -}}
{{- .Values.ferretdb.postgresImage -}}
{{- else -}}
{{- $v := .Values.engineVersion | toString -}}
{{- $pairs := dict
      "2.5" "ghcr.io/ferretdb/postgres-documentdb:17-0.106.0-ferretdb-2.5.0"
      "2.7" "ghcr.io/ferretdb/postgres-documentdb:17-0.107.0-ferretdb-2.7.0"
-}}
{{- $img := index $pairs $v -}}
{{- if not $img -}}
{{- fail (printf "no backend image pair for ferretdb %q — extend database-cluster.ferretdbPostgresImage in _helpers.tpl (and release a new chart version)" $v) -}}
{{- end -}}
{{- $img -}}
{{- end -}}
{{- end }}

{{/*
FerretDB frontend image: explicit override wins, else the engineVersion map.
*/}}
{{- define "database-cluster.ferretdbImage" -}}
{{- if .Values.ferretdb.image -}}
{{- .Values.ferretdb.image -}}
{{- else -}}
{{- include "database-cluster.image" . -}}
{{- end -}}
{{- end }}

{{/*
Guaranteed-QoS resources block (requests == limits): the customer pays for the
full size, so the full size is reserved.
*/}}
{{- define "database-cluster.resources" -}}
requests:
  cpu: {{ .Values.resources.cpu | quote }}
  memory: {{ .Values.resources.memoryGi }}Gi
limits:
  cpu: {{ .Values.resources.cpu | quote }}
  memory: {{ .Values.resources.memoryGi }}Gi
{{- end }}

{{/*
Per-engine pod selector for the LB Service: the WRITE endpoint of each engine.
  postgresql: the CNPG primary. TODO-verify(drill): cnpg.io/instanceRole is the
              current label on CNPG >= 1.25 (older docs show `role`); if 1.29
              drops it, fall back to CNPG managed.services.additional.
  mysql:      the Percona operator's HAProxy pods (routes writes to the GR
              primary). TODO-verify(drill): label pair on the pinned operator.
  mariadb:    the CHART-OWNED HAProxy deployment (templates/mariadb/) — the
              operator's <id>-primary Service is pod-name-pinned, not
              selectable.
  valkey:     best-known valkey-operator labels. TODO-verify(beta): unverified
              until the valkey CRD is pinned — see templates/valkey/valkey.yaml.
  ferretdb:   the chart's own frontend Deployment (labels stamped by us).
  opensearch: the operator's node-pool pods (any node serves reads/writes on
              9200 — no primary/replica split to select). Label pair verified
              on reference manifests; TODO-verify(drill): operator 3.x is
              migrating the api group opster.io -> opensearch.org, re-check
              the pod labels on 3.0.2.
  kafka:      NOT USED — service-lb.yaml skips kafka entirely; Strimzi owns
              the external bootstrap + per-broker LB Services (see
              LBServiceNameFor in internal/cloud/dbaas/engines.go).
*/}}
{{- define "database-cluster.serviceSelector" -}}
{{- $name := include "database-cluster.name" . -}}
{{- if eq .Values.engine "postgresql" -}}
cnpg.io/cluster: {{ $name }}
cnpg.io/instanceRole: primary
{{- else if eq .Values.engine "mysql" -}}
app.kubernetes.io/instance: {{ $name }}
app.kubernetes.io/component: haproxy
{{- else if eq .Values.engine "mariadb" -}}
app.kubernetes.io/name: mariadb-haproxy
app.kubernetes.io/instance: {{ $name }}
{{- else if eq .Values.engine "valkey" -}}
app.kubernetes.io/name: valkey
app.kubernetes.io/instance: {{ $name }}
{{- else if eq .Values.engine "ferretdb" -}}
app.kubernetes.io/name: ferretdb
app.kubernetes.io/instance: {{ $name }}
{{- else if eq .Values.engine "opensearch" -}}
opster.io/opensearch-cluster: {{ $name }}
opensearch.opster.io/component: nodes
{{- end -}}
{{- end }}

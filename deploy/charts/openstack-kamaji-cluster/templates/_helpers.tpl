{{/*
Create a cluster name. The name of the cluster is just the release name.
*/}}
{{- define "openstack-kamaji-cluster.clusterName" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Create a name for a cluster component.
*/}}
{{- define "openstack-kamaji-cluster.componentName" -}}
{{- $ctx := index . 0 -}}
{{- $componentName := index . 1 -}}
{{- printf "%s-%s" $ctx.Release.Name $componentName | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "openstack-kamaji-cluster.chart" -}}
{{-
  printf "%s-%s" .Chart.Name .Chart.Version |
    replace "+" "_" |
    trunc 63 |
    trimSuffix "-" |
    trimSuffix "." |
    trimSuffix "_"
}}
{{- end }}

{{/*
Common labels
*/}}
{{- define "openstack-kamaji-cluster.commonLabels" -}}
helm.sh/chart: {{ include "openstack-kamaji-cluster.chart" . }}
{{ .Values.projectPrefix }}/managed-by: {{ .Release.Service }}
{{ .Values.projectPrefix }}/infrastructure-provider: openstack
{{- end -}}

{{/*
Selector labels for cluster-level resources
*/}}
{{- define "openstack-kamaji-cluster.selectorLabels" -}}
{{ .Values.projectPrefix }}/cluster: {{ include "openstack-kamaji-cluster.clusterName" . }}
{{- end -}}

{{/*
Labels for cluster-level resources
*/}}
{{- define "openstack-kamaji-cluster.labels" -}}
{{ include "openstack-kamaji-cluster.commonLabels" . }}
{{ include "openstack-kamaji-cluster.selectorLabels" . }}
{{- end -}}

{{/*
Selector labels for component-level resources
*/}}
{{- define "openstack-kamaji-cluster.componentSelectorLabels" -}}
{{- $ctx := index . 0 -}}
{{- $componentName := index . 1 -}}
{{ include "openstack-kamaji-cluster.selectorLabels" $ctx }}
{{ $ctx.Values.projectPrefix }}/component: {{ $componentName }}
{{- end -}}

{{/*
Labels for component-level resources
*/}}
{{- define "openstack-kamaji-cluster.componentLabels" -}}
{{ include "openstack-kamaji-cluster.commonLabels" (index . 0) }}
{{ include "openstack-kamaji-cluster.componentSelectorLabels" . }}
{{- end -}}

{{/*
Name of the Kamaji-managed Service exposing the apiserver. Kamaji creates
the Service with the same name as the KamajiControlPlane / TenantControlPlane.
*/}}
{{- define "openstack-kamaji-cluster.kamajiServiceName" -}}
{{- include "openstack-kamaji-cluster.componentName" (list . "kamaji-cp") -}}
{{- end -}}

{{/*
Name of the Secret holding the tenant admin kubeconfig. The Kamaji control
plane provider names the TenantControlPlane after the KamajiControlPlane, and
Kamaji derives the Secret from the TCP name. Keys: admin.conf (external
endpoint), admin.svc (in-cluster Service URL — the one management-side
components must use), plus the super-admin variants.
*/}}
{{- define "openstack-kamaji-cluster.kamajiAdminKubeconfigSecretName" -}}
{{- printf "%s-admin-kubeconfig" (include "openstack-kamaji-cluster.kamajiServiceName" .) -}}
{{- end -}}

{{/*
Resolve the effective OIDC config. Returns a YAML dict with the OIDC fields
resolved either from inline values or from an existing Secret (when
`oidc.existingSecret.name` is set).

Behaviour:
  * Defaults to inline `.Values.oidc.*` fields.
  * When `existingSecret.name` is set, looks up the Secret in the release
    namespace and overlays the values it finds (keys mapped via
    `existingSecret.keys`). Missing keys fall back to inline values.
  * Returns an empty dict (no `issuerUrl`) when OIDC is not configured.

Caveat: `lookup` returns nil during `helm template` (no cluster). In that
case the inline values are used as-is.
*/}}
{{- define "openstack-kamaji-cluster.oidc.resolve" -}}
{{- $oidc := .Values.oidc | default dict -}}
{{- $existing := dig "existingSecret" "name" "" $oidc -}}
{{- $resolved := dict
  "issuerUrl"      ($oidc.issuerUrl      | default "")
  "clientId"       ($oidc.clientId       | default "")
  "usernameClaim"  ($oidc.usernameClaim  | default "sub")
  "usernamePrefix" ($oidc.usernamePrefix | default "oidc:")
  "groupsClaim"    ($oidc.groupsClaim    | default "groups")
  "groupsPrefix"   ($oidc.groupsPrefix   | default "oidc:")
  "signingAlgs"    ($oidc.signingAlgs    | default "RS256")
-}}
{{- if $existing -}}
{{- $secret := lookup "v1" "Secret" .Release.Namespace $existing -}}
{{- if and $secret $secret.data -}}
{{- $keys := dig "existingSecret" "keys" dict $oidc -}}
{{- range $field, $defaultKey := dict
    "issuerUrl"      "issuer-url"
    "clientId"       "client-id"
    "usernameClaim"  "username-claim"
    "usernamePrefix" "username-prefix"
    "groupsClaim"    "groups-claim"
    "groupsPrefix"   "groups-prefix"
    "signingAlgs"    "signing-algs"
}}
{{- $key := dig $field $defaultKey $keys -}}
{{- if hasKey $secret.data $key -}}
{{- $_ := set $resolved $field (index $secret.data $key | b64dec) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $resolved -}}
{{- end -}}

{{/*
Name of the secret containing the cloud credentials.
*/}}
{{- define "openstack-kamaji-cluster.cloudCredentialsSecretName" -}}
{{- if .Values.cloudCredentialsSecretName -}}
{{- .Values.cloudCredentialsSecretName -}}
{{- else -}}
{{ include "openstack-kamaji-cluster.componentName" (list . "cloud-credentials") -}}
{{- end -}}
{{- end -}}

{{/*
Template that merges two variables with the latter taking precedence and outputs the result as YAML.
Lists are merged by concatenating them rather than overwriting.
*/}}
{{- define "openstack-kamaji-cluster.mergeConcat" -}}
{{- $left := index . 0 }}
{{- $right := index . 1 }}
{{- if kindIs (kindOf list) $left }}
{{- if kindIs (kindOf list) $right }}
{{ concat $left $right | toYaml }}
{{- else }}
{{ default $left $right | toYaml }}
{{- end }}
{{- else if kindIs (kindOf dict) $left }}
{{- if kindIs (kindOf dict) $right }}
{{- range $key := concat (keys $left) (keys $right) | uniq }}
{{- if and (hasKey $left $key) (hasKey $right $key) }}
{{- $merged := include "openstack-kamaji-cluster.mergeConcat" (list (index $left $key) (index $right $key)) }}
{{ $key }}: {{ $merged | nindent 2 }}
{{- else if hasKey $left $key }}
{{ index $left $key | dict $key | toYaml }}
{{- else }}
{{ index $right $key | dict $key | toYaml }}
{{- end }}
{{- end }}
{{- else }}
{{ default $left $right | toYaml }}
{{- end }}
{{- else }}
{{ default $left $right | toYaml }}
{{- end }}
{{- end }}

{{/*
Applies a list of templates to an input object sequentially.
*/}}
{{- define "openstack-kamaji-cluster.mergeConcatMany" -}}
{{- $obj := first . }}
{{- range $overrides := rest . }}
{{- $obj = include "openstack-kamaji-cluster.mergeConcat" (list $obj $overrides) | fromYaml }}
{{- end }}
{{- toYaml $obj }}
{{- end }}

{{/*
Outputs the node registration object for setting node labels.
*/}}
{{- define "openstack-kamaji-cluster.nodeRegistration.nodeLabels" -}}
nodeRegistration:
  kubeletExtraArgs:
    - name: node-labels
      value: "{{ range $i, $k := (keys . | sortAlpha) }}{{ if ne $i 0 }},{{ end }}{{ $k }}={{ index $ $k }}{{ end }}"
{{- end }}

{{/*
Outputs the node registration object for setting node taints via
kubelet `--register-with-taints`. Empty when no taints are defined so the
mergeConcat chain becomes a no-op.
Format per taint: key=value:effect (value omitted when not set).
*/}}
{{- define "openstack-kamaji-cluster.nodeRegistration.taints" -}}
{{- if . }}
nodeRegistration:
  kubeletExtraArgs:
    - name: register-with-taints
      value: "{{ range $i, $t := . }}{{ if ne $i 0 }},{{ end }}{{ $t.key }}{{ if hasKey $t "value" }}={{ $t.value }}{{ end }}:{{ $t.effect }}{{ end }}"
{{- end }}
{{- end }}

{{/*
Converts the tags in a Neutron filter when required.
*/}}
{{- define "openstack-kamaji-cluster.convert.tags" -}}
{{- if kindIs "string" . -}}
{{- splitList "," . | toYaml }}
{{- else -}}
{{- toYaml . }}
{{- end }}
{{- end }}

{{/*
Converts a v1alpha7 Neutron ports filter to a v1beta1 filter.
*/}}
{{- define "openstack-kamaji-cluster.convert.neutronPortsFilter" -}}
{{- $ports := list -}}
{{- range $p := . -}}
{{- if $p.network -}}
{{- with $p.network -}}
{{- if not ( hasKey . "filter" ) -}}
{{- $portNetwork := include "openstack-kamaji-cluster.convert.neutronFilter" . | fromYaml -}}
{{- $p := set $p "network" $portNetwork -}}
{{- end -}}
{{- $ports = append $ports $p -}}
{{- end -}}
{{- else -}}
{{- $ports = append $ports $p -}}
{{- end -}}
{{- end -}}
{{- toYaml $ports }}
{{- end }}

{{/*
Converts a v1alpha7 Neutron filter to a v1beta1 filter.
*/}}
{{/*
`.id` rather than `hasKey . "id"`: a filter written as `{id: }` has the key but a nil value, and
emitting `id:` for it produces a resource reference to nothing — CAPO neither creates the object
nor finds one, and the failure surfaces far downstream. Fall through to the name/tag filter form
instead, which the callers then reject if it too is empty.
*/}}
{{- define "openstack-kamaji-cluster.convert.neutronFilter" -}}
{{- if .id -}}
id: {{ .id }}
{{- else -}}
filter:
  {{- with omit . "tags" "tagsAny" "notTags" "notTagsAny" }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
  {{- with .tags }}
  tags: {{ include "openstack-kamaji-cluster.convert.tags" . | nindent 4 }}
  {{- end }}
  {{- with .tagsAny }}
  tagsAny: {{ include "openstack-kamaji-cluster.convert.tags" . | nindent 4 }}
  {{- end }}
  {{- with .notTags }}
  notTags: {{ include "openstack-kamaji-cluster.convert.tags" . | nindent 4 }}
  {{- end }}
  {{- with .notTagsAny }}
  notTagsAny: {{ include "openstack-kamaji-cluster.convert.tags" . | nindent 4 }}
  {{- end }}
{{- end }}
{{- end }}

{{/*
Outputs the content for a containerd registry file containing mirror configuration.
*/}}
{{- define "openstack-kamaji-cluster.registryFile" -}}
{{- $registry := index . 0 -}}
{{- $registrySpec := index . 1 -}}
{{-
  $defaultUpstream :=
    eq $registry "docker.io" |
    ternary "registry-1.docker.io" $registry |
    printf "https://%s"
-}}
{{-
  $upstream :=
    kindIs "map" $registrySpec |
    ternary $registrySpec dict |
    dig "upstream" $defaultUpstream
-}}
{{-
  $mirrors :=
    kindIs "map" $registrySpec |
    ternary $registrySpec (dict "mirrors" $registrySpec) |
    dig "mirrors" list
-}}
{{- with $upstream }}
server = "{{ . }}"
{{- end }}
{{- range $mirror := $mirrors }}
{{-
  $url :=
    kindIs "map" $mirror |
    ternary $mirror (dict "url" $mirror) |
    dig "url" "" |
    required "unable to determine mirror url"
}}
{{-
  $capabilities :=
    kindIs "map" $mirror |
    ternary $mirror (dict "capabilities" list) |
    dig "capabilities" list |
    default (list "pull" "resolve")
}}
{{-
  $skipVerify :=
    kindIs "map" $mirror |
    ternary $mirror (dict "skipVerify" false) |
    dig "skipVerify" false
}}
{{-
  $overridePath :=
    kindIs "map" $mirror |
    ternary $mirror (dict "overridePath" true) |
    dig "overridePath" true
}}
[host."{{ $url }}"]
capabilities = [{{ range $i, $cap := $capabilities }}{{ if gt $i 0 }}, {{ end }}"{{ . }}"{{ end }}]
skip_verify = {{ ternary "true" "false" $skipVerify }}
override_path = {{ ternary "true" "false" $overridePath }}
{{- end }}
{{- end }}

{{/*
Produces the kubeadmConfigSpec required to configure containerd.
*/}}
{{- define "openstack-kamaji-cluster.kubeadmConfigSpec.containerd" -}}
files:
  - path: /etc/containerd/conf.d/.keepdir
    content: |
      # This file is created by the capi-helm-chart to ensure that its parent directory exists
    owner: root:root
    permissions: "0644"
  - path: /etc/containerd/certs.d/.keepdir
    content: |
      # This file is created by the capi-helm-chart to ensure that its parent directory exists
    owner: root:root
    permissions: "0644"
{{- with .Values.registryMirrors }}
{{- range $registry, $registrySpec := . }}
  - path: /etc/containerd/certs.d/{{ $registry }}/hosts.toml
    content: |
      {{- include "openstack-kamaji-cluster.registryFile" (list $registry $registrySpec) | nindent 6 }}
    owner: root:root
    permissions: "0644"
{{- end }}
{{- end }}
{{- if .Values.registryAuth }}
  - path: /etc/containerd/conf.d/auth.toml
    contentFrom:
      secret:
        name: {{ include "openstack-kamaji-cluster.componentName" (list . "containerd-auth") }}
        key: "auth.toml"
    owner: root:root
    permissions: "0644"
{{- end }}
{{- if ne .Values.osDistro "flatcar" }}
preKubeadmCommands:
  - |
      /usr/bin/bash -s <<EOF
      grep -q '\[plugins."io.containerd.grpc.v1.cri".registry\]' /etc/containerd/config.toml && exit
      cat <<CONTENT >> /etc/containerd/config.toml
      [plugins."io.containerd.grpc.v1.cri".registry]
        config_path = "/etc/containerd/certs.d"
      CONTENT
      systemctl restart containerd
      EOF
{{- end }}
{{- end }}

{{/*
Produces the kubeadmConfigSpec required to configure additional trusted CAs for cluster nodes,
e.g. for private registries.
*/}}
{{- define "openstack-kamaji-cluster.kubeadmConfigSpec.trustedCAs" -}}
{{- with .Values.trustedCAs }}
files:
  {{- range $name, $certificate := . }}
  - path: /usr/local/share/ca-certificates/{{ $name }}.crt
    content: |
      {{- nindent 6 $certificate }}
    owner: root:root
    permissions: "0644"
  {{- end }}
preKubeadmCommands:
  - update-ca-certificates
{{- end }}
{{- end }}

{{/*
Produces the kubeadmConfigSpec required to install additional packages.
*/}}
{{- define "openstack-kamaji-cluster.kubeadmConfigSpec.additionalPackages" -}}
{{- with .Values.additionalPackages }}
preKubeadmCommands:
  - apt update -y
  - apt install -y {{ join " " . }}
{{- end }}
{{- end }}

{{/*
Produces the spec for a KubeadmConfig object.
*/}}
{{- define "openstack-kamaji-cluster.kubeadmConfigSpec" -}}
{{- $ctx := index . 0 }}
{{- $kubeadmConfigSpec := index . 1 }}
{{-
  list
    (include "openstack-kamaji-cluster.kubeadmConfigSpec.trustedCAs" $ctx | fromYaml)
    (include "openstack-kamaji-cluster.kubeadmConfigSpec.containerd" $ctx | fromYaml)
    (include "openstack-kamaji-cluster.kubeadmConfigSpec.additionalPackages" $ctx | fromYaml)
    $kubeadmConfigSpec |
  include "openstack-kamaji-cluster.mergeConcatMany"
}}
{{- end }}

{{/*
Produces the spec for an Ignition based OS specific KubeadmConfig object conditional on osDistro set to "flatcar".
*/}}
{{- define "openstack-kamaji-cluster.flatcarKubeadmConfigSpec" -}}
initConfiguration:
  nodeRegistration:
    name: ${COREOS_OPENSTACK_HOSTNAME}
joinConfiguration:
  nodeRegistration:
    name: ${COREOS_OPENSTACK_HOSTNAME}
preKubeadmCommands:
  - export COREOS_OPENSTACK_HOSTNAME=${COREOS_OPENSTACK_HOSTNAME%.*}
  - envsubst < /etc/kubeadm.yml > /etc/kubeadm.yml.tmp
  - mv /etc/kubeadm.yml.tmp /etc/kubeadm.yml
format: ignition
ignition:
  containerLinuxConfig:
    additionalConfig: |
      systemd:
        units:
        - name: coreos-metadata-sshkeys@.service
          enabled: true
        - name: kubeadm.service
          enabled: true
          dropins:
          - name: 10-flatcar.conf
            contents: |
              [Unit]
              Requires=containerd.service coreos-metadata.service
              After=containerd.service coreos-metadata.service
              [Service]
              EnvironmentFile=/run/metadata/flatcar
{{- end }}

{{- define "openstack-kamaji-cluster.osDistroKubeadmConfigSpec" }}
{{- $ctx := index . 0 }}
{{- $osDistro := $ctx.Values.osDistro }}
{{- if eq $osDistro "flatcar" }}
{{- include "openstack-kamaji-cluster.flatcarKubeadmConfigSpec" $ctx }}
{{- end }}
{{- end }}

{{/*
Create folders necessary for webhook integration.
*/}}
{{- define "openstack-kamaji-cluster.webhookPatches" }}
{{- $authWebhook := .Values.authWebhook }}
  preKubeadmCommands:
    - mkdir -p /etc/kubernetes/webhooks
    - mkdir -p /etc/kubernetes/patches
{{- if eq $authWebhook "k8s-keystone-auth" }}
    - mkdir -p /etc/kubernetes/keystone-auth
  postKubeadmCommands:
    - cp /etc/kubernetes/manifests/kube-apiserver.yaml /etc/kubernetes/keystone-auth/kube-apiserver.yaml
    - kubectl kustomize /etc/kubernetes/keystone-auth -o /etc/kubernetes/manifests/kube-apiserver.yaml
{{- end }}
{{- end }}

{{/*
Supplement kubeadmConfig with apiServer config and webhook patches as needed. Authentication
webhooks and policies for audit logging can be added here.
*/}}
{{- define "openstack-kamaji-cluster.patchConfigSpec" -}}
{{- $ctx := index . 0 }}
{{- $authWebhook := $ctx.Values.authWebhook }}
  clusterConfiguration:
    apiServer:
      extraArgs:
        v: {{ $ctx.Values.apiServer.logLevel | quote }}
{{- if ne $authWebhook "none" }}
{{- if eq $authWebhook "azimuth-authorization-webhook" }}
        authorization-config: /etc/kubernetes/webhooks/authorization_config.yaml
{{/*
Add else if blocks with other webhooks and apiServer arguments (i.e. audit logging) 
in future
*/}}
{{- end }}
  initConfiguration:
    patches:
      directory: /etc/kubernetes/patches
  joinConfiguration:
    patches:
      directory: /etc/kubernetes/patches
{{- include "openstack-kamaji-cluster.webhookPatches" $ctx }}
{{- if eq $authWebhook "k8s-keystone-auth" }}
{{- include "openstack-kamaji-cluster.k8sKeystoneAuthWebhook" $ctx }}
{{- else if eq $authWebhook "azimuth-authorization-webhook" }}
{{- include "openstack-kamaji-cluster.azimuthAuthorizationWebhook" $ctx }}
{{/*
Add else if blocks with other webhooks or policy files in future.
*/}}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create and mount a directory for webhooks
*/}}
{{- define "openstack-kamaji-cluster.webhookMountDirectoryFile"}}
    - path: /etc/kubernetes/patches/kube-apiserver0+strategic.yaml
      permissions: "0644"
      owner: root:root
      content: |
        spec:
          containers:
          -  name: kube-apiserver
             volumeMounts:
             - mountPath: /etc/kubernetes/webhooks
               name: kube-webhooks
               readOnly: true
          volumes:
          - hostPath:
              path: /etc/kubernetes/webhooks
              type: DirectoryOrCreate
            name: kube-webhooks
{{- end }}

{{/*
Produces integration for k8s-keystone-auth webhook on apiserver
*/}}
{{- define "openstack-kamaji-cluster.k8sKeystoneAuthWebhook" }}
  files:
{{- include "openstack-kamaji-cluster.webhookMountDirectoryFile" . }}
    - path: /etc/kubernetes/keystone-auth/kustomization.yml
      permissions: "0644"
      owner: root:root
      content: |
        resources:
        - kube-apiserver.yaml
        patches:
        - patch: |-
            - op: add
              path: /spec/containers/0/command/-
              value: --authentication-token-webhook-config-file=/etc/kubernetes/webhooks/keystone_webhook_config.yaml
            - op: add
              path: /spec/containers/0/command/-
              value: --authorization-webhook-config-file=/etc/kubernetes/webhooks/keystone_webhook_config.yaml
            - op: add
              path: /spec/containers/0/command/-
              value: --authorization-mode=Webhook
          target:
            kind: Pod
    - path: /etc/kubernetes/webhooks/keystone_webhook_config.yaml
      content: |
        ---
        apiVersion: v1
        kind: Config
        preferences: {}
        clusters:
          - cluster:
              insecure-skip-tls-verify: true
              server: https://127.0.0.1:8443/webhook
            name: webhook
        users:
          - name: webhook
        contexts:
          - context:
              cluster: webhook
              user: webhook
            name: webhook
        current-context: webhook
      owner: root:root
      permissions: "0644"
{{- end }}

{{/*
Produces integration for azimuth_authorization_webhook on apiserver
*/}}
{{- define "openstack-kamaji-cluster.azimuthAuthorizationWebhook" }}
  files:
{{- include "openstack-kamaji-cluster.webhookMountDirectoryFile" . }}
    {{- if $.Values.azimuthAuthorizationWebhook.tls.enabled }}
    - path: /etc/kubernetes/webhooks/ca.pem
      content: {{ $.Values.azimuthAuthorizationWebhook.tls.cert | toYaml | nindent 12 }}
    {{- end }}
    - path: /etc/kubernetes/webhooks/azimuth_authorization_webhook_config.yaml
      content: |
        ---
        apiVersion: v1
        kind: Config
        preferences: {}
        clusters:
          - cluster:
              {{- if $.Values.azimuthAuthorizationWebhook.tls.enabled }}
              certificate-authority: /etc/kubernetes/webhooks/ca.pem
              {{- else }}
              insecure-skip-tls-verify: true
              {{- end }}
              server: {{ $.Values.azimuthAuthorizationWebhook.server }}
            name: webhook
        users:
          - name: webhook
        contexts:
          - context:
              cluster: webhook
              user: webhook
            name: webhook
        current-context: webhook
      owner: root:root
      permissions: "0644"
    - path: /etc/kubernetes/webhooks/authorization_config.yaml
      content: |
        ---
        apiVersion: apiserver.config.k8s.io/v1
        kind: AuthorizationConfiguration
        authorizers:
          - type: Webhook
            name: webhook
            webhook:
              timeout: {{ $.Values.azimuthAuthorizationWebhook.timeout }}
              subjectAccessReviewVersion: {{ $.Values.azimuthAuthorizationWebhook.webhookVersion }}
              matchConditionSubjectAccessReviewVersion: {{ $.Values.azimuthAuthorizationWebhook.webhookVersion }}
              failurePolicy: {{ $.Values.azimuthAuthorizationWebhook.failurePolicy }}
              connectionInfo:
                type: KubeConfigFile
                kubeConfigFile: /etc/kubernetes/webhooks/azimuth_authorization_webhook_config.yaml
              matchConditions:
                {{- $quotedNSList := list }}
                {{- range $.Values.azimuthAuthorizationWebhook.filteredNamespaces }}
                {{- $quotedNSList = append $quotedNSList (quote .) }}
                {{- end }}
                - expression: has(request.resourceAttributes) && (!has(request.resourceAttributes.namespace) || request.resourceAttributes.namespace == "" || request.resourceAttributes.namespace in [{{ join "," $quotedNSList }}])
              {{- range $.Values.azimuthAuthorizationWebhook.extraPreFilters }}
                - expression: {{ . }}
              {{- end }}
          {{- range $.Values.azimuthAuthorizationWebhook.additionalAuthorizers }}
          - type: {{ .type }}
            name: {{ .name }}
          {{- end }}
{{- end }}

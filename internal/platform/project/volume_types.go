package project

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/platform/externalservice"
	"github.com/menlocloud/stratos/pkg/httpx"
)

const maxCreateServerDataVolumes = 32

// configuredVolumeTypes returns the operator-published catalog for a service
// and region. Missing configuration intentionally yields an empty catalog.
func (h *Handler) configuredVolumeTypes(ctx context.Context, serviceID, region string) []externalservice.VolumeTypeConfig {
	if serviceID == "" || region == "" || h.esSvc == nil {
		return []externalservice.VolumeTypeConfig{}
	}
	es, err := h.esSvc.Get(ctx, serviceID)
	if err != nil || es == nil {
		return []externalservice.VolumeTypeConfig{}
	}
	return es.EnabledVolumeTypes(region)
}

// applyVolumeTypeConfig intersects live Cinder types with the strict admin
// catalog. Output follows admin order and adds displayName while retaining the
// real Cinder name/id required by create calls.
func applyVolumeTypeConfig(live []map[string]any, configured []externalservice.VolumeTypeConfig) []map[string]any {
	byName := make(map[string]map[string]any, len(live))
	for _, item := range live {
		name, _ := item["name"].(string)
		if name != "" {
			byName[name] = item
		}
	}
	out := make([]map[string]any, 0, len(configured))
	for _, cfg := range configured {
		item, exists := byName[cfg.Name]
		if !exists {
			continue
		}
		curated := make(map[string]any, len(item)+1)
		for key, value := range item {
			curated[key] = value
		}
		curated["name"] = cfg.Name
		curated["displayName"] = cfg.DisplayName
		out = append(out, curated)
	}
	return out
}

func (h *Handler) liveConfiguredVolumeTypes(
	ctx context.Context,
	cc *client.Client,
	serviceID string,
	region string,
) ([]map[string]any, error) {
	live, err := cc.ListVolumeTypes(ctx)
	if err != nil {
		return nil, err
	}
	return applyVolumeTypeConfig(live, h.configuredVolumeTypes(ctx, serviceID, region)), nil
}

// resolveVolumeType accepts a real Cinder name from a curated list. A sole
// enabled type is selected automatically; multiple types require an explicit
// choice. Display labels are never accepted as provider identifiers.
func resolveVolumeType(curated []map[string]any, requested string) (string, *httpx.HTTPError) {
	requested = strings.TrimSpace(requested)
	if len(curated) == 0 {
		return "", httpx.BadRequest("No block-storage type is enabled in this region")
	}
	if requested == "" {
		if len(curated) == 1 {
			name, _ := curated[0]["name"].(string)
			return name, nil
		}
		return "", httpx.BadRequest("A block-storage type is required")
	}
	for _, item := range curated {
		if name, _ := item["name"].(string); name == requested {
			return name, nil
		}
	}
	return "", httpx.BadRequest(fmt.Sprintf("Block-storage type %q is not enabled in this region", requested))
}

// exactPositiveInt rejects fractional JSON numbers rather than silently
// truncating them. Cinder sizes are whole GiB values.
func exactPositiveInt(value any) (int, bool) {
	var n int64
	switch value := value.(type) {
	case int:
		return value, value > 0
	case int64:
		n = value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value > math.MaxInt64 {
			return 0, false
		}
		n = int64(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		n = parsed
	default:
		return 0, false
	}
	if n <= 0 || uint64(n) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(n), true
}

func integerAny(value any) int {
	n, _ := exactPositiveInt(value)
	return n
}

func byteSize(value any) float64 {
	switch value := value.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		bytes, _ := value.Float64()
		return bytes
	}
	return 0
}

func imageSizeGiB(image map[string]any) int {
	bytes := math.Max(byteSize(image["size"]), math.Max(byteSize(image["virtual_size"]), byteSize(image["virtualSize"])))
	if bytes <= 0 {
		return 0
	}
	return int(math.Ceil(bytes / 1073741824))
}

func maxInts(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

// serverRequestHasNetwork reports whether the create request names at least
// one network (flat networkIds or the wizard's networkInterfaces:[{uuid}]).
// At Nova microversion 2.67 — which every volume-backed create pins — the
// networks field is mandatory, so a network-less request would otherwise
// surface as a raw Nova schema 400.
func serverRequestHasNetwork(data map[string]any) bool {
	if ids, ok := data["networkIds"].([]any); ok {
		for _, id := range ids {
			if value, _ := id.(string); strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	if rows, ok := data["networkInterfaces"].([]any); ok {
		for _, raw := range rows {
			if row, ok := raw.(map[string]any); ok {
				if value, _ := row["uuid"].(string); strings.TrimSpace(value) != "" {
					return true
				}
			}
		}
	}
	return false
}

// prepareServerStorage converts the browser's root/data volume request into a
// server-authoritative, validated block-device specification. It applies only
// to Nova SERVER creates; bare-metal keeps its existing image-backed path.
func (h *Handler) prepareServerStorage(
	ctx context.Context,
	project *Project,
	serviceID string,
	region string,
	data map[string]any,
) *httpx.HTTPError {
	if !serverRequestHasNetwork(data) {
		return httpx.BadRequest("At least one network is required")
	}
	cc, ok := h.tryTenantClient(ctx, project, serviceID)
	if !ok {
		return httpx.NewError(http.StatusServiceUnavailable, http.StatusServiceUnavailable, "Cloud client is not ready")
	}
	curated, err := h.liveConfiguredVolumeTypes(ctx, cc, serviceID, region)
	if err != nil {
		return httpx.NewError(http.StatusServiceUnavailable, http.StatusServiceUnavailable, "Block-storage catalog is unavailable")
	}

	flavorID := strings.TrimSpace(strAny(data["flavorId"]))
	imageID := strings.TrimSpace(strAny(data["imageId"]))
	if flavorID == "" || imageID == "" {
		return httpx.BadRequest("flavorId and imageId are required")
	}
	flavor, err := cc.GetFlavor(ctx, flavorID)
	if err != nil || flavor == nil {
		return httpx.BadRequest("Selected flavor is unavailable")
	}
	image, err := cc.GetImage(ctx, imageID)
	if err != nil || image == nil {
		return httpx.BadRequest("Selected image is unavailable")
	}
	return normalizeServerStorageRequest(data, flavor, image, curated)
}

func normalizeServerStorageRequest(
	data map[string]any,
	flavor map[string]any,
	image map[string]any,
	curated []map[string]any,
) *httpx.HTTPError {
	if flavorHasLocalStorage(flavor) {
		return httpx.BadRequest("Selected flavor includes local ephemeral or swap storage and is not available for volume-backed servers")
	}
	minimumRootSize := maxInts(1, integerAny(image["min_disk"]), integerAny(image["minDisk"]), imageSizeGiB(image))
	defaultRootSize := maxInts(minimumRootSize, integerAny(flavor["disk"]))
	rootValue, rootPresent := data["rootVolume"]
	root, rootIsMap := rootValue.(map[string]any)
	if rootPresent && rootValue != nil && !rootIsMap {
		return httpx.BadRequest("rootVolume must be an object")
	}
	if root == nil {
		root = map[string]any{}
	}
	rootSize := defaultRootSize
	if value, present := root["sizeGiB"]; present {
		var valid bool
		rootSize, valid = exactPositiveInt(value)
		if !valid {
			return httpx.BadRequest("Root volume sizeGiB must be a positive whole number")
		}
	}
	if rootSize < minimumRootSize {
		return httpx.BadRequest(fmt.Sprintf("Root volume must be at least %d GiB for the selected image", minimumRootSize))
	}
	rootType, typeErr := resolveVolumeType(curated, strAny(root["type"]))
	if typeErr != nil {
		return typeErr
	}
	data["rootVolume"] = map[string]any{"sizeGiB": rootSize, "type": rootType}

	rawDataVolumes, present := data["dataVolumes"]
	if !present || rawDataVolumes == nil {
		data["dataVolumes"] = []any{}
		return nil
	}
	rows, ok := rawDataVolumes.([]any)
	if !ok {
		return httpx.BadRequest("dataVolumes must be an array")
	}
	if len(rows) > maxCreateServerDataVolumes {
		return httpx.BadRequest(fmt.Sprintf("A server can include at most %d data volumes", maxCreateServerDataVolumes))
	}
	normalized := make([]any, 0, len(rows))
	for index, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return httpx.BadRequest(fmt.Sprintf("Data volume %d is invalid", index+1))
		}
		size, valid := exactPositiveInt(row["sizeGiB"])
		if !valid {
			return httpx.BadRequest(fmt.Sprintf("Data volume %d sizeGiB must be a positive whole number", index+1))
		}
		volumeType, typeErr := resolveVolumeType(curated, strAny(row["type"]))
		if typeErr != nil {
			return typeErr
		}
		normalized = append(normalized, map[string]any{"sizeGiB": size, "type": volumeType})
	}
	data["dataVolumes"] = normalized
	return nil
}

func flavorHasLocalStorage(flavor map[string]any) bool {
	return integerAny(flavor["ephemeral"]) > 0 || integerAny(flavor["swap"]) > 0
}

func serverIsVolumeBacked(resource *cloud.CloudResource) bool {
	return resource != nil && cloud.ServerIsVolumeBacked(resource.Data)
}

func (h *Handler) prepareStandaloneVolume(
	ctx context.Context,
	project *Project,
	serviceID string,
	region string,
	data map[string]any,
) *httpx.HTTPError {
	size, valid := exactPositiveInt(data["size"])
	if !valid {
		return httpx.BadRequest("Volume size must be a positive whole number")
	}
	data["size"] = size
	// Create-from-snapshot must keep the origin volume's type — Cinder rejects
	// a mismatch. A blank type stays blank so Cinder inherits it; an explicit
	// type is still validated against the curated catalog.
	if strings.TrimSpace(strAny(data["snapshotExternalId"])) != "" && strings.TrimSpace(strAny(data["type"])) == "" {
		data["type"] = ""
		return nil
	}
	volumeType, typeErr := h.resolveConfiguredVolumeType(ctx, project, serviceID, region, strAny(data["type"]))
	if typeErr != nil {
		return typeErr
	}
	data["type"] = volumeType
	return nil
}

func (h *Handler) resolveConfiguredVolumeType(
	ctx context.Context,
	project *Project,
	serviceID string,
	region string,
	requested string,
) (string, *httpx.HTTPError) {
	cc, ok := h.tryTenantClient(ctx, project, serviceID)
	if !ok {
		return "", httpx.NewError(http.StatusServiceUnavailable, http.StatusServiceUnavailable, "Cloud client is not ready")
	}
	curated, err := h.liveConfiguredVolumeTypes(ctx, cc, serviceID, region)
	if err != nil {
		return "", httpx.NewError(http.StatusServiceUnavailable, http.StatusServiceUnavailable, "Block-storage catalog is unavailable")
	}
	return resolveVolumeType(curated, requested)
}

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	computequotas "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
)

// QuotaMetric is the common live OpenStack quota shape exposed to the project API.
// A negative Limit means unlimited. Reserved is kept separate so callers can make
// the same availability calculation as the backing OpenStack service.
type QuotaMetric struct {
	Used     int `json:"used"`
	Reserved int `json:"reserved"`
	Limit    int `json:"limit"`
}

// ComputeQuotaUsage contains the Nova quotas relevant to server creation.
type ComputeQuotaUsage struct {
	Instances QuotaMetric `json:"instances"`
	Cores     QuotaMetric `json:"cores"`
	RAMMB     QuotaMetric `json:"ramMb"`
}

// StorageQuotaUsage contains the Cinder quotas relevant to volume and snapshot
// creation. Values measured in gigabytes use Cinder's GiB-compatible quota unit.
type StorageQuotaUsage struct {
	Volumes            QuotaMetric                            `json:"volumes"`
	Gigabytes          QuotaMetric                            `json:"gigabytes"`
	Snapshots          QuotaMetric                            `json:"snapshots"`
	PerVolumeGigabytes *QuotaMetric                           `json:"perVolumeGigabytes,omitempty"`
	VolumeTypes        map[string]StorageVolumeTypeQuotaUsage `json:"volumeTypes,omitempty"`
}

// StorageVolumeTypeQuotaUsage contains Cinder's optional per-volume-type
// quotas. Each metric is optional because deployments may configure only a
// subset of the volumes_<type>, gigabytes_<type>, and snapshots_<type> keys.
type StorageVolumeTypeQuotaUsage struct {
	Volumes   *QuotaMetric `json:"volumes,omitempty"`
	Gigabytes *QuotaMetric `json:"gigabytes,omitempty"`
	Snapshots *QuotaMetric `json:"snapshots,omitempty"`
}

// Nova added GET /os-quota-sets/{tenant}/detail in microversion 2.50.
const computeQuotaDetailMicroversion = "2.50"

// ComputeQuotaUsage reads Nova's detailed quota set for targetProjectID. The
// project is explicit because an application-credential client is locked to its
// credential project and cannot be re-scoped, while an admin credential can
// still perform this read for another tenant. It deliberately reads live usage
// rather than the provider's stored default-quota configuration, which may
// differ from the tenant's actual limits.
func (c *Client) ComputeQuotaUsage(ctx context.Context, targetProjectID string) (*ComputeQuotaUsage, error) {
	if err := c.requireOpenStackQuotaTarget(targetProjectID); err != nil {
		return nil, err
	}
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	cc.Microversion = computeQuotaDetailMicroversion
	quota, err := computequotas.GetDetail(ctx, cc, targetProjectID).Extract()
	if err != nil {
		return nil, err
	}
	return computeQuotaUsageFromDetail(quota), nil
}

// StorageQuotaUsage reads Cinder's detailed quota set (usage=true) for the
// explicit target project. See ComputeQuotaUsage for why the target is not
// inferred from this client's authentication scope.
func (c *Client) StorageQuotaUsage(ctx context.Context, targetProjectID string) (*StorageQuotaUsage, error) {
	if err := c.requireOpenStackQuotaTarget(targetProjectID); err != nil {
		return nil, err
	}
	bc, err := c.blockStorage()
	if err != nil {
		return nil, err
	}
	var body struct {
		QuotaSet map[string]json.RawMessage `json:"quota_set"`
	}
	url := fmt.Sprintf("%s?usage=true", bc.ServiceURL("os-quota-sets", targetProjectID))
	resp, err := bc.Get(ctx, url, &body, nil)
	_, _, err = gophercloud.ParseResponse(resp, err)
	if err != nil {
		return nil, err
	}
	return storageQuotaUsageFromRaw(body.QuotaSet)
}

// quotaTargetRe bounds the pgdoc-sourced externalProjectId before it is spliced
// into Nova/Cinder quota-set URLs — same barrier pattern as externalservice
// serviceIDRe (keystone project IDs are hex/uuid; no path metacharacters).
var quotaTargetRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func (c *Client) requireOpenStackQuotaTarget(targetProjectID string) error {
	if c == nil {
		return fmt.Errorf("cloud: client is nil")
	}
	if c.IsCephS3() {
		return ErrNotOpenStack
	}
	if targetProjectID == "" {
		return fmt.Errorf("cloud: target project required for quota usage")
	}
	if !quotaTargetRe.MatchString(targetProjectID) {
		return fmt.Errorf("cloud: invalid target project id for quota usage")
	}
	return nil
}

func computeQuotaMetric(q computequotas.QuotaDetail) QuotaMetric {
	return QuotaMetric{Used: q.InUse, Reserved: q.Reserved, Limit: q.Limit}
}

func computeQuotaUsageFromDetail(q computequotas.QuotaDetailSet) *ComputeQuotaUsage {
	return &ComputeQuotaUsage{
		Instances: computeQuotaMetric(q.Instances),
		Cores:     computeQuotaMetric(q.Cores),
		RAMMB:     computeQuotaMetric(q.RAM),
	}
}

type storageQuotaDetail struct {
	InUse    int `json:"in_use"`
	Reserved int `json:"reserved"`
	Limit    int `json:"limit"`
}

func storageQuotaMetricFromRaw(raw map[string]json.RawMessage, key string) (*QuotaMetric, error) {
	value, ok := raw[key]
	if !ok {
		return nil, fmt.Errorf("cloud: Cinder quota response is missing %q", key)
	}
	var detail storageQuotaDetail
	if err := json.Unmarshal(value, &detail); err != nil {
		return nil, fmt.Errorf("cloud: decode Cinder quota %q: %w", key, err)
	}
	return &QuotaMetric{Used: detail.InUse, Reserved: detail.Reserved, Limit: detail.Limit}, nil
}

func storageQuotaUsageFromRaw(raw map[string]json.RawMessage) (*StorageQuotaUsage, error) {
	volumes, err := storageQuotaMetricFromRaw(raw, "volumes")
	if err != nil {
		return nil, err
	}
	gigabytes, err := storageQuotaMetricFromRaw(raw, "gigabytes")
	if err != nil {
		return nil, err
	}
	snapshots, err := storageQuotaMetricFromRaw(raw, "snapshots")
	if err != nil {
		return nil, err
	}

	result := &StorageQuotaUsage{
		Volumes:     *volumes,
		Gigabytes:   *gigabytes,
		Snapshots:   *snapshots,
		VolumeTypes: map[string]StorageVolumeTypeQuotaUsage{},
	}
	if _, ok := raw["per_volume_gigabytes"]; ok {
		result.PerVolumeGigabytes, err = storageQuotaMetricFromRaw(raw, "per_volume_gigabytes")
		if err != nil {
			return nil, err
		}
	}

	for key := range raw {
		var resourceType string
		var prefix string
		switch {
		case strings.HasPrefix(key, "volumes_"):
			resourceType, prefix = "volumes", "volumes_"
		case strings.HasPrefix(key, "gigabytes_"):
			resourceType, prefix = "gigabytes", "gigabytes_"
		case strings.HasPrefix(key, "snapshots_"):
			resourceType, prefix = "snapshots", "snapshots_"
		default:
			continue
		}
		volumeType := strings.TrimPrefix(key, prefix)
		if volumeType == "" {
			continue
		}
		metric, metricErr := storageQuotaMetricFromRaw(raw, key)
		if metricErr != nil {
			return nil, metricErr
		}
		typed := result.VolumeTypes[volumeType]
		switch resourceType {
		case "volumes":
			typed.Volumes = metric
		case "gigabytes":
			typed.Gigabytes = metric
		case "snapshots":
			typed.Snapshots = metric
		}
		result.VolumeTypes[volumeType] = typed
	}
	if len(result.VolumeTypes) == 0 {
		result.VolumeTypes = nil
	}
	return result, nil
}

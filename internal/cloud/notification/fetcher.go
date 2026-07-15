package notification

import (
	"context"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
)

// dataKeyByType is the cloudResource `data` sub-document key each type's live object is stored under
// (the cache convention the FE reads: data.server.name, data.network.name, …). "" = stored bare
// (BUCKET keeps its flat DataBucket; the minimal {id} fallback is also bare).
var dataKeyByType = map[string]string{
	cloud.TypeServer:          "server",
	cloud.TypeBaremetalServer: "server",
	cloud.TypeNetwork:         "network",
	cloud.TypeVolume:          "volume",
	cloud.TypePort:            "port",
	cloud.TypeFloatingIP:      "floatingIp",
	cloud.TypeRouter:          "router",
	cloud.TypeSecurityGroup:   "securityGroup",
	cloud.TypeShare:           "share",
	cloud.TypeBarbicanSecret:  "secret",
	cloud.TypeImage:           "image",
	cloud.TypeDNSZone:         "zone",
}

// FetchByType re-reads one live OpenStack object for the os-notification re-fetch step
// and wraps it in the cloudResource `data` shape the
// cache/FE expect ({network:{…}}, {server:{…}}, …). `cc` must already be scoped to the resource's
// tenant. Returns (data, found, err): a clean 404 → (nil,false,nil) so the notification resolves to a
// DELETE; any other error is propagated so the caller makes NO cache change (avoids a spurious delete
// on a transient failure). A type without a by-id live getter records a minimal {id} object.
func FetchByType(ctx context.Context, cc *client.Client, resourceType, externalID string) (map[string]any, bool, error) {
	var (
		obj map[string]any
		err error
	)
	switch resourceType {
	case cloud.TypeServer, cloud.TypeBaremetalServer:
		obj, err = cc.GetServer(ctx, externalID)
	case cloud.TypeNetwork:
		obj, err = cc.GetNetwork(ctx, externalID)
	case cloud.TypeVolume:
		obj, err = cc.GetVolume(ctx, externalID)
	case cloud.TypePort:
		obj, err = cc.GetPort(ctx, externalID)
	case cloud.TypeFloatingIP:
		obj, err = cc.GetFloatingIP(ctx, externalID)
	case cloud.TypeRouter:
		obj, err = cc.GetRouter(ctx, externalID)
	case cloud.TypeSecurityGroup:
		obj, err = cc.GetSecurityGroup(ctx, externalID)
	case cloud.TypeShare:
		obj, err = cc.GetShare(ctx, externalID)
	case cloud.TypeBarbicanSecret:
		obj, err = cc.GetSecret(ctx, externalID)
	case cloud.TypeImage:
		obj, err = cc.GetImage(ctx, externalID)
	case cloud.TypeDNSZone:
		obj, err = cc.GetZone(ctx, externalID)
	case cloud.TypeBucket:
		// BUCKET stores a flat DataBucket (no wrapping key) — return it as-is.
		if b, berr := cc.GetBucket(ctx, externalID); berr == nil {
			return b, b != nil, nil
		} else if client.IsNotFound(berr) {
			return nil, false, nil
		} else {
			return nil, false, berr
		}
	default:
		// No by-id live getter (e.g. STACK is name+id keyed) — record the resource minimally so the
		// cache still tracks it; the next sync/refresh fills in the full object.
		return map[string]any{"id": externalID}, true, nil
	}
	if err != nil {
		if client.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if obj == nil {
		return nil, false, nil
	}
	if key := dataKeyByType[resourceType]; key != "" {
		data := map[string]any{key: obj}
		if (resourceType == cloud.TypeServer || resourceType == cloud.TypeBaremetalServer) && cloud.ServerIsVolumeBacked(data) {
			data["volumeBacked"] = true
		}
		return data, true, nil
	}
	return obj, true, nil
}

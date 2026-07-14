package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// allocationPools parses the FE's `allocationPools` ([{start,end}]) into client opts (threaded
// into the subnet).
func allocationPools(v any) []client.AllocationPool {
	list, _ := v.([]any)
	out := make([]client.AllocationPool, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, client.AllocationPool{Start: mstr(m, "start"), End: mstr(m, "end")})
	}
	return out
}

// hostRoutes parses the FE's `hostRoutes` ([{destination,nexthop}]) into client opts.
func hostRoutes(v any) []client.HostRoute {
	list, _ := v.([]any)
	out := make([]client.HostRoute, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, client.HostRoute{DestinationCIDR: mstr(m, "destination"), NextHop: mstr(m, "nexthop")})
	}
	return out
}

// write.go = the write dispatch (create/delete by type). It
// parses the controller's free-form `data` into the per-type CloudClient write opts, performs
// the OpenStack write, builds the CloudResource (free-form `data` sub-doc), and upserts the
// cache. The OpenStack calls go through the Writer interface (the *client.Client satisfies it) so
// the dispatch + cache logic is integration-testable with a fake writer.
//
// CONSTRAINT (user): NETWORK create is INTERNAL only (no router:external) — CloudClient offers
// no external-network create, so this dispatch cannot make one.

// Writer is the subset of the CloudClient write surface this dispatch needs (*client.Client
// satisfies it).
type Writer interface {
	CreateNetwork(ctx context.Context, o client.CreateNetworkOpts) (map[string]any, error)
	GetNetwork(ctx context.Context, id string) (map[string]any, error)
	DeleteNetwork(ctx context.Context, id string) error
	CreateSubnet(ctx context.Context, o client.CreateSubnetOpts) (map[string]any, error)
	UpdateSubnet(ctx context.Context, id string, o client.UpdateSubnetOpts) (map[string]any, error)
	DeleteSubnet(ctx context.Context, id string) error
	CreateRouter(ctx context.Context, o client.CreateRouterOpts) (map[string]any, error)
	AddRouterInterface(ctx context.Context, routerID, subnetID string) error
	DeleteRouter(ctx context.Context, id string) error
	CreateServer(ctx context.Context, o client.CreateServerOpts) (map[string]any, error)
	GetServer(ctx context.Context, id string) (map[string]any, error)
	DeleteServer(ctx context.Context, id string) error
	CreateVolume(ctx context.Context, o client.CreateVolumeOpts) (map[string]any, error)
	DeleteVolume(ctx context.Context, id string) error
	CreatePort(ctx context.Context, o client.CreatePortOpts) (map[string]any, error)
	UpdatePort(ctx context.Context, portID string, o client.UpdatePortOpts) (map[string]any, error)
	DeletePort(ctx context.Context, id string) error
	DeleteImage(ctx context.Context, id string) error
	CreateFloatingIP(ctx context.Context, o client.CreateFloatingIPOpts) (map[string]any, error)
	DeleteFloatingIP(ctx context.Context, id string) error
	AttachVolume(ctx context.Context, serverID, volumeID string) (map[string]any, error)
	DetachVolume(ctx context.Context, serverID, attachmentID string) error
	RebootServer(ctx context.Context, id string, hard bool) error
	StartServer(ctx context.Context, id string) error
	StopServer(ctx context.Context, id string) error
	ResizeServer(ctx context.Context, id, flavorID string) error
	ConfirmResize(ctx context.Context, id string) error
	SetServerPassword(ctx context.Context, id, password string) error
	RevertResize(ctx context.Context, id string) error
	RenameServer(ctx context.Context, id, name string) (map[string]any, error)
	CreateServerImage(ctx context.Context, id, name string) (string, error)
	AssociateFloatingIP(ctx context.Context, fipID, portID string) (map[string]any, error)
	DisassociateFloatingIP(ctx context.Context, fipID string) (map[string]any, error)
	CreateSecurityGroup(ctx context.Context, o client.CreateSecurityGroupOpts) (map[string]any, error)
	GetSecurityGroup(ctx context.Context, id string) (map[string]any, error)
	DeleteSecurityGroup(ctx context.Context, id string) error
	CreateSecGroupRule(ctx context.Context, o client.CreateSecGroupRuleOpts) (map[string]any, error)
	DeleteSecGroupRule(ctx context.Context, ruleID string) error
	CreateKeypair(ctx context.Context, o client.CreateKeypairOpts) (map[string]any, error)
	DeleteKeypair(ctx context.Context, name string) error
	CreateVolumeSnapshot(ctx context.Context, o client.CreateVolumeSnapshotOpts) (map[string]any, error)
	DeleteVolumeSnapshot(ctx context.Context, id string) error
	ExtendVolume(ctx context.Context, id string, newSize int) error
	RetypeVolume(ctx context.Context, id, newType, migrationPolicy string) error
	RemoveRouterInterfaceByPort(ctx context.Context, routerID, portID string) error
	SetRouterGateway(ctx context.Context, routerID, networkID string) (map[string]any, error)
	CreateServerGroup(ctx context.Context, o client.CreateServerGroupOpts) (map[string]any, error)
	GetServerGroup(ctx context.Context, id string) (map[string]any, error)
	DeleteServerGroup(ctx context.Context, id string) error
	CreateZone(ctx context.Context, o client.CreateZoneOpts) (map[string]any, error)
	DeleteZone(ctx context.Context, id string) error
	CreateSecret(ctx context.Context, o client.CreateSecretOpts) (map[string]any, error)
	GetSecret(ctx context.Context, id string) (map[string]any, error)
	ListSecrets(ctx context.Context) ([]map[string]any, error)
	DeleteSecret(ctx context.Context, id string) error
	CreateBucket(ctx context.Context, o client.CreateBucketOpts) (map[string]any, error)
	GetBucket(ctx context.Context, name string) (map[string]any, error)
	ListBuckets(ctx context.Context) ([]map[string]any, error)
	DeleteBucket(ctx context.Context, name string) error
	CreateLoadBalancer(ctx context.Context, o client.CreateLoadBalancerOpts) (map[string]any, error)
	GetLoadBalancer(ctx context.Context, id string) (map[string]any, error)
	DeleteLoadBalancer(ctx context.Context, id string) error
	CreateStack(ctx context.Context, o client.CreateStackOpts) (map[string]any, error)
	GetStack(ctx context.Context, name, id string) (map[string]any, error)
	DeleteStack(ctx context.Context, name, id string) error
	CreateShare(ctx context.Context, o client.CreateShareOpts) (map[string]any, error)
	GetShare(ctx context.Context, id string) (map[string]any, error)
	DeleteShare(ctx context.Context, id string) error
	// Manila share-network + security-service, VPNaaS, IPSec/IKE, Magnum, Ironic, Barbican container,
	// keystone user — full-port create/delete (some live-blocked until a region exposes the backend).
	CreateShareNetwork(ctx context.Context, o client.CreateShareNetworkOpts) (map[string]any, error)
	DeleteShareNetwork(ctx context.Context, id string) error
	CreateShareSecurityService(ctx context.Context, o client.CreateShareSecurityServiceOpts) (map[string]any, error)
	DeleteShareSecurityService(ctx context.Context, id string) error
	CreateVPNService(ctx context.Context, o client.CreateVPNServiceOpts) (map[string]any, error)
	DeleteVPNService(ctx context.Context, id string) error
	CreateVPNEndpointGroup(ctx context.Context, o client.CreateVPNEndpointGroupOpts) (map[string]any, error)
	DeleteVPNEndpointGroup(ctx context.Context, id string) error
	CreateIKEPolicy(ctx context.Context, o client.CreateIKEPolicyOpts) (map[string]any, error)
	DeleteIKEPolicy(ctx context.Context, id string) error
	CreateIPSecPolicy(ctx context.Context, o client.CreateIPSecPolicyOpts) (map[string]any, error)
	DeleteIPSecPolicy(ctx context.Context, id string) error
	CreateIPSecSiteConnection(ctx context.Context, o client.CreateIPSecSiteConnectionOpts) (map[string]any, error)
	DeleteIPSecSiteConnection(ctx context.Context, id string) error
	CreateCluster(ctx context.Context, o client.CreateClusterOpts) (map[string]any, error)
	DeleteCluster(ctx context.Context, id string) error
	CreateBareMetalNode(ctx context.Context, o client.CreateBareMetalNodeOpts) (map[string]any, error)
	DeleteBareMetalNode(ctx context.Context, id string) error
	CreateContainer(ctx context.Context, o client.CreateContainerOpts) (map[string]any, error)
	DeleteContainer(ctx context.Context, id string) error
	CreateUser(ctx context.Context, o client.CreateUserOpts) (*client.CreatedUser, error)
	DeleteUser(ctx context.Context, id string) error
	// Trilio (TrilioVault backup) — code-only / live-blocked (the "workloads" service is absent on the
	// current regions); each call resolves the endpoint + errors cleanly when Trilio isn't present.
	CreateWorkload(ctx context.Context, payload map[string]any) (map[string]any, error)
	DeleteWorkload(ctx context.Context, id string) error
	CreateSnapshot(ctx context.Context, workloadID string, full bool, payload map[string]any) (map[string]any, error)
	DeleteSnapshot(ctx context.Context, id string) error
	CreateBackupTarget(ctx context.Context, payload map[string]any) (map[string]any, error)
	DeleteBackupTarget(ctx context.Context, id string) error
	CreateRestore(ctx context.Context, snapshotID string, payload map[string]any) (map[string]any, error)
	DeleteRestore(ctx context.Context, id string) error
}

// WriteService dispatches create/delete to the cloud + the CloudResource cache.

// zoneName reads a cached DNS_ZONE's fqdn (data.name, else data.zone.name — the create/notification
// shapes both carry it).
func zoneName(cr *cloud.CloudResource) string {
	if n, _ := cr.Data["name"].(string); n != "" {
		return n
	}
	if z, _ := cr.Data["zone"].(map[string]any); z != nil {
		n, _ := z["name"].(string)
		return n
	}
	return ""
}

type WriteService struct {
	w    Writer
	repo *cloud.Repo
	// projectID is the authorized project of the current request (set at Create/Action). It scopes
	// resolveExtID so a secondary body id can only resolve against THIS project's cache rows (§6).
	projectID string
}

func NewWriteService(w Writer, repo *cloud.Repo) *WriteService {
	return &WriteService{w: w, repo: repo}
}

// defaultNetworkMTU is the deployment-wide MTU stamped on client-created networks (0 = leave unset,
// so neutron uses the provider default). Set once at startup from config via SetDefaultNetworkMTU.
var defaultNetworkMTU int

// SetDefaultNetworkMTU sets the process-wide default network MTU (from STRATOS_DEFAULT_NETWORK_MTU).
func SetDefaultNetworkMTU(m int) { defaultNetworkMTU = m }

// CreateRequest is the controller body: a type + free-form data.
type CreateRequest struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Create dispatches a create by type, writes OpenStack, and upserts the CloudResource cache. userID
// scopes identity resources (KEYPAIR) — CloudResource.userId (not projectId) is set for those.
func (s *WriteService) Create(ctx context.Context, serviceID, region, projectID, userID string, req CreateRequest) (*cloud.CloudResource, error) {
	s.projectID = projectID // scope resolveExtID to the authorized project (§6)
	now := time.Now().UTC()
	cr := &cloud.CloudResource{
		ServiceID: serviceID, Region: region, ProjectID: projectID, Type: req.Type,
		CreatedAt: &now, UpdatedAt: &now,
	}
	d := req.Data

	switch req.Type {
	case cloud.TypeNetwork:
		// MTU: an explicit per-request mtu wins, else the deployment default (0 = leave unset →
		// neutron's provider default, e.g. the geneve/vxlan value).
		netMTU := mint(d, "mtu")
		if netMTU == 0 {
			netMTU = defaultNetworkMTU
		}
		net, err := s.w.CreateNetwork(ctx, client.CreateNetworkOpts{
			Name: mstr(d, "name"), AvailabilityZoneHints: mstrs(d, "availabilityZones"),
			MTU: netMTU,
		})
		if err != nil {
			return nil, err
		}
		netID := mstr(net, "id")
		if netID == "" {
			return nil, fmt.Errorf("create network: no id")
		}
		if mbool(d, "defaultSubnet") {
			dhcp := mbool(d, "enableDhcp")
			if _, err := s.w.CreateSubnet(ctx, client.CreateSubnetOpts{
				NetworkID: netID, Name: mstr(d, "name") + "-subnet",
				CIDR: mstr(d, "cidr"), IPVersion: 4, EnableDHCP: &dhcp,
				Gateway: mbool(d, "gateway"), CustomGatewayIP: mbool(d, "customGatewayIp"),
				GatewayIP: mstr(d, "gatewayIp"), DNSNameservers: mstrs(d, "dnsNameServers"),
				AllocationPools: allocationPools(d["allocationPools"]), HostRoutes: hostRoutes(d["hostRoutes"]),
			}); err != nil {
				return nil, err
			}
			if fresh, err := s.w.GetNetwork(ctx, netID); err == nil {
				net = fresh
			}
		}
		cr.ExternalID = netID
		cr.Data = map[string]any{"network": net, "networkName": mstr(d, "name")}

	case cloud.TypeSubnet:
		// Add a subnet to an existing network. data{networkId, cidr, name?, enableDhcp?, gateway?,
		// customGatewayIp?/gatewayIp?, dnsNameServers?, allocationPools?, hostRoutes?}.
		if mstr(d, "networkId") == "" {
			return nil, fmt.Errorf("networkId is required")
		}
		dhcp := mbool(d, "enableDhcp")
		sub, err := s.w.CreateSubnet(ctx, client.CreateSubnetOpts{
			NetworkID: mstr(d, "networkId"), Name: mstr(d, "name"),
			CIDR: mstr(d, "cidr"), IPVersion: 4, EnableDHCP: &dhcp,
			Gateway: mbool(d, "gateway"), CustomGatewayIP: mbool(d, "customGatewayIp"),
			GatewayIP: mstr(d, "gatewayIp"), DNSNameservers: mstrs(d, "dnsNameServers"),
			AllocationPools: allocationPools(d["allocationPools"]), HostRoutes: hostRoutes(d["hostRoutes"]),
		})
		if err != nil {
			return nil, err
		}
		sid := mstr(sub, "id")
		if sid == "" {
			return nil, fmt.Errorf("create subnet: no id")
		}
		cr.ExternalID = sid
		cr.Data = map[string]any{"subnet": sub}

	case cloud.TypeRouter:
		rt, err := s.w.CreateRouter(ctx, client.CreateRouterOpts{
			Name:                     mstr(d, "name"),
			ExternalGatewayNetworkID: mstr(d, "externalNetworkId"),
		})
		if err != nil {
			return nil, err
		}
		rid := mstr(rt, "id")
		if rid == "" {
			return nil, fmt.Errorf("create router: no id")
		}
		if subnetID := mstr(d, "subnetId"); subnetID != "" {
			if err := s.w.AddRouterInterface(ctx, rid, subnetID); err != nil {
				return nil, err
			}
		}
		cr.ExternalID = rid
		cr.Data = map[string]any{"router": rt, "routerName": mstr(d, "name")}

	case cloud.TypeServer, cloud.TypeBaremetalServer:
		// The create-server wizard sends networkInterfaces:[{uuid,port}],
		// securityGroupNames and availabilityZoneName — accept those (falling back to the flat
		// networkIds/securityGroups/availabilityZone aliases the direct API used).
		netIDs := mstrs(d, "networkIds")
		if len(netIDs) == 0 {
			netIDs = ifaceUUIDs(d["networkInterfaces"])
		}
		secGroups := mstrs(d, "securityGroupNames")
		if len(secGroups) == 0 {
			secGroups = mstrs(d, "securityGroups")
		}
		az := mstr(d, "availabilityZoneName")
		if az == "" {
			az = mstr(d, "availabilityZone")
		}
		// Login + cloud-init: keyName OR adminPass (password login), plus optional raw user-data
		// (cloud-init) the client can send for custom provisioning.
		var userData []byte
		if ud := mstr(d, "userData"); ud != "" {
			userData = []byte(ud)
		}
		srv, err := s.w.CreateServer(ctx, client.CreateServerOpts{
			Name: mstr(d, "name"), FlavorID: mstr(d, "flavorId"), ImageID: mstr(d, "imageId"),
			NetworkIDs: netIDs, FixedIPs: ifaceFixedIPs(d["networkInterfaces"]), KeyName: mstr(d, "keyName"),
			SecurityGroups: secGroups, AvailabilityZone: az,
			AdminPass: mstr(d, "adminPass"), UserData: userData,
		})
		if err != nil {
			return nil, err
		}
		srvID := mstr(srv, "id")
		if srvID == "" {
			return nil, fmt.Errorf("create server: no id")
		}
		// Nova's create response is minimal (id + adminPass only — no name/status/flavor), which would
		// cache a blank, grey row until the next sync. Re-read the full server so the list shows the
		// name + BUILD status + flavor immediately.
		if full, gerr := s.w.GetServer(ctx, srvID); gerr == nil && mstr(full, "id") != "" {
			srv = full
		}
		// Keep the just-created cache entry quota/rating-ready. Nova's server
		// response generally carries only flavor.id; resolving it here prevents
		// GPU usage from reading as zero until the next background sync.
		requestedFlavorID := mstr(d, "flavorId")
		flavor, ok := srv["flavor"].(map[string]any)
		if !ok || flavor == nil {
			flavor = map[string]any{}
			srv["flavor"] = flavor
		}
		if mstr(flavor, "id") == "" {
			flavor["id"] = requestedFlavorID
		}
		enrichServerFlavorForCache(ctx, s.w, srv)
		cr.ExternalID = srvID
		cr.Data = map[string]any{"server": srv}

	case cloud.TypeVolume:
		vol, err := s.w.CreateVolume(ctx, client.CreateVolumeOpts{
			Name: mstr(d, "name"), Size: mint(d, "size"), VolumeType: mstr(d, "type"),
			ImageID: mstr(d, "imageId"), SnapshotID: mstr(d, "snapshotExternalId"),
			AvailabilityZone: mstr(d, "availabilityZone"),
		})
		if err != nil {
			return nil, err
		}
		volID := mstr(vol, "id")
		if volID == "" {
			return nil, fmt.Errorf("create volume: no id")
		}
		cr.ExternalID = volID
		cr.Data = map[string]any{"volume": vol, "attachments": []any{}}

	case cloud.TypePort:
		port, err := s.w.CreatePort(ctx, client.CreatePortOpts{
			NetworkID: mstr(d, "networkId"), Name: mstr(d, "name"),
			MACAddress: mstr(d, "macAddress"), FixedIP: mstr(d, "fixedIp"), SubnetID: mstr(d, "subnetId"),
			PortSecurityEnabled: mboolPtr(d, "portSecurityEnabled"),
			AllowedAddressPairs: addressPairs(d["allowedAddressPairs"]),
		})
		if err != nil {
			return nil, err
		}
		pid := mstr(port, "id")
		if pid == "" {
			return nil, fmt.Errorf("create port: no id")
		}
		cr.ExternalID = pid
		cr.Data = map[string]any{"port": port}

	case cloud.TypeFloatingIP:
		fnet := mstr(d, "floatingNetworkId")
		if fnet == "" {
			fnet = mstr(d, "networkId")
		}
		if fnet == "" {
			// networkId is required (NETWORK_POOL_IS_NOT_SET) → 400.
			return nil, httpx.BadRequest("Network Pool is not set")
		}
		// Read externalPortId (the FE's create-with-port association); fall back to
		// the legacy `portId` key. Previously only `portId` was read → a create-with-port was dropped.
		portID := mstr(d, "externalPortId")
		if portID == "" {
			portID = mstr(d, "portId")
		}
		fip, err := s.w.CreateFloatingIP(ctx, client.CreateFloatingIPOpts{
			FloatingNetworkID: fnet, PortID: portID, Description: mstr(d, "description"),
		})
		if err != nil {
			return nil, err
		}
		fid := mstr(fip, "id")
		if fid == "" {
			return nil, fmt.Errorf("create floating ip: no id")
		}
		cr.ExternalID = fid
		cr.Data = map[string]any{"floatingIp": fip}

	case cloud.TypeSecurityGroup:
		sg, err := s.w.CreateSecurityGroup(ctx, client.CreateSecurityGroupOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"),
		})
		if err != nil {
			return nil, err
		}
		sgID := mstr(sg, "id")
		if sgID == "" {
			return nil, fmt.Errorf("create security group: no id")
		}
		cr.ExternalID = sgID
		cr.Data = map[string]any{"securityGroup": sg}

	case cloud.TypeKeypair:
		// Identity resource: scoped to the USER, externalId =
		// "<name>_<userId>", privateKey (when nova generates one) surfaced as ephemeralData (transient).
		kp, err := s.w.CreateKeypair(ctx, client.CreateKeypairOpts{
			Name: mstr(d, "name"), PublicKey: mstr(d, "publicKey"),
		})
		if err != nil {
			return nil, err
		}
		name := mstr(kp, "name")
		if name == "" {
			name = mstr(d, "name")
		}
		cr.ProjectID = ""
		cr.UserID = userID
		cr.ExternalID = name + "_" + userID
		if pk, _ := kp["private_key"].(string); pk != "" {
			cr.EphemeralData = map[string]any{"privateKey": pk}
			delete(kp, "private_key")
		}
		cr.Data = map[string]any{"keypair": kp}

	case cloud.TypeVolumeSnapshot:
		volID := mstr(d, "externalVolumeId")
		if volID == "" {
			return nil, fmt.Errorf("externalVolumeId is required")
		}
		snap, err := s.w.CreateVolumeSnapshot(ctx, client.CreateVolumeSnapshotOpts{
			VolumeID: volID, Name: mstr(d, "name"), Description: mstr(d, "description"), Force: mbool(d, "force"),
		})
		if err != nil {
			return nil, err
		}
		snapID := mstr(snap, "id")
		if snapID == "" {
			return nil, fmt.Errorf("create volume snapshot: no id")
		}
		cr.ExternalID = snapID
		cr.Data = map[string]any{"volumeSnapshot": snap}

	case cloud.TypeServerGroup:
		// Nova server group. FE: data{name, policy} (policy ∈
		// affinity/anti-affinity/soft-affinity/soft-anti-affinity).
		sg, err := s.w.CreateServerGroup(ctx, client.CreateServerGroupOpts{
			Name: mstr(d, "name"), Policy: mstr(d, "policy"),
		})
		if err != nil {
			return nil, err
		}
		sgID := mstr(sg, "id")
		if sgID == "" {
			return nil, fmt.Errorf("create server group: no id")
		}
		cr.ExternalID = sgID
		cr.Data = map[string]any{"serverGroup": sg}

	case cloud.TypeDNSZone:
		// Designate zone. FE: data{domain, email, ttl, description}.
		// The request's `domain` is FQDN-normalized (trim + single trailing dot) → the zone name.
		name := fqdn(mstr(d, "domain"))
		if name == "" {
			return nil, fmt.Errorf("domain is required")
		}
		// Duplicate-domain guard: the CACHE is checked first (list by zone name,
		// serviceId-scoped) and a match is a friendly 400 (was a raw 409).
		if cached, err := s.repo.FindByServiceAndType(ctx, serviceID, cloud.TypeDNSZone); err == nil {
			for i := range cached {
				if zoneName(&cached[i]) == name {
					return nil, httpx.BadRequest("This domain already exists on our servers.")
				}
			}
		}
		zone, err := s.w.CreateZone(ctx, client.CreateZoneOpts{
			Name: name, Email: mstr(d, "email"), TTL: mint(d, "ttl"), Description: mstr(d, "description"),
		})
		if err != nil {
			return nil, err
		}
		zid := mstr(zone, "id")
		if zid == "" {
			return nil, fmt.Errorf("create dns zone: no id")
		}
		cr.ExternalID = zid
		cr.Data = map[string]any{"zone": zone, "name": name}

	case cloud.TypeBarbicanSecret:
		// Barbican secret. FE: data{name, secretType,
		// algorithm, bitLength, mode, expiration, payloadContentType, payloadContentEncoding, payload}.
		// externalId = the UUID tail of the secret_ref; data.secret = the re-fetched secret.
		secret, err := s.w.CreateSecret(ctx, client.CreateSecretOpts{
			Name: mstr(d, "name"), SecretType: mstr(d, "secretType"), Algorithm: mstr(d, "algorithm"),
			BitLength: mint(d, "bitLength"), Mode: mstr(d, "mode"), Expiration: mstr(d, "expiration"),
			PayloadContentType: mstr(d, "payloadContentType"), PayloadContentEncoding: mstr(d, "payloadContentEncoding"),
			Payload: mstr(d, "payload"),
		})
		if err != nil {
			return nil, err
		}
		sid := mstr(secret, "id")
		if sid == "" {
			return nil, fmt.Errorf("create barbican secret: no id")
		}
		cr.ExternalID = sid
		cr.Data = map[string]any{"secret": secret}

	case cloud.TypeBucket:
		// Bucket (Swift container or ceph-s3 bucket). FE: data{bucketName, objectLockEnabled?}.
		// externalId = the bucket name; data = DataBucket{bucketName, objectCount:0, sizeInGb:0,
		// sizeInBytes:0, storageBackend}. objectLockEnabled is CREATE-TIME ONLY (S3 forbids enabling it
		// later) and is rejected on Swift.
		name := mstr(d, "bucketName")
		if name == "" {
			return nil, fmt.Errorf("bucket name is required")
		}
		bucket, err := s.w.CreateBucket(ctx, client.CreateBucketOpts{
			Name: name, ObjectLockEnabled: mbool(d, "objectLockEnabled"),
		})
		if err != nil {
			return nil, err
		}
		cr.ExternalID = name
		cr.Data = bucket

	case cloud.TypeLoadBalancer:
		// Octavia load balancer. FE:
		// data{name, networkExternalId, availabilityZone}. networkExternalId → vip_network_id; the FE
		// may send a NETWORK cache id → resolve to externalId. externalId = the LB id (PENDING_CREATE).
		lb, err := s.w.CreateLoadBalancer(ctx, client.CreateLoadBalancerOpts{
			Name:             mstr(d, "name"),
			NetworkID:        s.resolveExtID(ctx, mstr(d, "networkExternalId")),
			AvailabilityZone: mstr(d, "availabilityZone"),
		})
		if err != nil {
			return nil, err
		}
		lbID := mstr(lb, "id")
		if lbID == "" {
			return nil, fmt.Errorf("create load balancer: no id")
		}
		cr.ExternalID = lbID
		cr.Data = map[string]any{"loadBalancer": lb}

	case cloud.TypeStack:
		// Heat stack. FE: data{name, template, environment,
		// disableRollback}. externalId = the stack id; data.stack carries the re-fetched stack (its
		// `stack_name` is needed to delete — Heat is name+id keyed).
		stack, err := s.w.CreateStack(ctx, client.CreateStackOpts{
			Name: mstr(d, "name"), Template: mstr(d, "template"),
			Environment: mstr(d, "environment"), DisableRollback: mbool(d, "disableRollback"),
		})
		if err != nil {
			return nil, err
		}
		stID := mstr(stack, "id")
		if stID == "" {
			return nil, fmt.Errorf("create stack: no id")
		}
		cr.ExternalID = stID
		cr.Data = map[string]any{"stack": stack}

	case cloud.TypeShare:
		// Manila share. FE: data{name, description, protocol, size,
		// shareType, shareNetworkId, availabilityZone}. externalId = the share id.
		share, err := s.w.CreateShare(ctx, client.CreateShareOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"), Protocol: mstr(d, "protocol"),
			Size: mint(d, "size"), ShareType: mstr(d, "shareType"),
			ShareNetworkID: s.resolveExtID(ctx, mstr(d, "shareNetworkId")), ShareGroupID: mstr(d, "shareGroupId"),
			AvailabilityZone: mstr(d, "availabilityZone"),
		})
		if err != nil {
			return nil, err
		}
		shID := mstr(share, "id")
		if shID == "" {
			return nil, fmt.Errorf("create share: no id")
		}
		cr.ExternalID = shID
		cr.Data = map[string]any{"share": share}

	case cloud.TypeImage:
		// Server snapshot: glance image from a running server.
		// The FE sends data{name, serverId (the cloudResource CACHE id), imageType:"snapshot"}.
		serverID := mstr(d, "serverId")
		if sc, _ := s.repo.FindByID(ctx, serverID); sc != nil && sc.ExternalID != "" {
			serverID = sc.ExternalID
		}
		imgID, err := s.w.CreateServerImage(ctx, serverID, mstr(d, "name"))
		if err != nil {
			return nil, err
		}
		if imgID == "" {
			return nil, fmt.Errorf("create snapshot: no image id")
		}
		cr.ExternalID = imgID
		cr.Data = map[string]any{"image": map[string]any{
			"id": imgID, "name": mstr(d, "name"), "image_type": "snapshot",
			"imageType": "snapshot", "instance_uuid": serverID, "instanceUuid": serverID,
		}}

	case cloud.TypeShareNetwork:
		obj, err := s.w.CreateShareNetwork(ctx, client.CreateShareNetworkOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"),
			ExternalNetworkID: s.resolveExtID(ctx, mstr(d, "networkId")), ExternalSubnetID: s.resolveExtID(ctx, mstr(d, "subnetId")),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create share network: no id")
		}
		cr.Data = map[string]any{"shareNetwork": obj}

	case cloud.TypeShareSecurityService:
		obj, err := s.w.CreateShareSecurityService(ctx, client.CreateShareSecurityServiceOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"), Type: mstr(d, "type"),
			DNSIP: mstr(d, "dnsIp"), User: mstr(d, "user"), Password: mstr(d, "password"),
			Domain: mstr(d, "domain"), Server: mstr(d, "server"),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create share security service: no id")
		}
		cr.Data = map[string]any{"shareSecurityService": obj}

	case cloud.TypeVPNService:
		obj, err := s.w.CreateVPNService(ctx, client.CreateVPNServiceOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"), AdminStateUp: true,
			RouterID: s.resolveExtID(ctx, mstr(d, "routerId")), SubnetID: s.resolveExtID(ctx, mstr(d, "subnetId")), FlavorID: mstr(d, "flavorId"),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create vpn service: no id")
		}
		cr.Data = map[string]any{"vpnService": obj}

	case cloud.TypeVPNEndpointGroup:
		obj, err := s.w.CreateVPNEndpointGroup(ctx, client.CreateVPNEndpointGroupOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"), Type: mstr(d, "type"), Endpoints: strSlice(d["endpoints"]),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create vpn endpoint group: no id")
		}
		cr.Data = map[string]any{"vpnEndpointGroup": obj}

	case cloud.TypeIKEPolicy:
		obj, err := s.w.CreateIKEPolicy(ctx, client.CreateIKEPolicyOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"), AuthAlgorithm: mstr(d, "authAlgorithm"),
			EncryptionAlgorithm: mstr(d, "encryptionAlgorithm"), PFS: mstr(d, "pfs"),
			Phase1NegotiationMode: mstr(d, "phase1NegotiationMode"), IKEVersion: mstr(d, "ikeVersion"),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create ike policy: no id")
		}
		cr.Data = map[string]any{"ikePolicy": obj}

	case cloud.TypeIPSecPolicy:
		obj, err := s.w.CreateIPSecPolicy(ctx, client.CreateIPSecPolicyOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"), EncryptionAlgorithm: mstr(d, "encryptionAlgorithm"),
			PFS: mstr(d, "pfs"), TransformProtocol: mstr(d, "transformProtocol"), EncapsulationMode: mstr(d, "encapsulationMode"),
			AuthAlgorithm: mstr(d, "authAlgorithm"),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create ipsec policy: no id")
		}
		cr.Data = map[string]any{"ipsecPolicy": obj}

	case cloud.TypeIPSecSiteConnection:
		obj, err := s.w.CreateIPSecSiteConnection(ctx, client.CreateIPSecSiteConnectionOpts{
			Name: mstr(d, "name"), Description: mstr(d, "description"), PeerAddress: mstr(d, "peerAddress"),
			PeerID: mstr(d, "peerId"), PSK: mstr(d, "psk"), Initiator: mstr(d, "initiator"),
			VPNServiceID: s.resolveExtID(ctx, mstr(d, "vpnServiceId")), IKEPolicyID: s.resolveExtID(ctx, mstr(d, "ikePolicyId")),
			IPSecPolicyID: s.resolveExtID(ctx, mstr(d, "ipsecPolicyId")), LocalEndpointGroup: s.resolveExtID(ctx, mstr(d, "localEndpointGroupId")),
			PeerEndpointGroup: s.resolveExtID(ctx, mstr(d, "peerEndpointGroupId")), MTU: mint(d, "mtu"),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create ipsec site connection: no id")
		}
		cr.Data = map[string]any{"ipsecSiteConnection": obj}

	case cloud.TypeKubernetesCluster:
		obj, err := s.w.CreateCluster(ctx, client.CreateClusterOpts{
			Name: mstr(d, "name"), KeyName: mstr(d, "keyName"), ClusterTemplateID: mstr(d, "clusterTemplateId"),
			WorkerFlavorID: mstr(d, "workerFlavorId"), MasterFlavorID: mstr(d, "masterFlavorId"), NodeCount: mint(d, "nodeCount"),
			FixedNetwork: s.resolveExtID(ctx, mstr(d, "fixedNetwork")), HA: mbool(d, "ha"), FloatingIP: mbool(d, "floatingIp"),
			Autoscaling: mbool(d, "autoscaling"), MinNodeCount: mstr(d, "minNodeCount"), MaxNodeCount: mstr(d, "maxNodeCount"),
			ContainerVolumesStorageType: mstr(d, "containerVolumesStorageType"), ContainerVolumesStorageSize: mstr(d, "containerVolumesStorageSize"),
			Labels: mstr(d, "labels"),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create cluster: no id")
		}
		cr.Data = map[string]any{"cluster": obj}

	case cloud.TypeBarbicanContainer:
		obj, err := s.w.CreateContainer(ctx, client.CreateContainerOpts{
			Name: mstr(d, "name"), Type: mstr(d, "type"), SecretRefs: containerSecretRefs(d["secretRefs"]),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(obj, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create barbican container: no id")
		}
		cr.Data = map[string]any{"container": obj}

	case cloud.TypeUser:
		cu, err := s.w.CreateUser(ctx, client.CreateUserOpts{
			Username: mstr(d, "name"), Description: mstr(d, "description"), Email: mstr(d, "email"),
		})
		if err != nil {
			return nil, err
		}
		if cr.ExternalID = mstr(cu.User, "id"); cr.ExternalID == "" {
			return nil, fmt.Errorf("create user: no id")
		}
		cr.UserID = userID
		cr.Data = map[string]any{"user": cu.User}
		if cu.Password != "" {
			cr.EphemeralData = map[string]any{"password": cu.Password, "username": cu.Username}
		}

	case cloud.TypeTrilioWorkload:
		obj, err := s.w.CreateWorkload(ctx, d)
		if err != nil {
			return nil, err
		}
		cr.ExternalID = mstr(obj, "id")
		cr.Data = map[string]any{"workload": obj}

	case cloud.TypeTrilioSnapshot:
		obj, err := s.w.CreateSnapshot(ctx, mstr(d, "workloadId"), mbool(d, "full"), d)
		if err != nil {
			return nil, err
		}
		cr.ExternalID = mstr(obj, "id")
		cr.Data = map[string]any{"snapshot": obj}

	case cloud.TypeTrilioBackupTarget:
		obj, err := s.w.CreateBackupTarget(ctx, d)
		if err != nil {
			return nil, err
		}
		cr.ExternalID = mstr(obj, "id")
		cr.Data = map[string]any{"backupTarget": obj}

	case cloud.TypeTrilioRestore:
		obj, err := s.w.CreateRestore(ctx, mstr(d, "snapshotId"), d)
		if err != nil {
			return nil, err
		}
		cr.ExternalID = mstr(obj, "id")
		cr.Data = map[string]any{"restore": obj}

	default:
		return nil, fmt.Errorf("unsupported create type %q", req.Type)
	}

	saved, err := s.repo.Insert(ctx, cr)
	if err != nil {
		return nil, err
	}
	// Insert re-decodes the persisted doc (EphemeralData is dropped by the record builder) — re-attach the
	// transient data so the create response can carry it (KEYPAIR privateKey), unpersisted.
	saved.EphemeralData = cr.EphemeralData
	return saved, nil
}

type flavorReader interface {
	GetFlavor(context.Context, string) (map[string]any, error)
}

// enrichServerFlavorForCache is optional for fake writers and non-OpenStack
// implementations, but *client.Client supplies it. It mirrors the read-side
// enrichment without coupling the provider package back to the project layer.
func enrichServerFlavorForCache(ctx context.Context, writer any, srv map[string]any) {
	flavor, ok := srv["flavor"].(map[string]any)
	if !ok || flavor == nil {
		return
	}
	if _, resolved := flavor["extra_specs"]; resolved {
		return
	}
	flavorID, _ := flavor["id"].(string)
	reader, ok := writer.(flavorReader)
	if !ok || flavorID == "" {
		return
	}
	specs, err := reader.GetFlavor(ctx, flavorID)
	if err != nil {
		return
	}
	for key, value := range specs {
		flavor[key] = value
	}
}

// Action handles POST /cloud/{rid}/action: dispatches by the
// cached resource's type + the action name. Returns the (possibly updated) cached resource.
// {externalID} is the resource's externalId.
func (s *WriteService) Action(ctx context.Context, serviceID, projectID, externalID, action string, data map[string]any) (*cloud.CloudResource, error) {
	s.projectID = projectID // scope resolveExtID to the authorized project (§6)
	cr, err := s.repo.FindByServiceIDAndExternalID(ctx, serviceID, externalID)
	if err != nil {
		return nil, err
	}
	if cr == nil {
		return nil, fmt.Errorf("resource %q not found", externalID)
	}
	switch cr.Type {
	case cloud.TypeVolume:
		serverID := mstr(data, "serverId")
		switch action {
		case "ATTACH":
			att, err := s.w.AttachVolume(ctx, serverID, externalID)
			if err != nil {
				return nil, err
			}
			cr.Data = withAttachment(cr.Data, att, serverID)
			return s.repo.Insert(ctx, cr)
		case "DETACH":
			if err := s.w.DetachVolume(ctx, serverID, externalID); err != nil {
				return nil, err
			}
			cr.Data = withoutAttachment(cr.Data, serverID)
			return s.repo.Insert(ctx, cr)
		case "EXTEND":
			// EXTEND: data{size} → cinder os-extend.
			return cr, s.w.ExtendVolume(ctx, externalID, mint(data, "size"))
		case "RETYPE":
			// RETYPE: data{newType, migrationPolicy} → cinder os-retype. Blank type
			// is a friendly 400, not a raw cinder error.
			if mstr(data, "newType") == "" {
				return cr, httpx.BadRequest("New volume type must be specified")
			}
			return cr, s.w.RetypeVolume(ctx, externalID, mstr(data, "newType"), mstr(data, "migrationPolicy"))
		}
	case cloud.TypeServer, cloud.TypeBaremetalServer:
		// INVOKEACTION is the FE's generic power dispatch (start/stop) carrying the real verb in
		// data.action; reboot is sent as the dedicated SOFTREBOOT/HARDREBOOT action.
		if action == "INVOKEACTION" {
			action = strings.ToUpper(mstr(data, "action"))
		}
		switch action {
		case "REBOOT_SOFT", "SOFTREBOOT", "REBOOT":
			return cr, s.w.RebootServer(ctx, externalID, false)
		case "REBOOT_HARD", "HARDREBOOT":
			return cr, s.w.RebootServer(ctx, externalID, true)
		case "START":
			return cr, s.w.StartServer(ctx, externalID)
		case "STOP", "SHUTOFF", "SHUTDOWN":
			return cr, s.w.StopServer(ctx, externalID)
		case "RESIZE":
			return cr, s.w.ResizeServer(ctx, externalID, mstr(data, "flavorId"))
		case "CONFIRMRESIZE":
			return cr, s.w.ConfirmResize(ctx, externalID)
		case "SET_PASSWORD":
			// Reset the instance's admin/root password (nova changeAdminPassword). Needs the image's
			// guest agent / cloud-init password support to take effect.
			return cr, s.w.SetServerPassword(ctx, externalID, mstr(data, "password"))
		case "REVERTRESIZE":
			return cr, s.w.RevertResize(ctx, externalID)
		case "RENAME":
			srv, err := s.w.RenameServer(ctx, externalID, mstr(data, "name"))
			if err != nil {
				return nil, err
			}
			cr.Data = map[string]any{"server": srv}
			return s.repo.Insert(ctx, cr)
		}
	case cloud.TypeRouter:
		// Router actions. interfaceId in ADD_INTERFACE is the SUBNET's CACHE id (resolved to
		// its externalId); in DELETE_INTERFACE it is passed straight to neutron as a PORT id.
		switch action {
		case "ADD_INTERFACE":
			// interfaceId is the subnet's neutron external id. Subnets have no cache row of their own
			// (their ids live on the parent NETWORK doc), so resolveExtID — a cache-id→externalId
			// lookup — always misses and would send an empty subnet id. Pass it straight through like
			// DELETE_INTERFACE's port id; the client is scoped to the project's tenant, so neutron
			// enforces ownership.
			return cr, s.w.AddRouterInterface(ctx, externalID, mstr(data, "interfaceId"))
		case "DELETE_INTERFACE":
			return cr, s.w.RemoveRouterInterfaceByPort(ctx, externalID, mstr(data, "interfaceId"))
		case "ADD_EXTERNAL_GATEWAY":
			rt, err := s.w.SetRouterGateway(ctx, externalID, mstr(data, "networkId"))
			if err != nil {
				return nil, err
			}
			cr.Data = map[string]any{"router": rt, "routerName": routerName(cr)}
			return s.repo.Insert(ctx, cr)
		case "DELETE_EXTERNAL_GATEWAY":
			rt, err := s.w.SetRouterGateway(ctx, externalID, "")
			if err != nil {
				return nil, err
			}
			cr.Data = map[string]any{"router": rt, "routerName": routerName(cr)}
			return s.repo.Insert(ctx, cr)
		}
	case cloud.TypeFloatingIP:
		switch action {
		case "ASSIGN":
			// FE sends data.id = the PORT's CACHE id (resolved to the port externalId).
			portExtID := s.resolveExtID(ctx, firstStrAny(mstr(data, "id"), mstr(data, "portId")))
			fip, err := s.w.AssociateFloatingIP(ctx, externalID, portExtID)
			if err != nil {
				return nil, err
			}
			cr.Data = map[string]any{"floatingIp": fip}
			return s.repo.Insert(ctx, cr)
		case "UNASSIGN":
			fip, err := s.w.DisassociateFloatingIP(ctx, externalID)
			if err != nil {
				return nil, err
			}
			cr.Data = map[string]any{"floatingIp": fip}
			return s.repo.Insert(ctx, cr)
		}
	case cloud.TypePort:
		// Port UPDATE: data{name?, portSecurityEnabled?, securityGroups?}. Guard:
		// disabling port security while requesting security groups is rejected;
		// disabling forces securityGroups=[].
		if action == "UPDATE" {
			opts := client.UpdatePortOpts{}
			if v, ok := data["name"].(string); ok {
				opts.Name = &v
			}
			var pse *bool
			if v, ok := data["portSecurityEnabled"].(bool); ok {
				pse = &v
				opts.PortSecurityEnabled = &v
			}
			var sgs *[]string
			if _, present := data["securityGroups"]; present {
				list := strSlice(data["securityGroups"])
				sgs = &list
			}
			if pse != nil && !*pse {
				if sgs != nil && len(*sgs) > 0 {
					return nil, fmt.Errorf("Cannot set security groups while disabling port security.")
				}
				empty := []string{}
				opts.SecurityGroups = &empty
			} else if sgs != nil {
				opts.SecurityGroups = sgs
			}
			// allowedAddressPairs: present (even empty) → set/replace; absent → leave unchanged.
			if _, present := data["allowedAddressPairs"]; present {
				pairs := addressPairs(data["allowedAddressPairs"])
				opts.AllowedAddressPairs = &pairs
			}
			port, err := s.w.UpdatePort(ctx, externalID, opts)
			if err != nil {
				return nil, err
			}
			cr.Data = map[string]any{"port": port}
			return s.repo.Insert(ctx, cr)
		}
	case cloud.TypeSubnet:
		// Subnet UPDATE: data{name?, enableDhcp?, gatewayIp?, dnsNameServers?}. gatewayIp present
		// (even "") sets/clears the gateway; dnsNameServers present replaces the list.
		if action == "UPDATE" {
			opts := client.UpdateSubnetOpts{}
			if v, ok := data["name"].(string); ok {
				opts.Name = &v
			}
			if v, ok := data["enableDhcp"].(bool); ok {
				opts.EnableDHCP = &v
			}
			if v, present := data["gatewayIp"]; present {
				g, _ := v.(string)
				opts.GatewayIP = &g
			}
			if _, present := data["dnsNameServers"]; present {
				dns := strSlice(data["dnsNameServers"])
				opts.DNSNameservers = &dns
			}
			sub, err := s.w.UpdateSubnet(ctx, externalID, opts)
			if err != nil {
				return nil, err
			}
			cr.Data = map[string]any{"subnet": sub}
			return s.repo.Insert(ctx, cr)
		}

	case cloud.TypeSecurityGroup:
		// Security-group actions: ADD_RULE / DELETE_RULE (LIST_RULES is a read-action handled
		// in cloud_writes.go). After a rule change, re-fetch the group so data carries the new ruleset.
		switch action {
		case "ADD_RULE":
			if _, err := s.w.CreateSecGroupRule(ctx, client.CreateSecGroupRuleOpts{
				SecGroupID:     externalID,
				Direction:      mstr(data, "direction"),
				EtherType:      mstr(data, "etherType"),
				Protocol:       mstr(data, "protocol"),
				PortRangeMin:   mint(data, "portRangeMin"),
				PortRangeMax:   mint(data, "portRangeMax"),
				RemoteIPPrefix: mstr(data, "remoteIpPrefix"),
				RemoteGroupID:  mstr(data, "remoteGroupId"),
			}); err != nil {
				return nil, err
			}
			return s.refreshSecurityGroup(ctx, cr, externalID)
		case "DELETE_RULE":
			if err := s.w.DeleteSecGroupRule(ctx, mstr(data, "ruleId")); err != nil {
				return nil, err
			}
			return s.refreshSecurityGroup(ctx, cr, externalID)
		}
	}
	return nil, fmt.Errorf("unsupported action %q for type %q", action, cr.Type)
}

// asAnySlice normalizes a value that may be []any (just-built) or a named slice type with
// the same underlying shape (a stored-doc round-trip) — see asList.
func asAnySlice(v any) []any {
	l, _ := asList(v)
	return l
}

// detachServerVolumes detaches every cached volume attached to the server before it is destroyed
// (list VOLUMEs associated to the server, DETACH
// each). Best-effort — a detach failure must not block the server delete (most clouds auto-detach
// anyway); the goal is to avoid stale cache attachment records + clouds that refuse the delete.
func (s *WriteService) detachServerVolumes(ctx context.Context, server *cloud.CloudResource) {
	if server == nil {
		return
	}
	vols, err := s.repo.FindByProjectAndType(ctx, server.ProjectID, cloud.TypeVolume)
	if err != nil {
		return
	}
	for i := range vols {
		v := &vols[i]
		for _, a := range asAnySlice(v.Data["attachments"]) {
			sid := attServerID(a)
			if sid == "" || (sid != server.ID && sid != server.ExternalID) {
				continue
			}
			_ = s.w.DetachVolume(ctx, server.ExternalID, v.ExternalID) // attachment id == volume id (nova)
			v.Data = withoutAttachment(v.Data, sid)
			_, _ = s.repo.Insert(ctx, v)
			break
		}
	}
}

// withAttachment appends a volume attachment record to data.attachments.
func withAttachment(data map[string]any, att map[string]any, serverID string) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	list := asAnySlice(data["attachments"])
	rec := map[string]any{"attachmentId": att["id"], "device": att["device"], "serverId": serverID}
	data["attachments"] = append(list, rec)
	return data
}

// withoutAttachment removes the attachment for serverID from data.attachments. Elements may be
// map[string]any (just-built) or a round-tripped named map shape, so serverId is read via attServerID.
func withoutAttachment(data map[string]any, serverID string) map[string]any {
	if data == nil {
		return map[string]any{}
	}
	list := asAnySlice(data["attachments"])
	kept := make([]any, 0, len(list))
	for _, a := range list {
		if attServerID(a) == serverID {
			continue
		}
		kept = append(kept, a)
	}
	data["attachments"] = kept
	return data
}

// attServerID reads an attachment element's serverId whatever map shape it decoded as
// (see asMap).
func attServerID(a any) string {
	if m, ok := asMap(a); ok {
		s, _ := m["serverId"].(string)
		return s
	}
	return ""
}

// Delete dispatches a delete by the cached resource's type, removes it from OpenStack, then
// archives the cache doc.
func (s *WriteService) Delete(ctx context.Context, serviceID, externalID string) error {
	cr, err := s.repo.FindByServiceIDAndExternalID(ctx, serviceID, externalID)
	if err != nil {
		return err
	}
	if cr == nil {
		return nil
	}
	if err := s.deleteCloudObject(ctx, cr); err != nil {
		return err
	}
	return s.repo.DeleteAndArchive(ctx, cr, time.Now().UTC())
}

// DeleteResource deletes the cloud object for an already-resolved resource that has NO cache row —
// the live-listed types (floating IPs, ports, security groups) the FE deletes by their neutron
// external id. Nothing to archive (they were never cached).
func (s *WriteService) DeleteResource(ctx context.Context, cr *cloud.CloudResource) error {
	return s.deleteCloudObject(ctx, cr)
}

// deleteCloudObject dispatches the type-specific cloud delete for a resolved resource (cached or
// live-resolved).
func (s *WriteService) deleteCloudObject(ctx context.Context, cr *cloud.CloudResource) error {
	externalID := cr.ExternalID
	var err error
	switch cr.Type {
	case cloud.TypeNetwork:
		err = s.w.DeleteNetwork(ctx, externalID)
	case cloud.TypeSubnet:
		err = s.w.DeleteSubnet(ctx, externalID)
	case cloud.TypeRouter:
		err = s.w.DeleteRouter(ctx, externalID)
	case cloud.TypeServer, cloud.TypeBaremetalServer:
		s.detachServerVolumes(ctx, cr) // detach attached volumes first (best-effort)
		err = s.w.DeleteServer(ctx, externalID)
	case cloud.TypeVolume:
		err = s.w.DeleteVolume(ctx, externalID)
	case cloud.TypePort:
		err = s.w.DeletePort(ctx, externalID)
	case cloud.TypeFloatingIP:
		err = s.w.DeleteFloatingIP(ctx, externalID)
	case cloud.TypeSecurityGroup:
		err = s.w.DeleteSecurityGroup(ctx, externalID)
	case cloud.TypeVolumeSnapshot:
		err = s.w.DeleteVolumeSnapshot(ctx, externalID)
	case cloud.TypeKeypair:
		// nova keypairs are keyed by NAME, not the synthetic "<name>_<userId>" externalId.
		err = s.w.DeleteKeypair(ctx, keypairName(cr, externalID))
	case cloud.TypeServerGroup:
		err = s.w.DeleteServerGroup(ctx, externalID)
	case cloud.TypeDNSZone:
		err = s.w.DeleteZone(ctx, externalID)
	case cloud.TypeBarbicanSecret:
		err = s.w.DeleteSecret(ctx, externalID)
	case cloud.TypeBucket:
		err = s.w.DeleteBucket(ctx, externalID)
	case cloud.TypeLoadBalancer:
		err = s.w.DeleteLoadBalancer(ctx, externalID)
	case cloud.TypeStack:
		// Heat delete is NAME+ID keyed — the name lives in the cached data.stack (stack_name/name).
		err = s.w.DeleteStack(ctx, stackName(cr), externalID)
	case cloud.TypeShare:
		err = s.w.DeleteShare(ctx, externalID)
	case cloud.TypeImage:
		err = s.w.DeleteImage(ctx, externalID)
	case cloud.TypeShareNetwork:
		err = s.w.DeleteShareNetwork(ctx, externalID)
	case cloud.TypeShareSecurityService:
		err = s.w.DeleteShareSecurityService(ctx, externalID)
	case cloud.TypeVPNService:
		err = s.w.DeleteVPNService(ctx, externalID)
	case cloud.TypeVPNEndpointGroup:
		err = s.w.DeleteVPNEndpointGroup(ctx, externalID)
	case cloud.TypeIKEPolicy:
		err = s.w.DeleteIKEPolicy(ctx, externalID)
	case cloud.TypeIPSecPolicy:
		err = s.w.DeleteIPSecPolicy(ctx, externalID)
	case cloud.TypeIPSecSiteConnection:
		err = s.w.DeleteIPSecSiteConnection(ctx, externalID)
	case cloud.TypeKubernetesCluster:
		err = s.w.DeleteCluster(ctx, externalID)
	case cloud.TypeBarbicanContainer:
		err = s.w.DeleteContainer(ctx, externalID)
	case cloud.TypeUser:
		err = s.w.DeleteUser(ctx, externalID)
	case cloud.TypeTrilioWorkload:
		err = s.w.DeleteWorkload(ctx, externalID)
	case cloud.TypeTrilioSnapshot:
		err = s.w.DeleteSnapshot(ctx, externalID)
	case cloud.TypeTrilioBackupTarget:
		err = s.w.DeleteBackupTarget(ctx, externalID)
	case cloud.TypeTrilioRestore:
		err = s.w.DeleteRestore(ctx, externalID)
	default:
		return fmt.Errorf("unsupported delete type %q", cr.Type)
	}
	return err
}

// resolveExtID maps a Stratos CloudResource CACHE id to its OpenStack externalId, REQUIRING the
// referenced row to belong to the authorized project (§6). A raw external id (no cache row) or a
// row owned by another project no longer falls through — it resolves to "" so the cloud write
// fails / drops the field, instead of letting a create/action reach across to another tenant's
// resource by id.
// ponytail: rejects by returning "" (write fails / optional field dropped) rather than a typed 404
// — swap for an error-returning resolver if the callers need a clean 404.
func (s *WriteService) resolveExtID(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	cr, _ := s.repo.FindByID(ctx, id)
	if crOwnedBy(cr, s.projectID) {
		return cr.ExternalID
	}
	return ""
}

// crOwnedBy is the project-scoped ownership gate for a resolved cache row: it must exist, carry an
// externalId, and (when a project scope is known) belong to that project. projectID=="" (e.g. an
// admin WriteService with no project scope) still requires a real owned cache row, so a raw
// external id is always rejected.
func crOwnedBy(cr *cloud.CloudResource, projectID string) bool {
	return cr != nil && cr.ExternalID != "" && (projectID == "" || cr.ProjectID == projectID)
}

// stackName reads the cached Heat stack's name (data.stack.stack_name||name) — needed because Heat
// delete is name+id keyed.
func stackName(cr *cloud.CloudResource) string {
	st, _ := cr.Data["stack"].(map[string]any)
	if st == nil {
		return ""
	}
	if n, _ := st["stack_name"].(string); n != "" {
		return n
	}
	n, _ := st["name"].(string)
	return n
}

// routerName reads the cached router's display name (data.routerName), tolerating JSON round-trip.
func routerName(cr *cloud.CloudResource) string {
	if n, _ := cr.Data["routerName"].(string); n != "" {
		return n
	}
	return ""
}

// fqdn normalizes a DNS domain to the Designate zone name form: trim
// whitespace + any trailing dots, then append a single trailing dot. "" stays "".
func fqdn(domain string) string {
	s := strings.TrimRight(strings.TrimSpace(domain), ".")
	if s == "" {
		return ""
	}
	return s + "."
}

// firstStrAny returns the first non-empty string.
func firstStrAny(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// refreshSecurityGroup re-fetches the security group after a rule change and re-caches it (the
// neutron group embeds its security_group_rules, so the detail view shows the new ruleset).
func (s *WriteService) refreshSecurityGroup(ctx context.Context, cr *cloud.CloudResource, externalID string) (*cloud.CloudResource, error) {
	sg, err := s.w.GetSecurityGroup(ctx, externalID)
	if err != nil {
		return nil, err
	}
	cr.Data = map[string]any{"securityGroup": sg}
	return s.repo.Insert(ctx, cr)
}

// keypairName recovers the nova keypair name from the cached resource (data.keypair.name), falling
// back to the externalId with the synthetic "_<userId>" suffix trimmed.
func keypairName(cr *cloud.CloudResource, externalID string) string {
	if kp, ok := asMap(cr.Data["keypair"]); ok {
		if n, _ := kp["name"].(string); n != "" {
			return n
		}
	}
	if cr.UserID != "" {
		return strings.TrimSuffix(externalID, "_"+cr.UserID)
	}
	return externalID
}

// map readers for the free-form controller `data`.
func mstr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func mbool(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

// mboolPtr returns a *bool: nil when the key is absent/non-bool, else
// the value — so a present `false` is distinguishable from an omitted field (e.g. portSecurityEnabled).
func mboolPtr(m map[string]any, k string) *bool {
	if b, ok := m[k].(bool); ok {
		return &b
	}
	return nil
}

func mint(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// containerSecretRefs builds the Barbican container secret-ref list from the FE's
// data.secretRefs ([{name, secretRef}]).
func containerSecretRefs(v any) []client.ContainerSecretRef {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]client.ContainerSecretRef, 0, len(raw))
	for _, x := range raw {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		ref := client.ContainerSecretRef{Name: mstr(m, "name")}
		ref.SecretRef = firstStrAny(mstr(m, "secretRef"), mstr(m, "secret_ref"))
		out = append(out, ref)
	}
	return out
}

// strSlice coerces a free-form JSON array (or single string) to []string.
func strSlice(v any) []string {
	switch a := v.(type) {
	case []any:
		out := make([]string, 0, len(a))
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return a
	case string:
		if a == "" {
			return []string{}
		}
		return []string{a}
	}
	return []string{}
}

// ifaceUUIDs extracts the network uuids from a ServerCreateRequest networkInterfaces value
// ([{uuid, port}] — may decode as []any of map[string]any).
func ifaceUUIDs(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			if id, _ := m["uuid"].(string); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// ifaceFixedIPs maps networkInterfaces [{uuid, fixedIp}] → {networkID: fixedIP} for the entries that
// request a specific IP (blank fixedIp entries are omitted).
func ifaceFixedIPs(v any) map[string]string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["uuid"].(string)
		ip, _ := m["fixedIp"].(string)
		if id != "" && ip != "" {
			out[id] = ip
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// addressPairs parses allowedAddressPairs [{ipAddress, macAddress}] → []client.AddressPair (blank
// IPs dropped).
func addressPairs(v any) []client.AddressPair {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]client.AddressPair, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		ip, _ := m["ipAddress"].(string)
		if ip == "" {
			continue
		}
		mac, _ := m["macAddress"].(string)
		out = append(out, client.AddressPair{IPAddress: ip, MACAddress: mac})
	}
	return out
}

func mstrs(m map[string]any, k string) []string {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

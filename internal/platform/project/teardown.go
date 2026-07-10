package project

// teardown.go implements the project-deletion cloud cascade (the admin DELETE /project/{id}/now
// leg). It is scoped STRICTLY to the project's own cached resources + its own Keystone tenant(s) —
// it can never touch another project.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/providers"
)

// deletionOrder ranks a cloud-resource type for teardown: LOWER = delete FIRST. Dependents (that
// hold references onto others) go before their dependencies, so a best-effort sweep in this order
// minimizes "resource still in use" failures. Unknown/leaf types sort last.
func deletionOrder(t string) int {
	switch t {
	case cloud.TypeKubernetesCluster, cloud.TypeStack, cloud.TypeLoadBalancer:
		return 0 // composites that own many children
	case cloud.TypeTrilioRestore, cloud.TypeTrilioSnapshot, cloud.TypeTrilioWorkload, cloud.TypeTrilioBackupTarget:
		return 1
	case cloud.TypeIPSecSiteConnection:
		return 2
	case cloud.TypeVPNService, cloud.TypeVPNEndpointGroup, cloud.TypeIKEPolicy, cloud.TypeIPSecPolicy:
		return 3
	case cloud.TypeServer, cloud.TypeBaremetalServer:
		return 4 // deleting an instance releases its FIP/port/volume attachments
	case cloud.TypeFloatingIP:
		return 5
	case cloud.TypePort:
		return 6
	case cloud.TypeVolumeSnapshot, cloud.TypeShareSnapshot, cloud.TypeShareSnapshotGroup:
		return 7
	case cloud.TypeVolume, cloud.TypeVolumeBackup:
		return 8
	case cloud.TypeShare, cloud.TypeShareGroup:
		return 9
	case cloud.TypeShareNetwork, cloud.TypeShareSecurityService:
		return 10
	case cloud.TypeSubnet:
		return 11
	case cloud.TypeNetwork:
		return 12
	case cloud.TypeRouter:
		return 13 // after its subnets/ports are gone
	default:
		return 100 // security-group / keypair / image / bucket / dns-zone / secret / server-group / …
	}
}

// teardownSweeps is how many dependency-ordered passes the cascade makes: a resource that fails
// because a blocker still exists (e.g. a network with a lingering port) can succeed on a later pass
// once the blocker is deleted.
const teardownSweeps = 3

// TeardownProject cascade-deletes a project's cloud resources (best-effort, dependency-ordered, with
// a few retry sweeps), deletes the project's Keystone tenant(s), then marks the project DELETED. It
// runs to completion regardless of individual failures; the periodic sync/reconcile is the backstop
// for anything left behind. Returns a non-nil error summarising what could not be deleted (the
// project is still marked DELETED — a re-run or the sync job mops up the remainder).
func (h *Handler) TeardownProject(ctx context.Context, projectID string) error {
	p, err := h.svc.GetProjectByID(ctx, projectID)
	if err != nil {
		return err
	}
	resources, err := h.cloud.FindAllByProjectID(ctx, projectID)
	if err != nil {
		return err
	}
	sort.SliceStable(resources, func(i, j int) bool {
		return deletionOrder(resources[i].Type) < deletionOrder(resources[j].Type)
	})

	remaining := resources
	for sweep := 0; sweep < teardownSweeps && len(remaining) > 0; sweep++ {
		var stillLeft []cloud.CloudResource
		for i := range remaining {
			res := &remaining[i]
			cc, ok := h.tryTenantClient(ctx, p, res.ServiceID)
			if !ok {
				stillLeft = append(stillLeft, *res)
				continue
			}
			ws := providers.NewWriteService(cc, h.cloud)
			if err := ws.Delete(ctx, res.ServiceID, res.ExternalID); err != nil {
				stillLeft = append(stillLeft, *res)
			}
		}
		remaining = stillLeft
	}

	// cephRevokeFailed records that a ceph-s3 credential could NOT be revoked (RGW purge failed), so its
	// local record was deliberately kept and the caller must be told deprovisioning is incomplete.
	cephRevokeFailed := false

	// Delete the per-service tenant the project is bootstrapped on (admin-scoped client, not
	// tenant-scoped — a tenant cannot delete itself). Best-effort per provider kind.
	for _, svcID := range p.ServiceIDs() {
		es, err := h.esSvc.Get(ctx, svcID)
		if err != nil || es == nil {
			continue
		}
		if es.IsCephS3() {
			// ceph-s3: purge the RGW user + ALL its data, then drop the stored credential.
			// The customer-facing DeleteBucket in the sweep above refuses a NON-EMPTY bucket (S3 semantics,
			// same as Swift), so those buckets are still in `remaining`. Force-delete each of THIS project's
			// ceph buckets via Admin Ops (purge-objects) so their cache rows get archived and teardown stops
			// reporting them as undeleted.
			region := es.CephRegion()
			if region == "" {
				region = h.cloudRegion
			}
			// Prefer the uid that was actually PROVISIONED (stored on the credential) over re-deriving it
			// from current service config — a uidPrefix change between provision and teardown would
			// otherwise target a user that never existed and leave the real one undeprovisioned.
			rgwUID := es.RGWUIDFor(p.ID)
			if h.cephCreds != nil {
				if cred, _ := h.cephCreds.Get(ctx, p.ID, svcID); cred != nil && cred.RGWUID != "" {
					rgwUID = cred.RGWUID
				}
			}
			adminCC, cerr := client.NewCephS3(ctx, es.CephConfig(region, "", "", rgwUID))
			if cerr != nil {
				// Could not even build the admin client → cloud credentials are NOT revoked. Keep every
				// local credential record so a re-run / operator can still revoke, and report the failure.
				slog.Error("teardown: build ceph admin client", "project", p.ID, "serviceId", svcID, "err", cerr)
				cephRevokeFailed = true
				continue
			}
			now := time.Now().UTC()
			var stillLeft []cloud.CloudResource
			for i := range remaining {
				res := &remaining[i]
				if res.ServiceID != svcID || res.Type != cloud.TypeBucket {
					stillLeft = append(stillLeft, *res)
					continue
				}
				if derr := adminCC.ForceDeleteCephBucket(ctx, res.ExternalID); derr != nil {
					stillLeft = append(stillLeft, *res)
					continue
				}
				// The bucket is gone cloud-side; archiving the cache row can still fail. Keep it in
				// `remaining` so teardown reports it (a re-run's ForceDeleteCephBucket is a no-op and retries
				// the archive) rather than silently claiming success with a stale row left behind.
				if aerr := h.cloud.DeleteAndArchive(ctx, res, now); aerr != nil {
					slog.Error("teardown: archive ceph bucket row", "project", p.ID, "externalId", res.ExternalID, "err", aerr)
					stillLeft = append(stillLeft, *res)
				}
			}
			remaining = stillLeft
			// Extra S3 keys are SEPARATE RGW users ("<projectUid>-<name>"); purging the project's own user
			// does not touch them. FAIL CLOSED (finding F1): only drop a local key record once its RGW user
			// is actually purged — otherwise a transient purge failure would leave a live S3 key behind with
			// no local inventory left to revoke it.
			if h.cephKeys != nil {
				if keys, kerr := h.cephKeys.List(ctx, p.ID, svcID); kerr == nil {
					for i := range keys {
						if derr := adminCC.DeleteCephChildUser(ctx, keys[i].RGWUID); derr != nil {
							slog.Error("teardown: purge ceph child key", "project", p.ID, "rgwUid", keys[i].RGWUID, "err", derr)
							cephRevokeFailed = true
							continue // keep the local record so the key can still be revoked later
						}
						// The RGW user is gone; a failed record delete leaves stale inventory, so report it
						// and keep teardown retryable (the purge re-run is a not-found no-op).
						if derr := h.cephKeys.Delete(ctx, keys[i].ID); derr != nil {
							slog.Error("teardown: delete ceph child key record", "project", p.ID, "keyId", keys[i].ID, "err", derr)
							cephRevokeFailed = true
						}
					}
				} else {
					slog.Error("teardown: list ceph child keys", "project", p.ID, "err", kerr)
					cephRevokeFailed = true
				}
			}
			if perr := adminCC.PurgeCephUser(ctx); perr != nil {
				// The parent user (and its keys) is still alive. Keep the stored credential so it can be
				// revoked on a retry; do not blank the only record that identifies it.
				slog.Error("teardown: purge ceph user", "project", p.ID, "rgwUid", rgwUID, "err", perr)
				cephRevokeFailed = true
			} else if h.cephCreds != nil {
				if derr := h.cephCreds.Delete(ctx, p.ID, svcID); derr != nil {
					// RGW user purged but the local record remains — report it so teardown is retried
					// rather than claiming a clean deprovision with stale credentials on file.
					slog.Error("teardown: delete ceph credential record", "project", p.ID, "serviceId", svcID, "err", derr)
					cephRevokeFailed = true
				}
			}
			continue
		}
		extProj := p.ExternalProjectID(svcID)
		if extProj == "" {
			continue
		}
		adminCC, err := client.New(ctx, es.ClientConfig(h.cloudRegion))
		if err != nil {
			continue
		}
		_ = adminCC.DeleteProject(ctx, extProj)
	}

	// Terminal state: mark the project DELETED (keep the doc for audit history).
	p.Status = "DELETED"
	if err := h.svc.Save(ctx, p); err != nil {
		return err
	}
	if cephRevokeFailed {
		return fmt.Errorf("teardown could not revoke some ceph-s3 credentials; their local records were kept for retry")
	}
	if len(remaining) > 0 {
		return fmt.Errorf("teardown left %d resource(s) undeleted; the sync job will reconcile them", len(remaining))
	}
	return nil
}

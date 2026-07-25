package billingjob

import (
	"context"
	"log/slog"
	"time"

	"github.com/menlocloud/stratos/internal/cloud/billingresource"
	"github.com/menlocloud/stratos/internal/platform/billingclient"
)

// remote.go is the charge path once billing runs as its own service.
//
// The work splits exactly where the in-process driver already splits it: RESOLUTION (read the cloud
// cache, resolve the profile's projects, build billable units) stays here because it is
// OpenStack-specific, and RATING moves to the billing service. The only difference from
// Service.Charge is that the units are POSTed instead of handed to pricing in-process.
//
// Note what this path does NOT touch: the local billing repo. The profile work-list comes from the
// billing service, and resolution only needs a profile id to walk orgs → projects → cloud
// resources. That is what makes the eventual removal of billing from this binary a deletion rather
// than a rewrite.

// Charger is the subset of billingclient.Client the remote driver needs (an interface so the driver
// can be tested without a server).
type Charger interface {
	ActiveProfileIDs(ctx context.Context) ([]string, error)
	Charge(ctx context.Context, req billingclient.ChargeRequest) (*billingclient.ChargeResponse, error)
}

// ResolveByProfileID builds one profile's charge request from local cloud state.
//
// It mirrors chargeBillingResource's resolution steps: the profile's ENABLED projects that have
// services, then each external service's project-scoped cloud resources dispatched through the
// type→Provider registry. Rating inputs the billing service cannot know (the resource-type catalog)
// travel with the request.
func (s *Service) ResolveByProfileID(ctx context.Context, profileID, timeUnit string, now time.Time) (billingclient.ChargeRequest, error) {
	cycleStart, cycleEnd := monthBounds(now)
	req := billingclient.ChargeRequest{
		ProfileID:      profileID,
		TimeUnit:       timeUnit,
		CycleStart:     cycleStart,
		CycleEnd:       cycleEnd,
		CycleTimestamp: truncateForTimeUnit(now, timeUnit),
		Catalog:        billingresource.Catalog(),
	}

	projects, err := s.activeProjectsWithServices(ctx, profileID)
	if err != nil {
		return req, err
	}
	externalServices, err := s.d.ExternalServices.List(ctx)
	if err != nil {
		return req, err
	}

	bc := s.billingContext(ctx)
	for i := range externalServices {
		es := &externalServices[i]
		resources, err := s.billingResources(ctx, bc, projects, es.ID)
		if err != nil {
			return req, err
		}
		// Skip services with nothing to charge: an empty group is pure wire overhead, and the
		// billing service would rate it to nothing anyway.
		if len(resources) == 0 {
			continue
		}
		req.Services = append(req.Services, billingclient.ServiceResources{
			ServiceID: es.ID,
			Resources: resources,
		})
	}
	return req, nil
}

// RemoteCharge is the cron entry point for the split charge flow: ask the billing service which
// profiles are ACTIVE, resolve each one's resources locally, and POST them back to be rated.
//
// Per-profile errors are logged and skipped rather than aborting the cycle — the same isolation the
// in-process loop and the RabbitMQ fan-out both provide, so one bad profile cannot stall the rest.
// The first error is returned so the run is still visibly unhealthy.
func RemoteCharge(ctx context.Context, resolver *Service, client Charger, timeUnit string, now time.Time, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}

	ids, err := client.ActiveProfileIDs(ctx)
	if err != nil {
		return err
	}

	var firstErr error
	for _, id := range ids {
		req, err := resolver.ResolveByProfileID(ctx, id, timeUnit, now)
		if err != nil {
			log.Error("remote charge: resolve", "profileId", id, "timeUnit", timeUnit, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if _, err := client.Charge(ctx, req); err != nil {
			log.Error("remote charge: charge", "profileId", id, "timeUnit", timeUnit, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}
	return firstErr
}

// compile-time proof that the concrete client satisfies the driver's interface.
var _ Charger = (*billingclient.Client)(nil)

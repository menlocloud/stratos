// Command stratos-api is the Go web server entrypoint: boot, read the config
// contract, connect PostgreSQL + RabbitMQ, attempt OIDC discovery, and serve
// health endpoints — so the Helm chart can deploy this image unchanged.
package main

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/menlocloud/stratos/internal/amqp"
	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/billingresource"
	"github.com/menlocloud/stratos/internal/cloud/cephcred"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/metrics"
	"github.com/menlocloud/stratos/internal/cloud/metricsjob"
	"github.com/menlocloud/stratos/internal/cloud/notification"
	"github.com/menlocloud/stratos/internal/cloud/providers"
	"github.com/menlocloud/stratos/internal/cloud/syncjob"
	"github.com/menlocloud/stratos/internal/config"
	"github.com/menlocloud/stratos/internal/health"
	"github.com/menlocloud/stratos/internal/oidc"
	"github.com/menlocloud/stratos/internal/pgdoc"
	"github.com/menlocloud/stratos/internal/platform/account"
	"github.com/menlocloud/stratos/internal/platform/admin"
	"github.com/menlocloud/stratos/internal/platform/adminapi"
	"github.com/menlocloud/stratos/internal/platform/affiliate"
	"github.com/menlocloud/stratos/internal/platform/audit"
	"github.com/menlocloud/stratos/internal/platform/billing"
	"github.com/menlocloud/stratos/internal/platform/billingjob"
	"github.com/menlocloud/stratos/internal/platform/catalog"
	"github.com/menlocloud/stratos/internal/platform/chargefanout"
	"github.com/menlocloud/stratos/internal/platform/externalservice"
	"github.com/menlocloud/stratos/internal/platform/feature"
	"github.com/menlocloud/stratos/internal/platform/job"
	"github.com/menlocloud/stratos/internal/platform/lock"
	"github.com/menlocloud/stratos/internal/platform/mail"
	mcpsrv "github.com/menlocloud/stratos/internal/platform/mcp"
	"github.com/menlocloud/stratos/internal/platform/message"
	"github.com/menlocloud/stratos/internal/platform/order"
	"github.com/menlocloud/stratos/internal/platform/org"
	"github.com/menlocloud/stratos/internal/platform/payment"
	"github.com/menlocloud/stratos/internal/platform/platformconfig"
	"github.com/menlocloud/stratos/internal/platform/pricing"
	"github.com/menlocloud/stratos/internal/platform/project"
	"github.com/menlocloud/stratos/internal/platform/projectinvite"
	"github.com/menlocloud/stratos/internal/platform/promotion"
	"github.com/menlocloud/stratos/internal/platform/scheduler"
	"github.com/menlocloud/stratos/internal/platform/sse"
	"github.com/menlocloud/stratos/internal/platform/user"
	"github.com/menlocloud/stratos/internal/server"
	"github.com/menlocloud/stratos/pkg/auth"
	"github.com/menlocloud/stratos/pkg/httpx"
	"github.com/menlocloud/stratos/pkg/textcrypt"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return err // fail-closed: refuse to start half-configured
	}

	log := newLogger(cfg.LogLevel)
	log.Info("starting stratos-api (go)",
		"appPort", cfg.Server.Port, "mgmtPort", cfg.Management.Port,
		"rabbitHost", cfg.Rabbit.Host)

	ctx := context.Background()

	// PostgreSQL: the primary datastore.
	pg, err := pgdoc.Connect(ctx, cfg.DB.URL)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer func() { _ = pg.Close(context.Background()) }()

	// RabbitMQ: maintain the connection in the background (connect + auto-
	// reconnect) so liveness is immediate and readiness self-heals if the
	// broker connection drops — otherwise a flapped pod deadlocks rollouts.
	var rabbit atomic.Pointer[amqp.Client]
	go maintainRabbit(ctx, cfg, log, &rabbit)
	defer func() {
		if ac := rabbit.Load(); ac != nil {
			_ = ac.Close()
		}
	}()

	// Platform: User repo + the principal resolver (get-or-create by sub).
	users := user.NewRepo(pg)
	go func() {
		if err := users.EnsureIndexes(ctx); err != nil {
			log.Warn("ensure user indexes", "err", err)
		}
	}()
	// Audit pipeline (async writer; client read endpoints). Handlers emit events
	// after a successful mutation; the org/account audit handlers serve the log.
	auditSvc := audit.NewService(audit.NewRepo(pg), log)
	acct := account.NewHandler(users, auditSvc)

	// Organization slice (+ minimal billing-profile stub for org create).
	orgRepo := org.NewRepo(pg)
	go func() {
		if err := orgRepo.EnsureIndexes(ctx); err != nil {
			log.Warn("ensure org indexes", "err", err)
		}
	}()
	billingRepo := billing.NewRepo(pg)
	orgSvc := org.NewService(orgRepo, billingRepo, platformconfig.NewRepo(pg))
	orgPolicy := org.NewPolicy(orgRepo)
	orgH := org.NewHandler(orgSvc, orgPolicy, orgRepo, users, auditSvc)
	// Custom org roles (roleDefinition) — completes RBAC (policy now resolves
	// custom-role permissions; project policy inherits it via org delegation).
	roleH := org.NewRoleHandler(org.NewRoleService(orgRepo), orgSvc, orgPolicy, users, auditSvc)
	// Org audit log (CLIENT_AREA events, cursor-paginated, ORGANIZATION_READ).
	orgAuditH := org.NewAuditHandler(orgSvc, orgPolicy, users, auditSvc)
	// Billing-profile read endpoints (BillingSummary) + the billing-domain list endpoints
	// (bill/promo/savings/collect → typed DTOs; pricingRepo supplies tax rates for BillDto gross).
	pricingRepo := pricing.NewRepo(pg)
	// mail + message templates: seed the 18 system templates (idempotent) + build the mail.Service.
	// The gateway is resolved per-send: the admin-configured SMTP integration (DB) wins, else the
	// STRATOS_MAIL_* env gateway; NoopMailer when neither is configured → email side-effects no-op.
	// This lets an operator configure mail from Admin → Integrations without a restart.
	msgRepo := message.NewRepo(pg)
	message.SeedSystemTemplates(ctx, msgRepo, log)
	mailBusiness := os.Getenv("STRATOS_MAIL_BUSINESS_NAME")
	if mailBusiness == "" {
		mailBusiness = "Stratos"
	}
	// STRATOS_DEFAULT_NETWORK_MTU stamps client-created networks with a fixed MTU; unset/0 leaves it
	// to neutron's provider default (e.g. the geneve/vxlan value).
	if v, err := strconv.Atoi(os.Getenv("STRATOS_DEFAULT_NETWORK_MTU")); err == nil && v > 0 {
		providers.SetDefaultNetworkMTU(v)
	}
	// Data-at-rest encryptor (shared): esSvc encrypts secrets on write; the mail SMTP gateway and
	// the billing payment-gateway reads decrypt with the SAME key. Declared here (ahead of esSvc at
	// its original site) so the mail resolver closure below can capture it.
	enc := textcrypt.New(cfg.Encryption.DefaultKey)
	billingRepo.SetEncryptor(enc) // decrypt payment-gateway (Stripe) secrets on GetGateway
	integrationStore := pg.C("thirdPartyIntegration")
	envMailer := mail.FromEnv()
	envFrom := os.Getenv("STRATOS_MAIL_FROM")
	mailSvc := mail.NewService(
		func(ctx context.Context, key string, vars map[string]any) (string, string, bool, error) {
			r, ok, err := msgRepo.Render(ctx, key, vars)
			return r.Title, r.Body, ok, err
		},
		// ponytail: DB read per send — emails are infrequent; add a short TTL cache if it ever matters.
		func(ctx context.Context) (mail.Mailer, string) {
			if m, from, ok := mail.SMTPFromStore(ctx, integrationStore, enc); ok {
				return m, from
			}
			return envMailer, envFrom
		},
		mailBusiness, log,
	)
	payService := billing.NewPayService(billingRepo, pricingRepo)
	// add-funds: build a Stripe gateway per integration secret (swappable for tests).
	stripeGatewayFor := func(secret string) payment.Gateway { return payment.NewStripeGateway(secret) }
	addFundsSvc := payment.NewAddFundsService(billingRepo, pricingRepo, stripeGatewayFor)
	addFundsSvc.SetNotifier(mailSvc)
	registerCardSvc := payment.NewRegisterCardService(billingRepo, stripeGatewayFor)
	collectSvc := payment.NewCollectService(billingRepo, pricingRepo, stripeGatewayFor)
	collectSvc.SetNotifier(mailSvc)
	// Transaction scanner (transaction-sync cron): reconciles stuck PENDING deposits/collects.
	txnScanner := payment.NewTransactionScanner(billingRepo, addFundsSvc, collectSvc, log)
	payService.SetNotifier(mailSvc)
	orderRepo := order.NewRepo(pg)
	billingH := org.NewBillingHandler(orgSvc, orgPolicy, billingRepo, pricingRepo, payService, addFundsSvc, registerCardSvc, collectSvc, cfg.Self.UIBaseURL, users, orderRepo)
	// Public billing-configuration read.
	billingCfgH := billing.NewConfigHandler(billingRepo)

	// Platform configuration (client-facing read; UI bootstrap + project quota).
	authStrategy := "LOCAL_IDP"
	if cfg.Auth.Main.IssuerURI != "" {
		authStrategy = "REMOTE_OIDC"
	}
	pcfgH := platformconfig.NewHandler(platformconfig.NewRepo(pg), authStrategy, "")

	// Feature flags — reports the available feature set.
	featureH := feature.NewHandler()

	// Project slice (memberships embedded; reuses the org service/policy for
	// org-level gates + the billing stub for the new-project status decision).
	projectRepo := project.NewRepo(pg)
	go func() {
		if err := projectRepo.EnsureIndexes(ctx); err != nil {
			log.Warn("ensure project indexes", "err", err)
		}
	}()
	projectSvc := project.NewService(projectRepo, orgRepo, billingRepo, users, platformconfig.NewRepo(pg))
	// Wire the org member→project propagation now that the project service exists (org can't import
	// project — set via a setter).
	orgH.SetProjectMemberAdder(projectSvc)
	cloudRepo := cloud.NewRepo(pg)
	// cloudCli holds the live CloudClient (set in the background once OpenStack auth completes;
	// declared here so the project handler's write endpoints can resolve it lazily).
	var cloudCli atomic.Pointer[client.Client]
	// esSvc is created here (ahead of the admin handler) so the project handler's client cloud read
	// endpoints (init menu / project services) can resolve external services too. It shares `enc`
	// (declared above, near the mail gateway) so write-encrypt and read-decrypt use the same key.
	esSvc := externalservice.NewService(externalservice.NewRepo(pg), enc)
	projectH := project.NewHandler(
		projectSvc,
		project.NewPolicy(orgPolicy),
		orgSvc,
		users,
		auditSvc,
		billingRepo,
		cloudRepo,
		esSvc,
		pricingRepo,
		project.NewInstanceMetadataReader(pg),
		func() *client.Client { return cloudCli.Load() },
		cfg.OpenStack.Region,
	)
	// Cloud-object download tokens (object-store DOWNLOAD action → GET /download/{token}).
	projectH.SetDownloads(cloud.NewDownloadRepo(pg), cfg.Self.APIBaseURL)
	projectH.SetCustomMenu(project.NewCustomMenuReader(pg))
	// Ceph RGW (S3) per-project credentials — enables ceph-s3 provisioning + bucket writes.
	cephCredRepo := cephcred.New(pg, enc)
	projectH.SetCephCreds(cephCredRepo, cephcred.NewKeyRepo(pg, enc))

	// Promotion (deposit config) + Affiliate (cfy check + project config/log) — client reads.
	// The promo-redeem authz gate resolves the bp's org WITH a membership check on the caller
	// (a non-member must not mint a credit on another org's profile); the resolver returns the
	// membership-404 from GetOrganizationForBillingProfile.
	promotionH := promotion.NewHandler(billingRepo, func(ctx context.Context, bpID, sub string) (string, string, error) {
		o, err := orgSvc.GetOrganizationForBillingProfile(ctx, bpID, sub)
		if err != nil {
			return "", "", err
		}
		return o.ID, o.BillingProfileID, nil
	})
	affiliateH := affiliate.NewHandler(affiliate.NewRepo(pg), projectSvc, orgSvc, billingRepo, users)
	catalogH := catalog.NewHandler(catalog.NewRepo(pg))
	orderH := order.NewHandler(orderRepo, users)
	inviteRepo := projectinvite.NewRepo(pg)
	if err := inviteRepo.EnsureIndexes(ctx); err != nil {
		log.Error("projectInvite TTL index", "err", err)
	}
	inviteH := projectinvite.NewHandler(inviteRepo, users, projectSvc, orgRepo, auditSvc, mailSvc, cfg.Self.UIBaseURL)
	// adminH is created below, after esSvc (it needs the externalService loader for cloud-admin reads).
	// SSE real-time stream (StreamingController + SseService). The in-memory pool + handler;
	// the real event source (os-notification → rabbit topic → Notify) is wired later.
	ssePool := sse.NewPool()
	sseH := sse.NewHandler(ssePool)
	// Gate stream subscription on real project membership: a user may only subscribe to a project's
	// stream if they are a member of that project (memberships.sub == user). FindForMember is the
	// direct member-scoped lookup (org members are propagated as project memberships on create).
	sseH.SetMembership(func(userID, projectID string) bool {
		p, err := projectRepo.FindForMember(context.Background(), projectID, userID)
		return err == nil && p != nil
	})

	// os-notification ingestion: POST /api/v1/notifications/
	// {externalServiceId}/{region} — the "Notifier URI" OpenStack/ceilometer POSTs lifecycle events
	// to, keeping the cloudResource cache live. permitAll (auth.go whitelist). The ResourceFetcher
	// re-reads the live object scoped to the resource's tenant; the ProjectResolver maps oslo
	// tenant_id → the internal project.
	notiResolver := notification.ResolverFunc(func(ctx context.Context, extProjID string) (string, bool) {
		p, err := projectRepo.FindByExternalProjectID(ctx, extProjID)
		if err != nil || p == nil {
			return "", false
		}
		return p.ID, true
	})
	notiFetcher := notification.FetcherFunc(func(ctx context.Context, extProjID, resType, extID string) (map[string]any, bool, error) {
		// Resolve the (single) OpenStack CLOUD service + its region, build an admin client scoped to
		// the resource's tenant, then per-type live get.
		services, err := esSvc.ListByType(ctx, externalservice.TypeCloud)
		if err != nil {
			return nil, false, err
		}
		var es *externalservice.ExternalService
		for i := range services {
			if services[i].IsNotDisabled() && services[i].Provider() == "openstack" {
				es = &services[i]
				break
			}
		}
		if es == nil {
			return map[string]any{"id": extID}, true, nil // no cloud service → record minimally
		}
		region := cfg.OpenStack.Region
		if rs := es.RegionNames(); len(rs) > 0 {
			region = rs[0]
		}
		cc, err := client.New(ctx, es.ClientConfigForProject(region, extProjID))
		if err != nil {
			return nil, false, err
		}
		return notification.FetchByType(ctx, cc, resType, extID)
	})
	notiSvc := notification.NewService(cloudRepo, notiFetcher, notiResolver, nil)
	notiSvc.SetLogger(log) // trace why an event is skipped/applied (a live update not landing)
	notiH := notification.NewHandler(notiSvc, log)
	// Per-provider webhook auth: the shared secret lives on each cloud's externalService
	// (secret.notificationSecret), so ceilometer's Notifier URI carries a secret scoped to that
	// provider. A provider with no secret configured keeps its webhook closed (fail-closed).
	notiH.SetSecretResolver(func(ctx context.Context, serviceID string) string {
		es, err := esSvc.Get(ctx, serviceID)
		if err != nil || es == nil {
			return ""
		}
		return es.NotificationSecret()
	})
	// After a notification is applied, push an SSE event to the project's open streams (best-effort).
	notiH.SetNotifier(func(serviceID, region, eventType string) {
		ssePool.Notify(sse.SseData{Type: "cloud_resource", Data: map[string]any{"eventType": eventType}})
	})

	// Resource-Server authenticator. Realms are discovered in the background so
	// startup is never blocked on an unreachable issuer; until they arrive,
	// bearer tokens fail closed (401) and unauthenticated protected paths 401.
	// Retries until every configured realm has a verifier: on a fresh install
	// the realms are often created (realm import / config-cli) after this binary
	// boots, and a realm that failed discovery once would otherwise reject its
	// tokens until a restart.
	authn := auth.New(log)
	go func() {
		for {
			realms := oidc.Discover(ctx, cfg, log)
			ar := make([]auth.Realm, 0, len(realms))
			missing := false
			for _, r := range realms {
				if r.Verifier == nil {
					missing = true
				}
				ar = append(ar, auth.Realm{Name: r.Name, ClientID: r.ClientID, IssuerURI: r.IssuerURI, Verifier: r.Verifier})
			}
			authn.SetRealms(ar)
			if !missing {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()
	// Admin-API SigV4 verification: resolve access keys from hmac_keys. The hmac_keys
	// collection also holds provider keys (erpCreate) that must NOT grant Admin-API / MCP access, so
	// the lookup is a POSITIVE allowlist: purpose:"admin-api" only. Every admin-api key writer stamps
	// this purpose (the admin hmac-keys endpoint + the mgmt gen-hmac-key trigger); provider keys carry
	// purpose:"provider". A positive match (vs the old $ne:"provider") means any key lacking an explicit
	// admin-api purpose is rejected rather than defaulted-in — defence in depth against a no-purpose row.
	hmacLookup := func(ctx context.Context, keyID string) (string, bool) {
		var doc struct {
			SecretKey string `json:"secretKey"`
		}
		filter := pgdoc.M{"_id": keyID, "purpose": "admin-api"}
		found, err := pg.C("hmac_keys").FindOne(ctx, filter, &doc)
		if err != nil || !found {
			return "", false
		}
		return doc.SecretKey, true
	}
	authn.SetHmacLookup(hmacLookup)

	// Cloud client (dev bootstrap from OpenStack env). Authenticated in the background
	// (non-fatal, like OIDC) so startup never blocks on the cloud. Used by the /debug/cloud
	// probe + the project cloud-write endpoints (cloudCli declared above the project handler).
	var cloudDebug http.HandlerFunc
	if cfg.OpenStack.AuthURL != "" {
		go func() {
			cc, err := client.New(ctx, client.Config{
				AuthURL: cfg.OpenStack.AuthURL, Region: cfg.OpenStack.Region,
				Username: cfg.OpenStack.Username, Password: cfg.OpenStack.Password,
				UserDomainName: cfg.OpenStack.UserDomain,
				ProjectName:    cfg.OpenStack.ProjectName, ProjectDomainName: cfg.OpenStack.ProjectDomain,
				AppCredID: cfg.OpenStack.AppCredID, AppCredSecret: cfg.OpenStack.AppCredSecret,
			})
			if err != nil {
				log.Warn("openstack auth failed", "err", err)
				return
			}
			cloudCli.Store(cc)
			log.Info("openstack cloud client ready", "region", cfg.OpenStack.Region)
		}()
		cloudDebug = func(w http.ResponseWriter, r *http.Request) {
			cc := cloudCli.Load()
			if cc == nil {
				httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
				return
			}
			out := map[string]any{"region": cfg.OpenStack.Region}
			if sid := r.URL.Query().Get("serverId"); sid != "" {
				if srv, err := cc.GetServer(r.Context(), sid); err == nil {
					out["server"] = srv
				} else {
					out["serverErr"] = err.Error()
				}
				httpx.OK(w, out)
				return
			}
			full := r.URL.Query().Get("full") == "1"
			if fs, err := cc.ListFlavors(r.Context()); err == nil {
				out["flavors"] = len(fs)
				if full {
					out["flavorList"] = fs
				}
			} else {
				out["flavorsErr"] = err.Error()
			}
			if full {
				if ims, err := cc.ListImages(r.Context()); err == nil {
					out["imageList"] = ims
				} else {
					out["imagesErr"] = err.Error()
				}
			}
			if ns, err := cc.ListNetworks(r.Context()); err == nil {
				out["networks"] = len(ns)
			} else {
				out["networksErr"] = err.Error()
			}
			if g, err := metrics.New(cc); err == nil {
				out["gnocchiOK"] = g.Ping(r.Context()) == nil
			} else {
				out["gnocchiErr"] = err.Error()
			}
			httpx.OK(w, out)
		}
	}

	// Scheduled jobs: the charge cron + the gnocchi metrics
	// ingestion, each guarded by a distributed lock so it runs once across the fleet. The charge
	// step reads the PostgreSQL cache (cloudResource/gnocchiMetrics/pricePlan); the metrics job
	// hits the live cloud per ExternalService. Wired always, STARTED only when
	// STRATOS_JOBS_SCHEDULER_ENABLED=true — so a plain deploy never charges bills until the
	// gated live rating run turns it on.
	// Admin handler (needs esSvc + region + the cloud-client factory for cloud-admin live reads).
	adminH := admin.NewHandler(admin.NewRepo(pg), catalog.NewRepo(pg), users, addFundsSvc, pricingRepo, billingRepo, cloudRepo, platformconfig.NewRepo(pg), auditSvc, esSvc, cfg.OpenStack.Region, client.New, cfg.Auth.Admin.IssuerURI, cfg.Auth.Admin.ClientID)
	// Public Admin API (/admin-api/v1 — SigV4 hmac_keys or the admin-api OIDC realm).
	adminAPIH := adminapi.NewHandler(pg, orgRepo, users, esSvc, auditSvc, cfg.Auth.AdminAPI.IssuerURI, cfg.Auth.AdminAPI.ClientID)
	// MCP endpoint (/mcp): client toolset for clients-realm JWTs, admin toolset for
	// admin-realm JWTs or `Bearer pk.sk` hmac api keys. Tools dispatch in-process
	// through the app router (root wired in server.AppRouter).
	mcpH := mcpsrv.New(log, hmacLookup, cfg.Auth.Main.IssuerURI, cfg.Auth.Admin.IssuerURI, cfg.Self.APIBaseURL)
	metricsRepo := metrics.NewRepo(pg)
	chargeJob := billingjob.New(billingjob.Deps{
		Billing:          billingRepo,
		ExternalServices: esSvc,
		Projects:         projectRepo,
		Orgs:             orgRepo,
		Pricing:          pricingRepo,
		Engine:           pricing.NewEngine(nil),
		Cloud:            cloudRepo,
		Registry: map[string]billingresource.Provider{
			cloud.TypeServer:       billingresource.NewServerProvider(metricsRepo),
			cloud.TypeVolume:       billingresource.NewVolumeProvider(),
			cloud.TypeFloatingIP:   billingresource.NewFloatingIPProvider(),
			cloud.TypeLoadBalancer: billingresource.NewLoadBalancerProvider(),
		},
	})
	metricsJob := metricsjob.New(projectRepo, cloudRepo, esSvc, metrics.NewService(metricsRepo), log)
	syncJob := syncjob.New(projectRepo, esSvc, cloudRepo, log)
	savingsSvc := billing.NewSavingsService(billingRepo)
	savingsSvc.SetNotifier(mailSvc)
	suspensionJob := billing.NewSuspensionJob(billingRepo, log)
	suspensionJob.SetNotifier(mailSvc)
	suspensionJob.SetAudit(auditSvc) // writes a system audit trail for CREATE/NOTIFY/SUSPEND/UNSUSPEND
	cloudSuspender := billingCloudSuspender{orgs: orgRepo, projects: projectRepo, cloud: cloudRepo, services: esSvc, log: log}
	suspensionJob.SetCloudSuspender(cloudSuspender)
	// ActivationService (billing/activation): the DIRECT activate/suspend/resume driven by
	// the admin status transitions + the public Admin API. Activating a profile provisions
	// each eligible project's memberships then bootstraps it onto the cloud.
	activationSvc := billing.NewActivationService(billingRepo, auditSvc, log)
	activationSvc.SetClouds(cloudSuspender)
	activationSvc.SetNotifier(mailSvc)
	activationSvc.SetLoginURL(cfg.Self.UIBaseURL) // {{loginUrl}} in billing_profile_validated
	activationSvc.SetActivateProjects(func(ctx context.Context, bpID string) error {
		orgs, err := orgRepo.FindAllByBillingProfileID(ctx, bpID)
		if err != nil || len(orgs) == 0 {
			return err
		}
		orgIDs := make([]string, 0, len(orgs))
		for i := range orgs {
			orgIDs = append(orgIDs, orgs[i].ID)
		}
		members, err := orgRepo.Members(ctx, orgs[0].ID)
		if err != nil {
			return err
		}
		memberships := make([]project.Membership, 0, len(members))
		for i := range members {
			role := project.RoleMember
			if members[i].Role() == "OWNER" {
				role = project.RoleOwner
			}
			memberships = append(memberships, project.Membership{Sub: members[i].Sub, Role: role})
		}
		projects, err := projectRepo.AllByBillingProfile(ctx, bpID, orgIDs)
		if err != nil {
			return err
		}
		for pi := range projects {
			p := &projects[pi]
			if p.IsEnabled() {
				continue
			}
			p.Memberships = memberships
			if err := projectRepo.Save(ctx, p); err != nil {
				log.Error("activate: save memberships", "project", p.ID, "err", err)
				continue
			}
			if err := projectH.EnableAndBootstrap(ctx, p); err != nil {
				log.Error("activate: bootstrap", "project", p.ID, "err", err)
			}
		}
		return nil
	})
	adminH.SetActivation(activationSvc)
	// On admin user-create, loop the given project IDs and send a project invite for each (best-effort).
	adminH.SetInviteToProject(inviteH.InviteToProject)
	// Live per-project cloud legs for the admin project + cloud-resource mutations:
	// nova pause/unpause, scoped sync, keystone bootstrap.
	adminH.SetProjectCloudOps(&admin.ProjectCloudOps{
		PauseServers: func(ctx context.Context, projectID string, pause bool) error {
			p, err := projectRepo.FindByID(ctx, projectID)
			if err != nil {
				return err
			}
			if p == nil {
				return fmt.Errorf("project %s not found", projectID)
			}
			cloudSuspender.pauseProjectServers(ctx, p, pause)
			return nil
		},
		Sync: syncJob.SyncOne,
		Bootstrap: func(ctx context.Context, projectID, esID, adoptExternalProjectID string) error {
			err := func() error {
				p, err := projectSvc.GetProjectByID(ctx, projectID)
				if err != nil {
					return err
				}
				es, err := esSvc.Get(ctx, esID)
				if err != nil {
					return err
				}
				if es == nil {
					return fmt.Errorf("external service %s not found", esID)
				}
				return projectH.BootstrapOnto(ctx, p, es, adoptExternalProjectID)
			}()
			if err != nil {
				// The admin handler wraps this into a generic 500 — log the real cause here.
				log.Error("admin project bootstrap", "project", projectID, "es", esID, "err", err)
			}
			return err
		},
		// Light pre-check before scheduling a project for deletion: the project must resolve (a
		// deeper "no locked resources" gate could go here later).
		CanDelete: func(ctx context.Context, projectID string) error {
			p, err := projectSvc.GetProjectByID(ctx, projectID)
			if err != nil {
				return err
			}
			if p == nil {
				return fmt.Errorf("project %s not found", projectID)
			}
			return nil
		},
		// Delete the project now: an async cloud cascade (fire-and-forget) — delete the project's
		// cloud resources + tenant, then mark it DELETED. Detached context so it outlives the request.
		Teardown: func(_ context.Context, projectID string) error {
			go func() {
				if err := projectH.TeardownProject(context.Background(), projectID); err != nil {
					log.Error("admin project teardown", "project", projectID, "err", err)
				}
			}()
			return nil
		},
	})
	adminAPIH.SetActivation(activationSvc)
	adminAPIH.SetBootstrapProject(func(ctx context.Context, projectID string) error {
		p, err := projectSvc.GetProjectByID(ctx, projectID)
		if err != nil {
			return err
		}
		return projectH.EnableAndBootstrap(ctx, p)
	})
	payService.SetReviewer(suspensionJob) // auto-resume a suspended profile when a bill is paid
	// bindOrderPaid flips an order PAID only when it belongs to the PAYING profile AND the settled
	// gross covers net+tax — a foreign txn.OrderID or a short settlement is a silent no-op (never
	// errors the payment). Shared by add-funds + collect; the write filters on {_id, billingProfileId}.
	bindOrderPaid := func(ctx context.Context, orderID, billingProfileID string, gross decimal.Decimal, status string) error {
		if orderID == "" {
			return nil
		}
		o, err := orderRepo.Get(ctx, orderID)
		if err != nil {
			return err
		}
		if !order.ShouldMarkPaid(o, billingProfileID, gross) {
			return nil
		}
		return orderRepo.UpdateStatusForProfile(ctx, orderID, billingProfileID, status)
	}
	// Side-effects when a deposit or collect transaction succeeds: suspension re-review, the
	// order PAID flip, and the deposit-targets-a-bill settle leg.
	addFundsSvc.SetReviewer(suspensionJob)
	addFundsSvc.SetOrderStatusUpdater(bindOrderPaid)
	addFundsSvc.SetBillPayer(func(ctx context.Context, profile *billing.BillingProfile, billID string) error {
		_, err := payService.PayBillWithCredits(ctx, profile, billID, time.Now().UTC())
		return err
	})
	collectSvc.SetReviewer(suspensionJob)
	collectSvc.SetOrderStatusUpdater(bindOrderPaid)
	// Monthly bill finalization: finalize each profile's previous-month OPEN bill (OPEN→SENT/PAID) so
	// the collect + dunning crons have SENT bills to act on.
	billSendSvc := billing.NewBillSendService(billingRepo, pricingRepo)
	billSendSvc.SetNotifier(mailSvc)
	// Project-deletion job: cascade-delete a scheduled project's cloud resources (live
	// WriteService.Delete per resource) then remove the doc. ⚠ performs LIVE cloud DELETEs.
	deletionJob := project.NewDeletionJob(projectRepo, projectCloudDeleter{cloudRepo: cloudRepo, client: func() *client.Client { return cloudCli.Load() }}, log)
	sched := scheduler.New(lock.New(pg))
	// Charge dispatch: in-process loop by default; RabbitMQ fan-out (one message per ACTIVE
	// profile → the per-pod consumer) when STRATOS_JOBS_RABBIT_FANOUT=true and the broker is up.
	chargeDispatch := func(ctx context.Context, timeUnit string) error {
		if cfg.Jobs.RabbitFanout {
			if rc := rabbit.Load(); rc != nil {
				n, err := chargefanout.Publish(ctx, rc, chargeJob, timeUnit)
				if err == nil {
					log.Info("charge fan-out published", "timeUnit", timeUnit, "count", n)
				}
				return err
			}
			log.Warn("rabbit fan-out on but broker not connected — charging in-process this tick", "timeUnit", timeUnit)
		}
		return chargeJob.Charge(ctx, timeUnit, time.Now().UTC())
	}
	registerJobs(sched, chargeDispatch, metricsJob, syncJob, savingsSvc, suspensionJob, collectSvc, txnScanner, deletionJob, billSendSvc, log)
	// When fan-out is on, run a consumer in this pod (waits for the broker, then drains the
	// charge queue). Background so startup never blocks on the broker.
	if cfg.Jobs.RabbitFanout {
		go startChargeConsumer(&rabbit, chargeJob, log)
	}
	if cfg.Jobs.SchedulerEnabled {
		log.Warn("scheduled jobs ENABLED (charge cron + cloud metrics) — bills will be charged on a timer")
		sched.Start()
		defer sched.Stop()
	} else {
		log.Info("scheduled jobs are wired but NOT started (set STRATOS_JOBS_SCHEDULER_ENABLED=true to enable)")
	}
	// On-demand triggers (mgmt port) for the golden run — deterministic instead of cron-timed:
	// run-sync (populate the cache) → run-metrics (ingest gnocchi) → run-charge. Exposed when the
	// scheduler is on OR debug-triggers is on, so a deploy can be driven manually while the timed
	// crons stay dormant (no automatic bill charging).
	var jobsDebug map[string]http.HandlerFunc
	if cfg.Jobs.SchedulerEnabled || cfg.Jobs.DebugTriggers {
		log.Warn("job debug triggers ENABLED on the mgmt port (/debug/run-{sync,metrics,charge})")
		jobsDebug = map[string]http.HandlerFunc{
			"run-sync": func(w http.ResponseWriter, r *http.Request) {
				n, err := syncJob.Run(r.Context())
				jobResult(w, map[string]any{"synced": n}, err)
			},
			"run-metrics": func(w http.ResponseWriter, r *http.Request) {
				jobResult(w, map[string]any{"ran": "metrics"}, metricsJob.Run(r.Context()))
			},
			"run-charge": func(w http.ResponseWriter, r *http.Request) {
				// ?timeUnit=minute|hour|month — defaults to minute. The charge job filters price-plan
				// rules by the EXACT time unit (an HOUR rule only accrues on an hourly charge), so the
				// golden run can drive whichever cadence a plan's rules use.
				tu := r.URL.Query().Get("timeUnit")
				switch tu {
				case pricing.TimeUnitHour, pricing.TimeUnitMonth, pricing.TimeUnitMinute:
				default:
					tu = pricing.TimeUnitMinute
				}
				jobResult(w, map[string]any{"ran": "charge", "timeUnit": tu}, chargeJob.Charge(r.Context(), tu, time.Now().UTC()))
			},
			"run-savings-expire": func(w http.ResponseWriter, r *http.Request) {
				n, err := savingsSvc.ExpireContracts(r.Context())
				jobResult(w, map[string]any{"ran": "savings-expire", "expired": n}, err)
			},
			"run-expiry-reminders": func(w http.ResponseWriter, r *http.Request) {
				n, err := savingsSvc.SendExpiryReminders(r.Context())
				jobResult(w, map[string]any{"ran": "expiry-reminders", "scheduled": n}, err)
			},
			"run-reminders": func(w http.ResponseWriter, r *http.Request) {
				n, err := savingsSvc.ProcessReminderNotifications(r.Context())
				jobResult(w, map[string]any{"ran": "reminders", "sent": n}, err)
			},
			"run-txn-scan": func(w http.ResponseWriter, r *http.Request) {
				n, err := txnScanner.Scan(r.Context())
				jobResult(w, map[string]any{"ran": "txn-scan", "scanned": n}, err)
			},
			"run-review": func(w http.ResponseWriter, r *http.Request) {
				// re-evaluate every profile's suspension (the ReviewBillingProfile resume path) —
				// deterministic driver for the live suspend/resume drill.
				profiles, err := billingRepo.AllBillingProfiles(r.Context())
				if err != nil {
					jobResult(w, nil, err)
					return
				}
				n := 0
				for i := range profiles {
					if err := suspensionJob.ReviewBillingProfile(r.Context(), &profiles[i]); err == nil {
						n++
					}
				}
				jobResult(w, map[string]any{"ran": "review", "reviewed": n}, nil)
			},
			"run-dunning": func(w http.ResponseWriter, r *http.Request) {
				n, err := suspensionJob.ExecuteDunning(r.Context())
				jobResult(w, map[string]any{"ran": "dunning", "suspended": n}, err)
			},
			"run-send-bills": func(w http.ResponseWriter, r *http.Request) {
				n, err := billSendSvc.SendAllBills(r.Context(), time.Now().UTC())
				jobResult(w, map[string]any{"ran": "send-bills", "finalized": n}, err)
			},
			"run-collect": func(w http.ResponseWriter, r *http.Request) {
				n, err := collectSvc.CollectAll(r.Context())
				jobResult(w, map[string]any{"ran": "collect", "paid": n}, err)
			},
			"run-project-deletion": func(w http.ResponseWriter, r *http.Request) {
				n, err := deletionJob.ExecuteAll(r.Context())
				jobResult(w, map[string]any{"ran": "project-deletion", "deleted": n}, err)
			},
			// sse-emit pushes a synthetic event to the SSE pool (the os-notification source will
			// do this). ?projectId=&type= → any open /api/v1/events/{projectId} stream receives it.
			"sse-emit": func(w http.ResponseWriter, r *http.Request) {
				evType := r.URL.Query().Get("type")
				if evType == "" {
					evType = "cloud_resource"
				}
				ssePool.Notify(sse.SseData{
					Type:      evType,
					ProjectID: r.URL.Query().Get("projectId"),
					UserID:    r.URL.Query().Get("userId"),
					Data:      map[string]any{"synthetic": true},
				})
				jobResult(w, map[string]any{"ran": "sse-emit", "subscribers": ssePool.Count()}, nil)
			},
			// send-test-mail renders a system message template + sends it via the configured SMTP
			// gateway (proves the message→render→shell→send chain live). ?to=&key= override.
			"send-test-mail": func(w http.ResponseWriter, r *http.Request) {
				to := r.URL.Query().Get("to")
				if to == "" {
					to = os.Getenv("STRATOS_MAIL_FROM")
				}
				key := r.URL.Query().Get("key")
				if key == "" {
					key = "notify_customer_is_resumed"
				}
				err := mailSvc.SendTemplate(r.Context(), key, []string{to}, map[string]any{
					"fullName": "Stratos Test User", "balance": "0.00", "currency": "USD",
				})
				jobResult(w, map[string]any{"ran": "send-test-mail", "to": to, "template": key}, err)
			},
			"run-charge-fanout": func(w http.ResponseWriter, r *http.Request) {
				rc := rabbit.Load()
				if rc == nil {
					jobResult(w, nil, errors.New("rabbitmq not connected"))
					return
				}
				n, err := chargefanout.Publish(r.Context(), rc, chargeJob, pricing.TimeUnitMinute)
				jobResult(w, map[string]any{"ran": "charge-fanout", "published": n}, err)
			},
			// rabbit-selftest proves Publish+Consume work live against the cluster broker (an
			// isolated queue round-trip) without needing any billing data seeded.
			"rabbit-selftest": func(w http.ResponseWriter, r *http.Request) {
				rc := rabbit.Load()
				if rc == nil {
					jobResult(w, nil, errors.New("rabbitmq not connected"))
					return
				}
				got := make(chan string, 1)
				stop, err := rc.Consume("stratos.selftest", func(b []byte) error { got <- string(b); return nil })
				if err != nil {
					jobResult(w, nil, err)
					return
				}
				defer func() { _ = stop() }()
				if err := rc.Publish(r.Context(), "stratos.selftest", []byte("ping")); err != nil {
					jobResult(w, nil, err)
					return
				}
				select {
				case b := <-got:
					jobResult(w, map[string]any{"roundtrip": b}, nil)
				case <-time.After(5 * time.Second):
					jobResult(w, nil, errors.New("selftest timeout"))
				}
			},
		}
		// gen-hmac-key mints an Admin-API SigV4 key pair on the operator mgmt port and is
		// UNAUTHENTICATED, so it is gated on DebugTriggers ONLY — deliberately NOT the scheduler. A
		// production billing deploy must set SchedulerEnabled=true to run the charge crons; that must
		// not also expose unauthenticated key-minting. The secret is returned ONCE and stored verbatim.
		if cfg.Jobs.DebugTriggers {
			jobsDebug["gen-hmac-key"] = func(w http.ResponseWriter, r *http.Request) {
				m := md5.Sum([]byte(uuid.NewString()))
				s := sha1.Sum([]byte(uuid.NewString()))
				id := "pk" + hex.EncodeToString(m[:])
				secret := "sk" + hex.EncodeToString(s[:])
				_, err := pg.C("hmac_keys").InsertOne(r.Context(), pgdoc.M{
					"_id": id, "secretKey": secret,
					"description": r.URL.Query().Get("description"),
					"createdAt":   time.Now().UTC(),
					"purpose":     "admin-api",
				})
				jobResult(w, map[string]any{"id": id, "secretKey": secret}, err)
			}
		}
	}

	h := health.New(
		health.Checker{Name: "postgres", Check: pg.Ping},
		health.Checker{Name: "rabbit", Check: func(context.Context) error {
			if ac := rabbit.Load(); ac != nil && ac.Healthy() {
				return nil
			}
			return errors.New("rabbitmq not connected")
		}},
	)

	// CORS allowed origins for the browser FE → api cross-origin calls (the UI + admin SPAs run
	// on different subdomains). Derived from the self UI/admin URLs (origin only — strip path) +
	// an optional STRATOS_CORS_ALLOWED_ORIGINS comma list.
	corsOrigins := originsOf(cfg.Self.UIBaseURL, cfg.Self.AdminBaseURL)
	for _, o := range strings.Split(os.Getenv("STRATOS_CORS_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			corsOrigins = append(corsOrigins, o)
		}
	}
	log.Info("cors allowed origins", "origins", corsOrigins)

	// Operator job triggers (/api/v1/admin/job/*) — reuse the same in-process job objects the mgmt
	// /debug/run-* triggers use; the gated/per-id ones degrade to 202 inside the handler.
	jobH := job.NewHandler(job.Runners{
		Charge:           func(ctx context.Context, tu string) error { return chargeJob.Charge(ctx, tu, time.Now().UTC()) },
		Metrics:          metricsJob.Run,
		ServicesSync:     func(ctx context.Context) error { _, err := syncJob.Run(ctx); return err },
		Collect:          func(ctx context.Context) error { _, err := collectSvc.CollectAll(ctx); return err },
		SavingsExpire:    func(ctx context.Context) error { _, err := savingsSvc.ExpireContracts(ctx); return err },
		ReminderSchedule: func(ctx context.Context) error { _, err := savingsSvc.SendExpiryReminders(ctx); return err },
		ReminderDispatch: func(ctx context.Context) error { _, err := savingsSvc.ProcessReminderNotifications(ctx); return err },
		TransactionScan:  func(ctx context.Context) error { _, err := txnScanner.Scan(ctx); return err },
		CloudResourceExists: func(ctx context.Context, svcID, extID string) (bool, error) {
			c, err := cloudRepo.FindByServiceIDAndExternalID(ctx, svcID, extID)
			return c != nil, err
		},
	})

	appSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           server.AppRouter(log, corsOrigins, authn, acct, orgH, roleH, orgAuditH, billingH, billingCfgH, projectH, pcfgH, featureH, promotionH, affiliateH, catalogH, orderH, inviteH, adminH, adminAPIH, sseH, notiH, jobH, mcpH),
		ReadHeaderTimeout: 10 * time.Second,
	}
	mgmtSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Management.Port),
		Handler:           server.MgmtRouter(h, cloudDebug, jobsDebug),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go serve(appSrv, "app", log, errCh)
	go serve(mgmtSrv, "mgmt", log, errCh)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
	}

	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = appSrv.Shutdown(sctx)
	_ = mgmtSrv.Shutdown(sctx)
	return nil
}

// startChargeConsumer waits for the RabbitMQ broker to come up (background-connected) then
// subscribes a charge consumer on this pod. Bounded wait so a missing broker just logs.
func startChargeConsumer(rabbit *atomic.Pointer[amqp.Client], charger chargefanout.Charger, log *slog.Logger) {
	for i := 0; i < 60; i++ {
		if rc := rabbit.Load(); rc != nil && rc.Healthy() {
			if _, err := chargefanout.StartConsumer(rc, charger, log); err != nil {
				log.Error("charge fan-out consumer", "err", err)
				return
			}
			log.Warn("charge fan-out consumer started", "queue", chargefanout.Queue)
			return
		}
		time.Sleep(2 * time.Second)
	}
	log.Error("charge fan-out consumer: broker not up after 120s")
}

// registerJobs wires the charge cron (minutely/hourly/monthly) + the gnocchi metrics job
// with the verified cron specs + lock names/durations. The charge crons
// share AtMostFor 5m / AtLeastFor 30s; the metrics job uses AtMostFor 10m / AtLeastFor 30s.
func registerJobs(sched *scheduler.Scheduler, chargeDispatch func(context.Context, string) error, metrics *metricsjob.Job, sync *syncjob.Job, savings *billing.SavingsService, suspension *billing.SuspensionJob, collect *payment.CollectService, txnScan *payment.TransactionScanner, deletion *project.DeletionJob, billSend *billing.BillSendService, log *slog.Logger) {
	chargeFn := func(timeUnit string) func(context.Context) {
		return func(ctx context.Context) {
			if err := chargeDispatch(ctx, timeUnit); err != nil {
				log.Error("charge job", "timeUnit", timeUnit, "err", err)
			}
		}
	}
	jobs := []scheduler.Job{
		{Name: "minutelyCharge", Spec: scheduler.MinutelyChargeSpec, AtMostFor: 5 * time.Minute, AtLeastFor: 30 * time.Second, Fn: chargeFn(pricing.TimeUnitMinute)},
		{Name: "hourlyCharge", Spec: scheduler.HourlyChargeSpec, AtMostFor: 5 * time.Minute, AtLeastFor: 30 * time.Second, Fn: chargeFn(pricing.TimeUnitHour)},
		{Name: "monthlyCharge", Spec: scheduler.MonthlyChargeSpec, AtMostFor: 5 * time.Minute, AtLeastFor: 30 * time.Second, Fn: chargeFn(pricing.TimeUnitMonth)},
		{Name: "gnocchiMetricsFetch", Spec: scheduler.GnocchiMetricsSpec, AtMostFor: 10 * time.Minute, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if err := metrics.Run(ctx); err != nil {
				log.Error("gnocchi metrics job", "err", err)
			}
		}},
		{Name: "savingsContractExpiration", Spec: scheduler.SavingsExpirationSpec, AtMostFor: 10 * time.Minute, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := savings.ExpireContracts(ctx); err != nil {
				log.Error("savings contract expiration job", "err", err)
			} else if n > 0 {
				log.Info("savings contracts expired", "count", n)
			}
		}},
		{Name: "savingsContractExpiryReminders", Spec: scheduler.SavingsExpiryRemindersSpec, AtMostFor: 10 * time.Minute, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := savings.SendExpiryReminders(ctx); err != nil {
				log.Error("savings expiry reminder scheduling job", "err", err)
			} else if n > 0 {
				log.Info("savings expiry reminders scheduled", "count", n)
			}
		}},
		{Name: "reminderNotifications", Spec: scheduler.ReminderNotificationsSpec, AtMostFor: 10 * time.Minute, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := savings.ProcessReminderNotifications(ctx); err != nil {
				log.Error("reminder notifications dispatch job", "err", err)
			} else if n > 0 {
				log.Info("reminder notifications sent", "count", n)
			}
		}},
		{Name: "paymentGatewayTransactionScanning", Spec: scheduler.TransactionScanSpec, AtMostFor: 10 * time.Minute, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := txnScan.Scan(ctx); err != nil {
				log.Error("transaction scan job", "err", err)
			} else if n > 0 {
				log.Info("pending transactions scanned", "count", n)
			}
		}},
		{Name: "autoSuspensionJob", Spec: scheduler.AutoSuspensionSpec, AtMostFor: 20 * time.Minute, AtLeastFor: 5 * time.Minute, Fn: func(ctx context.Context) {
			if n, err := suspension.ExecuteDunning(ctx); err != nil {
				log.Error("auto suspension job", "err", err)
			} else if n > 0 {
				log.Info("billing profiles auto-suspended", "count", n)
			}
		}},
		{Name: "monthlyBill", Spec: scheduler.MonthlyBillSpec, AtMostFor: time.Hour, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := billSend.SendAllBills(ctx, time.Now().UTC()); err != nil {
				log.Error("monthly bill (sendBills) job", "err", err)
			} else if n > 0 {
				log.Info("bills finalized (OPEN→SENT/PAID)", "count", n)
			}
		}},
		{Name: "monthlyCollect", Spec: scheduler.MonthlyCollectSpec, AtMostFor: time.Hour, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := collect.CollectAll(ctx); err != nil {
				log.Error("monthly collect job", "err", err)
			} else if n > 0 {
				log.Info("bills collected via card", "paid", n)
			}
		}},
		{Name: "servicesSync", Spec: scheduler.ServicesSyncSpec, AtMostFor: 10 * time.Minute, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := sync.Run(ctx); err != nil {
				log.Error("services sync job", "err", err)
			} else if n > 0 {
				log.Info("cloud resources synced", "count", n)
			}
		}},
		{Name: "executeProjectDeletion", Spec: scheduler.ProjectDeletionSpec, AtMostFor: time.Minute, AtLeastFor: 30 * time.Second, Fn: func(ctx context.Context) {
			if n, err := deletion.ExecuteAll(ctx); err != nil {
				log.Error("project deletion job", "err", err)
			} else if n > 0 {
				log.Info("projects deleted", "count", n)
			}
		}},
	}
	for _, j := range jobs {
		if err := sched.Register(j); err != nil {
			log.Error("register scheduled job", "name", j.Name, "err", err)
		}
	}
}

// projectCloudDeleter implements project.ResourceDeleter: cascade-delete a project's cloud
// resources via the live CloudClient. ⚠ performs
// LIVE cloud DELETEs — aborts on the first failure so the caller rolls the project back to ENABLED.

// billingCloudSuspender is the live OpenStack suspend/resume for a billing profile:
// resolve the profile's projects (via its orgs — greenfield projects inherit the org's bp), and
// per CLOUD service nova-PAUSE/UNPAUSE each cached SERVER with a tenant-scoped client + flip the
// project ENABLED↔DISABLED. Per-project/server errors are logged and skipped (best-effort).
// (keystone member/API-user disable legs are no-ops here — the bootstrap creates no
// per-customer keystone users yet.)
type billingCloudSuspender struct {
	orgs     *org.Repo
	projects *project.Repo
	cloud    *cloud.Repo
	services *externalservice.Service
	log      *slog.Logger
}

func (s billingCloudSuspender) SuspendBillingProfileClouds(ctx context.Context, bpID string) error {
	return s.forEachProjectServer(ctx, bpID, true)
}

func (s billingCloudSuspender) ResumeBillingProfileClouds(ctx context.Context, bpID string) error {
	return s.forEachProjectServer(ctx, bpID, false)
}

func (s billingCloudSuspender) forEachProjectServer(ctx context.Context, bpID string, pause bool) error {
	orgs, err := s.orgs.FindAllByBillingProfileID(ctx, bpID)
	if err != nil {
		return err
	}
	orgIDs := make([]string, 0, len(orgs))
	for i := range orgs {
		orgIDs = append(orgIDs, orgs[i].ID)
	}
	projects, err := s.projects.AllByBillingProfile(ctx, bpID, orgIDs)
	if err != nil {
		return err
	}
	for pi := range projects {
		p := &projects[pi]
		s.pauseProjectServers(ctx, p, pause)
		// suspend/resume: ENABLED → DISABLED on suspend, back on resume.
		if pause && p.IsEnabled() {
			p.Status = project.StatusDisabled
			_ = s.projects.Save(ctx, p)
		} else if !pause && p.IsDisabled() {
			p.Status = project.StatusEnabled
			_ = s.projects.Save(ctx, p)
		}
	}
	return nil
}

// pauseProjectServers is the CLOUD-only per-project leg: nova PAUSE/UNPAUSE every cached SERVER
// through a tenant-scoped client. Best-effort — per-server/-service failures are logged and
// skipped. Status flips stay with the callers (bp walk above; admin status-update handler).
func (s billingCloudSuspender) pauseProjectServers(ctx context.Context, p *project.Project, pause bool) {
	for _, serviceID := range p.ServiceIDs() {
		es, err := s.services.Get(ctx, serviceID)
		if err != nil || es == nil || es.Type != externalservice.TypeCloud {
			continue
		}
		extProjID := p.ExternalProjectID(es.ID)
		if extProjID == "" {
			continue
		}
		for _, region := range es.RegionNames() {
			cc, err := client.New(ctx, es.ClientConfigForProject(region, extProjID))
			if err != nil {
				s.log.Error("suspender: tenant client", "project", p.ID, "region", region, "err", err)
				continue
			}
			servers, err := s.cloud.FindByProjectAndType(ctx, p.ID, cloud.TypeServer)
			if err != nil {
				s.log.Error("suspender: list cached servers", "project", p.ID, "err", err)
				continue
			}
			for si := range servers {
				var aerr error
				if pause {
					aerr = cc.PauseServer(ctx, servers[si].ExternalID)
				} else {
					aerr = cc.UnpauseServer(ctx, servers[si].ExternalID)
				}
				if aerr != nil {
					s.log.Error("suspender: server action", "server", servers[si].ExternalID, "pause", pause, "err", aerr)
				}
			}
		}
	}
}

type projectCloudDeleter struct {
	cloudRepo *cloud.Repo
	client    func() *client.Client
}

func (d projectCloudDeleter) DeleteProjectResources(ctx context.Context, projectID string) error {
	cc := d.client()
	if cc == nil {
		return errors.New("cloud client not ready")
	}
	ws := providers.NewWriteService(cc, d.cloudRepo)
	resources, err := d.cloudRepo.FindAllByProjectID(ctx, projectID)
	if err != nil {
		return err
	}
	project.SortCloudResourcesForDeletion(resources)
	remaining := resources
	var lastErr error
	for sweep := 0; sweep < 3 && len(remaining) > 0; sweep++ {
		if sweep > 0 {
			// Nova server deletion is asynchronous: the boot volume of a
			// volume-backed server stays in-use until delete_on_termination
			// finishes (routinely 10-30s). Back-to-back sweeps would burn all
			// retries inside that window and fail the deletion — which the
			// deletion job then treats as canceled (project flips back ENABLED).
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Second):
			}
		}
		stillLeft := make([]cloud.CloudResource, 0, len(remaining))
		for i := range remaining {
			if err := ws.Delete(ctx, remaining[i].ServiceID, remaining[i].ExternalID); err != nil {
				lastErr = err
				stillLeft = append(stillLeft, remaining[i])
			}
		}
		remaining = stillLeft
	}
	if len(remaining) > 0 {
		return fmt.Errorf("scheduled project deletion left %d resource(s): %w", len(remaining), lastErr)
	}
	return nil
}

// jobResult writes a debug-trigger response: the result map on success, or a 500 with the error.
func jobResult(w http.ResponseWriter, out map[string]any, err error) {
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, out)
}

func serve(s *http.Server, name string, log *slog.Logger, errCh chan<- error) {
	log.Info("listening", "server", name, "addr", s.Addr)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s server: %w", name, err)
	}
}

// maintainRabbit keeps a live broker connection: it (re)connects whenever the
// current client is missing or unhealthy, so readiness self-heals after a
// dropped connection instead of staying DOWN (which would deadlock rollouts).
func maintainRabbit(ctx context.Context, cfg *config.Config, log *slog.Logger, dst *atomic.Pointer[amqp.Client]) {
	misses := 0
	for {
		if c := dst.Load(); c == nil || !c.Healthy() {
			if c != nil {
				_ = c.Close()
			}
			ac, err := amqp.Connect(cfg.Rabbit.Host, cfg.Rabbit.Port, cfg.Rabbit.Username, cfg.Rabbit.Password)
			if err == nil {
				dst.Store(ac)
				log.Info("rabbitmq connected", "host", cfg.Rabbit.Host, "port", cfg.Rabbit.Port)
				misses = 0
			} else {
				if misses%5 == 0 {
					log.Warn("rabbitmq not reachable, will retry", "err", err)
				}
				misses++
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func newLogger(level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch level {
	case "DEBUG", "TRACE":
		lvl = slog.LevelDebug
	case "WARN", "WARNING":
		lvl = slog.LevelWarn
	case "ERROR":
		lvl = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

// originsOf returns the scheme://host origin of each non-empty base URL (path/query stripped),
// for the CORS allow-list. A base URL like "https://stratos-admin.menlo.ai/stratos_admin" → the
// origin "https://stratos-admin.menlo.ai". Unparseable/empty inputs are skipped.
func originsOf(urls ...string) []string {
	out := []string{}
	for _, raw := range urls {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		out = append(out, u.Scheme+"://"+u.Host)
	}
	return out
}

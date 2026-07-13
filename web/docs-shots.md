# Docs screenshot backlog

## admin-billing

- [x] `/docs-img/billing-configuration-activation.png` — Billing configuration page, Activation tab, showing the auto-activation switch and the constraint selectors (KYC, payment method, deposit, validation) — page: `administrators-manual/account-activation.md`
- [x] `/docs-img/validations-queue.png` — Validations page listing pending billing profile validations with Approve and Reject buttons — page: `administrators-manual/account-activation.md`
- [x] `/docs-img/billing-profile-activate.png` — Billing profile detail page showing the Activate action on a non-active profile — page: `administrators-manual/account-activation.md`
- [x] `/docs-img/price-plans-create.png` — Price plans page with the create dialog open, showing name, access mode and enabled fields — page: `administrators-manual/billing/price-plans.md`
- [x] `/docs-img/price-plan-rules.png` — Price plan detail page listing pricing rules with resource type, priced attribute and time unit columns — page: `platform-admin/billing/price-plans.md` (captured on the live "Public rate card" plan, 2026-07-09)
- [x] `/docs-img/price-plan-rule-create.png` — Rule creation dialog with a resource type selected and its attribute list expanded — page: `platform-admin/billing/price-plans.md` (Add-rule dialog, resource type `instance`, attributes expanded; dialog cancelled, no write)
- [x] `/docs-img/billing-configuration-base-currency.png` — Billing configuration page, Business details tab, with the Base currency dropdown open — page: `administrators-manual/billing/currencies.md`
- [x] `/docs-img/taxes-add-tax.png` — Taxes page with the add-tax dialog open, showing name, rate and country fields — page: `administrators-manual/billing/tax-rules.md`
- [x] `/docs-img/billing-configuration-invoice-gateway.png` — Billing configuration page, Business details tab, showing the Invoice gateway selector — page: `administrators-manual/billing/configure-invoicing.md`
- [x] `/docs-img/suspension-balance.png` — Billing configuration Settings tab with Automatic suspension enabled, type BALANCE, showing notification limits and the suspend-at balance — page: `administrators-manual/billing/automated-suspension.md`
- [x] `/docs-img/suspension-due-date.png` — Billing configuration Settings tab with Automatic suspension type DUE_DATE, showing day-based notification limits and the suspend-at days — page: `administrators-manual/billing/automated-suspension.md`
- [x] `/docs-img/provisioning-promotional-credits.png` — Billing configuration Activation tab, Provisioning promotional credits section with a credit row (amount and days valid) filled in — page: `administrators-manual/billing/promotional-credits.md`
- [x] `/docs-img/savings-plan-targets.png` — Savings plan creation form showing the target resource selection and duration — page: `administrators-manual/billing/savings-plans.md`
- [x] `/docs-img/savings-plan-discount-tiers.png` — Discount tier rows (start amount and discount percent) for a savings plan schedule — page: `administrators-manual/billing/savings-plans.md`

## admin-openstack-settings

- [x] `/docs-img/add-openstack-cloud-form.png` — Stratos admin: System > Cloud providers > Add provider dialog with the General and Configuration sections filled in
- [skip: no discovered-regions/country view exists in current admin UI] `/docs-img/add-openstack-cloud-regions.png` — Stratos admin: Discovered regions list after a successful Connect, showing region name and country fields
- [x] `/docs-img/enable-disable-services-tab.png` — Stratos admin: cloud provider detail, Services tab with per-region toggles for compute, volumes, load balancer, shares, object store, DNS
- [x] `/docs-img/instance-metrics-feature-toggle.png` — Stratos admin: cloud provider detail, Features section with the instance metrics toggle enabled
- [skip: instance-metrics feature toggle is OFF on live provider; no charts rendered] `/docs-img/instance-metrics-client-charts.png` — client portal: server detail page showing the CPU/memory/network charts rendered from Gnocchi data
- [x] `/docs-img/domain-reseller-provider.png` — Stratos admin: cloud provider form configured with Private visibility and Domain administrator mode for a reseller domain
- [skip: no reseller billing profile exists; profile detail has no reseller flag UI] `/docs-img/domain-reseller-billing-profile.png` — Stratos admin: billing profile detail with the reseller flag enabled and the private provider attached
- [x] `/docs-img/flavor-categories-list.png` — Stratos admin: System > Instances page listing flavor categories with their assigned flavors
- [x] `/docs-img/flavor-categories-client-tabs.png` — client portal: create-server Hardware section showing one tab per flavor category
- [x] `/docs-img/custom-menu-item-form.png` — Stratos admin: System > Custom menu page with the Add menu item form (display name, URL, icon)
- [x] `/docs-img/custom-menu-item-client-sidebar.png` — client portal: sidebar showing the custom menu section with the configured Support link and icon
- [skip: no Keycloak admin console credentials provided] `/docs-img/customize-keycloak-theme-realm.png` — Keycloak admin console: Realm settings > Themes with the custom stratos theme selected as login theme
- [x] `/docs-img/customize-keycloak-theme-login.png` — branded client login page rendered with the custom Keycloak theme
- [removed] `/docs-img/marketing-events-gtm-trigger.png` — conversion-tracking.md page DELETED 2026-07-09 (GTM/dataLayer feature has no frontend implementation); shot obsolete

## client

- [x] `/docs-img/client-portal-overview.png` — Project dashboard after login, with the full sidebar (Compute, Storage, Network, Platform, Billing, Organization) visible — page: `client-manual.md`
- [skip: profile already activated; form only shown before first deposit] `/docs-img/billing-details-form.png` — Billing > Funds page showing the billing details form (name, phone, address, country fields) before first deposit — page: `client-manual/account-activation.md`
- [x] `/docs-img/deposit-funds.png` — Deposit dialog on Billing > Funds with amount entered and the card payment element visible — page: `client-manual/account-activation.md`
- [x] `/docs-img/savings-plans-catalog.png` — Billing > Savings plans page showing the published plans with duration options and "up to X% upfront / no upfront" tier summaries — page: `client-manual/savings-plans.md`
- [skip: no published savings plans; purchase form unreachable] `/docs-img/savings-plans-purchase.png` — Purchase form with duration selected, monthly committed amount filled in, start-month selector and the "Pay upfront" checkbox — page: `client-manual/savings-plans.md`
- [skip: no contracts exist; section is empty duplicate of catalog shot] `/docs-img/savings-contracts-table.png` — Active savings contracts table showing committed/month, discount %, end date, and the Extend/Cancel actions — page: `client-manual/savings-plans.md`
- [x] `/docs-img/invite-member.png` — Organization > Members page with the email invite field and Send invite button, plus the existing-member "Add to project" picker — page: `client-manual/user-invitation.md`
- [skip: needs a live invite token; sending an invite is a write] `/docs-img/join-project-accept.png` — The /join-project screen showing the pending invitation with Accept invite and Decline buttons — page: `client-manual/user-invitation.md`
- [x] `/docs-img/admin-console-first-login.png` — Stratos admin console right after first sign-in on a fresh install — page: `operators-manual/kubernetes-install.md`

## client-first-server

Captured 2026-07-10 on the live `menlo.ai` project (light theme). Dialog/step-card shots are element-scoped (no top-bar/email chrome); the server detail/actions shots are viewport shots of the real `my-first-server` VM that was created and then deleted. Page: `getting-started/first-server.md`.

- [x] `/docs-img/first-server-keypair-create.png` — Compute → Key pairs, Create keypair dialog with a name filled and the "private key shown only once" hint
- [x] `/docs-img/first-server-keypair-private.png` — "Save your private key" one-time dialog with Copy / Download .pem (throwaway generated key, keypair since removed)
- [x] `/docs-img/first-server-servers-header.png` — Servers page header cropped to the title + Create server button
- [x] `/docs-img/first-server-create-image.png` — Create-server step 3 Image table with Ubuntu Server 24.04 LTS selected
- [x] `/docs-img/first-server-create-flavor.png` — Create-server step 4 Flavor table with t3.small selected (general/burstable families visible)
- [x] `/docs-img/first-server-create-network.png` — Create-server step 5 Network with the project network checked and Fixed IP field revealed
- [x] `/docs-img/first-server-create-access.png` — Create-server step 7 Access: Password login (username + password) and the allow-all security group checked (allow-all is used for demo simplicity only — not a recommended default; real servers should use an SSH-only group scoped to the user's IP)
- [x] `/docs-img/first-server-create-name.png` — Create-server step 8 Name with `my-first-server` entered
- [x] `/docs-img/first-server-building.png` — Server detail page for `my-first-server` in the Build state (no addresses yet)
- [x] `/docs-img/first-server-detail.png` — Server detail page for `my-first-server` now Active, showing IP addresses, power buttons and tabs
- [x] `/docs-img/first-server-actions.png` — Server detail with the More actions menu open (Rename, Resize, Rebuild, Rescue, Set password, Console (VNC), Delete)
- [skip: element shot kept catching the sticky top bar; step described in prose] `/docs-img/first-server-create-publicip.png` — Create-server step 6 Public IP with the Assign floating IP toggle on

## client-storage

Storage-guide shots, captured 2026-07-13 against throwaway demo resources (a volume, its snapshot, and a bucket) that were deleted right after.

- [x] `/docs-img/create-volume.png` — The Create volume dialog (name, size in GB, optional type/AZ) — page: `guides/volumes.md`
- [x] `/docs-img/volume-attach.png` — Volume `docs-demo-vol` (ceph-ssd1) attached to the `docs-demo` server, shown on the server detail Volumes tab (VM IP blurred) — page: `guides/volumes.md`
- [x] `/docs-img/volume-snapshot.png` — The Snapshots list showing `docs-demo-snap` (a snapshot of `docs-demo-vol`) — page: `guides/volumes.md`
- [x] `/docs-img/create-bucket.png` — Create bucket dialog showing the Swift/S3-(Ceph) backend picker, globally-unique-name note and object-lock option — page: `guides/object-storage.md`
- [x] `/docs-img/bucket-objects.png` — Bucket object explorer with a folder and two uploaded files, and the Private toggle — page: `guides/object-storage.md`
- [x] `/docs-img/s3-access-keys.png` — S3 access keys page: project credentials + additional scoped keys. **Access key IDs replaced with AWS example values, the secret left masked, and the S3 endpoint hostname blurred (both the Endpoint field and the CLI example) — never publish real keys/endpoints** — page: `guides/object-storage.md`
- [x] `/docs-img/bucket-settings.png` — Bucket settings dialog (General tab: versioning/object-lock/quota) with the General/Website/Access/Lifecycle/Policy tab bar — page: `guides/object-storage.md`

-- Public rate card seed (docs/pricing-rate-card.md). Idempotent:
--   * plan + rules use fixed ids + ON CONFLICT DO NOTHING
--   * billingConfiguration (base currency USD) inserts only when the table is empty
-- Apply (operator, one command):
--   kubectl --context <ctx> -n <ns> exec deploy/stratos-api -- sh -c 'psql "$STRATOS_DB_URL"' < deploy/seed/price-plan-seed.sql
-- Verify afterwards: admin UI -> System -> Price plans, and the provider GPU tab's
-- "Unpriced flavors" list (flavors with a gap bill zero).

CREATE TABLE IF NOT EXISTS "pricePlan" (id text PRIMARY KEY, doc jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS "pricePlanRule" (id text PRIMARY KEY, doc jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS "billingConfiguration" (id text PRIMARY KEY, doc jsonb NOT NULL);

-- Base currency (prices below are USD). Only when no billing configuration exists yet.
INSERT INTO "billingConfiguration" (id, doc)
SELECT '6a4e63e8aa5e5eed10000018', '{"defaultConfiguration": true, "baseCurrency": "USD"}'::jsonb
WHERE NOT EXISTS (SELECT 1 FROM "billingConfiguration");

INSERT INTO "pricePlan" (id, doc) VALUES
('6a4e63e8aa5e5eed10000001', '{"name":"Public rate card","enabled":true,"accessMode":"PUBLIC","serviceProviders":[]}')
ON CONFLICT (id) DO NOTHING;

INSERT INTO "pricePlanRule" (id, doc) VALUES
('6a4e63e8aa5e5eed10000002', '{"name":"Compute:CPU + RAM components","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"vcpus","tiers":[{"value":"0.008"}]},{"attributeName":"ram_gb","tiers":[{"value":"0.004"}]}],"filters":[],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000003', '{"name":"GPU:nvidia-4090 (RTX 4090)","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"gpu_count","tiers":[{"value":"0.65"}]}],"filters":[{"attributeName":"gpu_model","operator":"eq","value":"nvidia-4090"}],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000004', '{"name":"GPU:nvidia-3090 (RTX 3090)","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"gpu_count","tiers":[{"value":"0.43"}]}],"filters":[{"attributeName":"gpu_model","operator":"eq","value":"nvidia-3090"}],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000005', '{"name":"GPU:nvidia-3080ti (RTX 3080 Ti, interpolated)","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"gpu_count","tiers":[{"value":"0.29"}]}],"filters":[{"attributeName":"gpu_model","operator":"eq","value":"nvidia-3080ti"}],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000006', '{"name":"GPU:nvidia-a6000 (RTX A6000 Ampere 48GB)","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"gpu_count","tiers":[{"value":"0.47"}]}],"filters":[{"attributeName":"gpu_model","operator":"eq","value":"nvidia-a6000"}],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000007', '{"name":"GPU:nvidia-pro-6000 (RTX PRO 6000 Blackwell 96GB)","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"gpu_count","tiers":[{"value":"1.99"}]}],"filters":[{"attributeName":"gpu_model","operator":"eq","value":"nvidia-pro-6000"}],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000008', '{"name":"GPU:nvidia-pro-4500 (RTX PRO 4500 Blackwell 32GB, interpolated)","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"gpu_count","tiers":[{"value":"0.69"}]}],"filters":[{"attributeName":"gpu_model","operator":"eq","value":"nvidia-pro-4500"}],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000009', '{"name":"GPU:intel-a60 (placeholder)","timeUnit":"hour","resourceType":"instance","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"gpu_count","tiers":[{"value":"0.15"}]}],"filters":[{"attributeName":"gpu_model","operator":"eq","value":"intel-a60"}],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000010', '{"name":"Public egress:1 TiB free, then $0.01/GB","timeUnit":"hour","resourceType":"instance_traffic","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"outgoing_public_traffic_mb","tiers":[{"from":"0","to":"1048576","value":"0"},{"from":"1048576","value":"0.00001"}]}],"filters":[],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000011', '{"name":"Block storage","timeUnit":"hour","resourceType":"volume","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"size","tiers":[{"value":"0.000137"}]}],"filters":[],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000012', '{"name":"Floating IP","timeUnit":"hour","resourceType":"floating_ip","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"existence","tiers":[{"value":"0.005"}]}],"filters":[],"modifiers":[]}'),
('6a4e63e8aa5e5eed10000013', '{"name":"Load balancer","timeUnit":"hour","resourceType":"load_balancer","pricePlanId":"6a4e63e8aa5e5eed10000001","applyMethod":"ADD_TO_TOTAL","prices":[{"attributeName":"existence","tiers":[{"value":"0.0165"}]}],"filters":[],"modifiers":[]}')
ON CONFLICT (id) DO NOTHING;

-- Sanity read-back
SELECT id, doc->>'name' AS name FROM "pricePlan" WHERE id = '6a4e63e8aa5e5eed10000001';
SELECT count(*) AS rules FROM "pricePlanRule" WHERE doc->>'pricePlanId' = '6a4e63e8aa5e5eed10000001';
SELECT doc->>'baseCurrency' AS base_currency FROM "billingConfiguration" LIMIT 1;

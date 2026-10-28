-- Canonical first-go schema for a new Crane database. Keep every statement
-- safe to repeat; schema evolution is intentionally outside this baseline.
CREATE TABLE IF NOT EXISTS zones (
 id text PRIMARY KEY, provider text NOT NULL, account_id text NOT NULL,
 region text NOT NULL, provider_zone_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT true, lifecycle_state text NOT NULL DEFAULT 'requested',
 UNIQUE(provider, account_id, region, provider_zone_id), UNIQUE(id, provider_zone_id), UNIQUE(id,provider,region)
);
CREATE TABLE IF NOT EXISTS clusters (
 id text PRIMARY KEY,
 api_server_endpoint text NOT NULL DEFAULT '', ca_cert_hash text NOT NULL DEFAULT '',
 kubeadm_version text NOT NULL, kubelet_version text NOT NULL,
 container_runtime text NOT NULL, cni text NOT NULL CHECK(cni = 'cilium'),
 join_configuration jsonb NOT NULL DEFAULT '{}', lifecycle_state text NOT NULL DEFAULT 'requested'
);
CREATE TABLE IF NOT EXISTS cluster_zones (
 cluster_id text NOT NULL REFERENCES clusters(id), zone_id text NOT NULL REFERENCES zones(id),
 lifecycle_state text NOT NULL DEFAULT 'requested' CHECK(lifecycle_state IN ('requested','initializing','ready','failed')),
 access_state text NOT NULL DEFAULT 'pending' CHECK(access_state IN ('pending','approved','revoked')),
 PRIMARY KEY(cluster_id,zone_id)
);
CREATE TABLE IF NOT EXISTS node_pools (
 id text PRIMARY KEY,
 enabled boolean NOT NULL DEFAULT true,
 max_vcpu bigint NOT NULL CHECK(max_vcpu >= 0), max_memory_mib bigint NOT NULL CHECK(max_memory_mib >= 0),
 max_nodes bigint NOT NULL CHECK(max_nodes >= 0)
);
CREATE TABLE IF NOT EXISTS node_groups (
 naming_provider text NOT NULL, development_stage text NOT NULL, naming_region text NOT NULL,
 naming_az text NOT NULL, naming_group text NOT NULL,
 id text PRIMARY KEY, cluster_id text NOT NULL, zone_id text NOT NULL,
 node_pool_id text NOT NULL REFERENCES node_pools(id), capacity_class text NOT NULL,
 provider_zone_id text NOT NULL, target_nodes bigint NOT NULL DEFAULT 0,
 target_revision bigint NOT NULL DEFAULT 0 CHECK(target_revision >= 0),
 min_nodes bigint NOT NULL CHECK(min_nodes >= 0), max_nodes bigint NOT NULL,
 template_revision text NOT NULL, lifecycle_state text NOT NULL DEFAULT 'active',
 vcpu bigint NOT NULL CHECK(vcpu > 0), memory_mib bigint NOT NULL CHECK(memory_mib > 0),
 launch_parameters jsonb NOT NULL,
 CHECK(naming_provider ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$' AND development_stage ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'
 AND naming_region ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$' AND naming_az ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'
 AND naming_group ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'),
 CHECK(id = naming_provider || '-' || development_stage || '-' || naming_region || '-' || naming_az || '-' || naming_group),
 CHECK(max_nodes >= min_nodes), CHECK(target_nodes BETWEEN min_nodes AND max_nodes),
 FOREIGN KEY(cluster_id, zone_id) REFERENCES cluster_zones(cluster_id, zone_id),
 FOREIGN KEY(zone_id, provider_zone_id) REFERENCES zones(id, provider_zone_id),
 FOREIGN KEY(zone_id,naming_provider,naming_region) REFERENCES zones(id,provider,region),
 UNIQUE(id, node_pool_id, zone_id)
);
CREATE TABLE IF NOT EXISTS capacity_requests (
 id text PRIMARY KEY, group_id text NOT NULL REFERENCES node_groups(id), request_id text NOT NULL,
 cluster_id text NOT NULL REFERENCES clusters(id), expected_revision bigint NOT NULL,
 desired_target bigint NOT NULL, template_revision text NOT NULL, result_revision bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(group_id, request_id)
);
CREATE TABLE IF NOT EXISTS host (
 id varchar PRIMARY KEY, role varchar, zone varchar, imageid varchar, state varchar NOT NULL,
 health varchar, createdat timestamptz, updatedat timestamptz, provider varchar, providerid varchar,
 lastseenheartbeat timestamptz, endpoint varchar, port int4, dbname varchar, username varchar,
 secretarn varchar, rdssgid varchar,
 protected boolean NOT NULL DEFAULT false,
 subd_endpoint text NOT NULL DEFAULT '',
 node_group_id text, node_pool_id text, crane_zone text REFERENCES zones(id), operation_id text,
 desired_state_version text NOT NULL DEFAULT '', observed_state_version text NOT NULL DEFAULT '',
 allocatable_vcpu bigint NOT NULL DEFAULT 0 CHECK(allocatable_vcpu >= 0),
 allocatable_memory_mib bigint NOT NULL DEFAULT 0 CHECK(allocatable_memory_mib >= 0),
 reserved_vcpu bigint NOT NULL DEFAULT 0 CHECK(reserved_vcpu >= 0),
 reserved_memory_mib bigint NOT NULL DEFAULT 0 CHECK(reserved_memory_mib >= 0),
 excess_capacity boolean NOT NULL DEFAULT false,
 FOREIGN KEY(node_group_id, node_pool_id, crane_zone) REFERENCES node_groups(id, node_pool_id, zone_id) MATCH FULL,
 CHECK(state IN ('REQUESTED','PROVISIONING','BOOTING','REGISTERING','CONVERGING','READY','DRAINING','TERMINATING','TERMINATED','FAILED','UNHEALTHY'))
);
CREATE TABLE IF NOT EXISTS zone_dominators (
 zone_id text PRIMARY KEY REFERENCES zones(id),
 endpoint text NOT NULL, identity text NOT NULL,
 observability_status text NOT NULL DEFAULT 'configuration_pending'
 CHECK(observability_status IN ('configuration_pending','ready','failed'))
);
-- Secret values are never stored here. Explicit retries resubmit their payload.
CREATE TABLE IF NOT EXISTS host_state_requests (
 host_id text NOT NULL REFERENCES host(id), request_id text NOT NULL, fingerprint text NOT NULL,
 revision bigint NOT NULL CHECK(revision > 0), outcome text NOT NULL DEFAULT 'pending'
 CHECK(outcome IN ('pending','applied','failed','unknown')),
 result jsonb, PRIMARY KEY(host_id,request_id), UNIQUE(host_id,revision)
);
CREATE TABLE IF NOT EXISTS operations (
 id text PRIMARY KEY, kind text NOT NULL CHECK(kind IN ('provision', 'create_zone', 'remove_node')),
 zone_id text NOT NULL REFERENCES zones(id), group_id text REFERENCES node_groups(id),
 host_id text REFERENCES host(id) DEFERRABLE INITIALLY DEFERRED,
 capacity_request_id text REFERENCES capacity_requests(id), target_revision bigint NOT NULL DEFAULT 0,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending', 'leased', 'launching', 'succeeded', 'cancelled', 'failed', 'blocked')),
 provider_token text NOT NULL UNIQUE, parameters jsonb NOT NULL,
 lease_owner text, lease_until timestamptz, fence bigint NOT NULL DEFAULT 0 CHECK(fence >= 0),
 last_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((lease_owner IS NULL) = (lease_until IS NULL)),
 CHECK(kind <> 'provision' OR (group_id IS NOT NULL AND host_id IS NOT NULL AND capacity_request_id IS NOT NULL))
);
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'host_operation' AND conrelid = 'host'::regclass) THEN
  ALTER TABLE host ADD CONSTRAINT host_operation FOREIGN KEY(operation_id) REFERENCES operations(id);
 END IF;
END $$;
CREATE INDEX IF NOT EXISTS operations_claim ON operations(zone_id, kind, state, lease_until);
CREATE OR REPLACE FUNCTION crane_immutable_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.kind,NEW.zone_id,NEW.group_id,NEW.host_id,NEW.capacity_request_id,NEW.target_revision,NEW.provider_token,NEW.parameters)
   IS DISTINCT FROM ROW(OLD.kind,OLD.zone_id,OLD.group_id,OLD.host_id,OLD.capacity_request_id,OLD.target_revision,OLD.provider_token,OLD.parameters) THEN
  RAISE EXCEPTION 'operation intent is immutable';
 END IF;
 RETURN NEW;
END $$;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'immutable_operation' AND tgrelid = 'operations'::regclass AND NOT tgisinternal) THEN
  CREATE TRIGGER immutable_operation BEFORE UPDATE ON operations FOR EACH ROW EXECUTE FUNCTION crane_immutable_operation();
 END IF;
END $$;
CREATE TABLE IF NOT EXISTS infrastructure_reservations (
 id text PRIMARY KEY,
 group_id text NOT NULL REFERENCES node_groups(id), capacity_request_id text NOT NULL REFERENCES capacity_requests(id),
 host_id varchar NOT NULL UNIQUE REFERENCES host(id),
 vcpu bigint NOT NULL CHECK(vcpu > 0), memory_mib bigint NOT NULL CHECK(memory_mib > 0),
 nodes bigint NOT NULL DEFAULT 1 CHECK(nodes = 1),
 state text NOT NULL CHECK(state IN ('reserved', 'committed', 'released')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS infrastructure_reservations_group ON infrastructure_reservations(group_id, state);
CREATE TABLE IF NOT EXISTS zone_bootstrap_operations (
 operation_id text PRIMARY KEY REFERENCES operations(id), zone_id text NOT NULL REFERENCES zones(id),
 phase text NOT NULL DEFAULT 'Requested' CHECK(phase IN ('Requested', 'DatabaseProvisioning', 'DatabaseReady', 'SpireHostProvisioning', 'SpireInstalling', 'SpireValidating',
 'InfrastructureProvisioning', 'ControlPlaneInitializing', 'NetworkInstalling', 'DominatorInstalling', 'Validating', 'Ready', 'Failed'))
);
CREATE UNIQUE INDEX IF NOT EXISTS zone_bootstrap_active ON zone_bootstrap_operations(zone_id) WHERE phase NOT IN ('Ready','Failed');
CREATE TABLE IF NOT EXISTS node_removal_operations (
 operation_id text PRIMARY KEY REFERENCES operations(id), group_id text NOT NULL REFERENCES node_groups(id),
 host_id varchar NOT NULL REFERENCES host(id), node_name text NOT NULL, provider_id text NOT NULL,
 reason text NOT NULL CHECK(reason IN ('autoscaler-delete', 'repair', 'maintenance')),
 preserve_desired_capacity boolean NOT NULL, drain_deadline timestamptz NOT NULL,
 drain_status text NOT NULL DEFAULT 'pending', provider_termination_status text NOT NULL DEFAULT 'pending',
 completed_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS node_removal_exclusive ON node_removal_operations(host_id) WHERE completed_at IS NULL;
CREATE TABLE IF NOT EXISTS actions (
 id serial PRIMARY KEY, hostid varchar NOT NULL, status varchar NOT NULL,
 attempts integer, createdat timestamptz, type varchar NOT NULL,
 updatedat timestamptz, provider varchar, providerid varchar
);
CREATE TABLE IF NOT EXISTS host_problems (
 id serial PRIMARY KEY, host_id varchar NOT NULL REFERENCES host(id),
 problem_type varchar NOT NULL, severity varchar NOT NULL, details jsonb,
 detected_at timestamptz NOT NULL, resolved_at timestamptz
);
CREATE TABLE IF NOT EXISTS host_health_trends (
 id serial PRIMARY KEY, host_id varchar NOT NULL REFERENCES host(id), date date NOT NULL,
 problems_detected integer NOT NULL DEFAULT 0, health_score decimal(5,2) NOT NULL DEFAULT 100.0
);

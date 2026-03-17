-- Databases
CREATE DATABASE crane;
CREATE DATABASE spire;

--dbo.actions definition

-- Drop table

DROP TABLE public.actions;

CREATE TABLE public.actions (
	hostid varchar NOT NULL,
	status varchar NOT NULL,
	attempts int4 NULL,
	createdat timestamptz NULL,
	"type" varchar NOT NULL,
	id int4 NOT NULL,
	updatedat timestamptz NULL,
	provider varchar NULL,
	providerID varchar NULL
);

-- dbo.host definition

-- Drop table

DROP TABLE public.host;

CREATE TABLE public.host (
	id varchar NOT NULL PRIMARY KEY,
	"role" varchar NULL,
	"zone" varchar NULL,
	imageid varchar NULL,
	state varchar NOT NULL,
	health varchar NULL,
	createdat timestamptz NULL,
	updatedat timestamptz NULL,
	provider varchar NULL,
	providerID varchar NULL,
	lastSeenHeartbeat timestamptz NULL,
	endpoint varchar NULL,
	port int4 NULL,
	dbname varchar NULL,
	username varchar NULL,
	secretarn varchar NULL,
	rdssgid varchar NULL
);

-- Problems detected on hosts
CREATE TABLE host_problems (
    id SERIAL PRIMARY KEY,
    host_id VARCHAR NOT NULL,
    problem_type VARCHAR NOT NULL,
    severity VARCHAR NOT NULL,
    details JSONB,
    detected_at TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ,
    FOREIGN KEY (host_id) REFERENCES public.host(id)
);
CREATE INDEX idx_host_problems_host_detected ON host_problems(host_id, detected_at);
CREATE INDEX idx_host_problems_type_severity ON host_problems(problem_type, severity);

-- Historical trends for analysis
CREATE TABLE host_health_trends (
    id SERIAL PRIMARY KEY,
    host_id VARCHAR NOT NULL,
    date DATE NOT NULL,
    problems_detected INTEGER NOT NULL DEFAULT 0,
    health_score DECIMAL(3,2) NOT NULL DEFAULT 100.0,
    FOREIGN KEY (host_id) REFERENCES public.host(id)
);
CREATE INDEX idx_host_health_trends_host_date ON host_health_trends(host_id, date);
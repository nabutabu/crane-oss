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
	providerID varchar NULL,
);

-- dbo.host definition

-- Drop table

DROP TABLE public.host;

CREATE TABLE public.host (
	id varchar NOT NULL,
	"role" varchar NULL,
	"zone" varchar NULL,
	imageid varchar NULL,
	state varchar NOT NULL,
	health varchar NULL,
	createdat timestamptz NULL,
	updatedat timestamptz NULL
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
    INDEX(host_id, detected_at),
    INDEX(problem_type, severity),
    FOREIGN KEY (host_id) REFERENCES public.host(id)
);
-- Historical trends for analysis
CREATE TABLE host_health_trends (
    id SERIAL PRIMARY KEY,
    host_id VARCHAR NOT NULL,
    date DATE NOT NULL,
    problems_detected INTEGER NOT NULL DEFAULT 0,
    health_score DECIMAL(3,2) NOT NULL DEFAULT 100.0,
    INDEX(host_id, date),
    FOREIGN KEY (host_id) REFERENCES public.host(id)
);
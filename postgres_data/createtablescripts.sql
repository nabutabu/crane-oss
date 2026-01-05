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
	updatedat timestamptz NULL
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
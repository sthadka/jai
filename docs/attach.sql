-- docs/attach.sql — DuckDB convenience layer over the live jai.db (bead jai-2lc.6)
-- =============================================================================
-- Attaches the live SQLite `jai.db` READ-ONLY and defines convenience views,
-- macros, and variables for notebook / BI exploration of the flow-metrics fact
-- store (`fact_issue_flow`, `fact_field_interval`, `fact_issue_membership`).
-- Spec of record: `flow-metrics-export-analysis-plan.md` §3 (architecture) and §4.
-- Companion: `docs/metrics-duckdb-reconciliation.md` (the correctness gate).
--
-- The one engine ergonomics this file relies on (bead jai-2lc.6): the delivered
-- cohort's occupancy is now a persisted column `fact_issue_flow.flow_occupancy`
-- (active ÷ lead, bounded [0,1]; NULL for non-delivery / undefined lead) and an
-- `estimate` column (story points where present), so neither needs a per-query
-- windowed active-seconds aggregation or a join.
--
-- -----------------------------------------------------------------------------
-- HOW TO RUN
-- -----------------------------------------------------------------------------
-- DuckDB's ATTACH takes a string literal (no ~, no env-var expansion), so this
-- file ships a `PATH/TO/jai.db` placeholder — never a hard-coded absolute path.
-- Substitute your DB path (default below) and pipe it in:
--
--   sed "s#PATH/TO/jai.db#$HOME/.local/share/jai/jai.db#" docs/attach.sql \
--     | duckdb :memory:
--
-- Do NOT pass `-readonly` to the duckdb CLI — that forbids the in-memory catalog
-- these views live in; the READ-ONLY guarantee is the `READ_ONLY` on the SQLite
-- ATTACH, which is what keeps jai.db untouched.
--
-- -----------------------------------------------------------------------------
-- PARQUET SNAPSHOT (warehouse hand-off)
-- -----------------------------------------------------------------------------
-- A one-liner snapshot of the active generation's flow facts for portability /
-- warehouse hand-off (the attach above is the query path; this is export-only):
--
--   COPY (SELECT * FROM jai.fact_issue_flow WHERE generation = getvariable('g'))
--     TO 'snapshot.parquet' (FORMAT parquet);
--
-- (uncomment the COPY at the end of this file to produce it, or run it ad-hoc).
-- =============================================================================

INSTALL sqlite;
LOAD sqlite;

ATTACH 'PATH/TO/jai.db' AS jai (TYPE sqlite, READ_ONLY);

-- Active generation: every fact query filters on it (readers never see staged
-- or obsolete generations). Bound once as `g`.
SET VARIABLE g = (SELECT generation FROM jai.metrics_active_generation WHERE id = 1);

-- Genuine-delivery resolutions come from the `metrics:` config (`done_resolutions`).
-- The values below are ROX's documented set — EDIT this one line to match your
-- tenant's config; everything downstream reads it, so there is no tenant literal
-- scattered through the views.
SET VARIABLE done_res = ['Done', 'Done-Errata'];

-- -----------------------------------------------------------------------------
-- Percentile / unit helpers
-- -----------------------------------------------------------------------------
-- seconds -> days (lead/cycle are stored in seconds); R-7 continuous percentiles
-- (quantile_cont) match the Go providers' interpolation exactly.
CREATE OR REPLACE MACRO to_days(seconds) AS seconds / 86400.0;
CREATE OR REPLACE MACRO p50(x) AS quantile_cont(x, 0.50);
CREATE OR REPLACE MACRO p85(x) AS quantile_cont(x, 0.85);
CREATE OR REPLACE MACRO p95(x) AS quantile_cont(x, 0.95);

-- -----------------------------------------------------------------------------
-- Delivered cohort (the shared completion policy, as a view)
-- -----------------------------------------------------------------------------
-- Resolved issues with a genuine-done resolution, scoped to the active generation.
-- This is the cohort cycle_time / lead_time / flow_occupancy / ratio run over.
CREATE OR REPLACE VIEW delivered AS
SELECT *
FROM jai.fact_issue_flow
WHERE generation = getvariable('g')
  AND resolved IS NOT NULL
  AND list_contains(getvariable('done_res'), resolution);

-- -----------------------------------------------------------------------------
-- Period keys (day / week / month / quarter / year)
-- -----------------------------------------------------------------------------
-- date_trunc keys off the completion instant for time-series grouping at any grain
-- (the engine's CLI stays month/week; the grain is a view concern). created_* keys
-- support created-date grouping / net-flow.
CREATE OR REPLACE VIEW flow_periods AS
SELECT
  f.*,
  date_trunc('day',     resolved) AS resolved_day,
  date_trunc('week',    resolved) AS resolved_week,
  date_trunc('month',   resolved) AS resolved_month,
  date_trunc('quarter', resolved) AS resolved_quarter,
  date_trunc('year',    resolved) AS resolved_year,
  date_trunc('day',     created)  AS created_day,
  date_trunc('week',    created)  AS created_week,
  date_trunc('month',   created)  AS created_month,
  date_trunc('quarter', created)  AS created_quarter,
  date_trunc('year',    created)  AS created_year
FROM delivered f;

-- -----------------------------------------------------------------------------
-- Membership dimension joins (component / label / sprint)
-- -----------------------------------------------------------------------------
-- Event-time, fan-out-safe temporal joins: one row per (issue, member) via the
-- half-open interval `entered_at <= resolved AND (exited_at IS NULL OR exited_at
-- > resolved)`, so GROUP BY the member column never double-counts a single issue's
-- measure. member is the value in effect at completion (never today's snapshot).
CREATE OR REPLACE VIEW flow_by_component AS
SELECT f.*, m.member_name AS component
FROM delivered f
JOIN jai.fact_issue_membership m
  ON m.generation = f.generation
 AND m.issue_key  = f.issue_key
 AND m.dimension  = 'component'
 AND m.entered_at <= f.resolved
 AND (m.exited_at IS NULL OR m.exited_at > f.resolved);

CREATE OR REPLACE VIEW flow_by_label AS
SELECT f.*, m.member_name AS label
FROM delivered f
JOIN jai.fact_issue_membership m
  ON m.generation = f.generation
 AND m.issue_key  = f.issue_key
 AND m.dimension  = 'label'
 AND m.entered_at <= f.resolved
 AND (m.exited_at IS NULL OR m.exited_at > f.resolved);

CREATE OR REPLACE VIEW flow_by_sprint AS
SELECT f.*, m.member_id AS sprint_id, m.member_name AS sprint
FROM delivered f
JOIN jai.fact_issue_membership m
  ON m.generation = f.generation
 AND m.issue_key  = f.issue_key
 AND m.dimension  = 'sprint'
 AND m.entered_at <= f.resolved
 AND (m.exited_at IS NULL OR m.exited_at > f.resolved);

-- -----------------------------------------------------------------------------
-- Example queries (uncomment to run; all read the views above)
-- -----------------------------------------------------------------------------
-- Overall lead/cycle/occupancy distribution (no windowing — occupancy is a column):
--   SELECT count(*) n,
--          round(p50(to_days(lead_seconds)), 2)  AS lead_p50,
--          round(p95(to_days(lead_seconds)), 2)  AS lead_p95,
--          round(p50(to_days(cycle_seconds)), 2) AS cycle_p50,
--          round(p50(flow_occupancy), 4)         AS occ_p50,
--          round(p95(flow_occupancy), 4)         AS occ_p95
--   FROM delivered;
--
-- Lead p50 per component (event-time, fan-out-safe):
--   SELECT component, count(*) n, round(p50(to_days(lead_seconds)), 1) p50
--   FROM flow_by_component WHERE lead_seconds IS NOT NULL
--   GROUP BY component HAVING count(*) >= 50 ORDER BY p50 DESC;
--
-- Throughput per month:
--   SELECT resolved_month, count(*) throughput
--   FROM flow_periods GROUP BY resolved_month ORDER BY resolved_month;
--
-- Effort (Σ estimate) per month, where story points exist (NULL-safe):
--   SELECT resolved_month, sum(estimate) effort
--   FROM flow_periods WHERE estimate IS NOT NULL
--   GROUP BY resolved_month ORDER BY resolved_month;

-- Load sanity check (prints when the file is piped into duckdb):
SELECT
  getvariable('g')                                    AS active_generation,
  (SELECT count(*) FROM delivered)                    AS delivered_rows,
  (SELECT count(*) FROM delivered
     WHERE flow_occupancy IS NOT NULL)                AS occupancy_rows,
  (SELECT count(*) FROM delivered
     WHERE estimate IS NOT NULL)                      AS estimate_rows,
  (SELECT count(DISTINCT component) FROM flow_by_component) AS components;

-- Parquet snapshot (warehouse hand-off) — uncomment to produce snapshot.parquet:
-- COPY (SELECT * FROM jai.fact_issue_flow WHERE generation = getvariable('g'))
--   TO 'snapshot.parquet' (FORMAT parquet);

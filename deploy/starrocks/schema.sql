-- StarRocks real-time schema for the home-improvement query API.
-- Non-overlap contract:
--   StarRocks stores real-time days only: stat_date >= realtime cutoff.
--   Hive or Trino stores historical days only: stat_date < realtime cutoff.
--   Keep the ranges disjoint so source=auto can sum segments without double counting.

CREATE DATABASE IF NOT EXISTS analytics;
USE analytics;

CREATE TABLE IF NOT EXISTS dws_retail_order_daily (
  org_id VARCHAR(64) NOT NULL COMMENT 'Organization identifier, for example ORG-001',
  stat_date DATE NOT NULL COMMENT 'Business statistic date',
  order_count BIGINT NOT NULL COMMENT 'Retail order count for the organization and day',
  sales_amount DECIMAL(18,2) NOT NULL COMMENT 'Retail sales amount for the organization and day'
)
PRIMARY KEY (org_id, stat_date)
PARTITION BY RANGE(stat_date) (
  PARTITION p202607 VALUES [('2026-07-01'), ('2026-08-01')),
  PARTITION p202608 VALUES [('2026-08-01'), ('2026-09-01'))
)
DISTRIBUTED BY HASH(org_id) BUCKETS 8
PROPERTIES (
  "replication_num" = "1"
);

CREATE TABLE IF NOT EXISTS dws_renovation_stage_daily (
  org_id VARCHAR(64) NOT NULL COMMENT 'Organization identifier, for example ORG-001',
  stat_date DATE NOT NULL COMMENT 'Business statistic date',
  lead_count BIGINT NOT NULL COMMENT 'Renovation leads created that day',
  invited_count BIGINT NOT NULL COMMENT 'Leads invited to store or appointment',
  measured_count BIGINT NOT NULL COMMENT 'Leads with measurement completed',
  signed_count BIGINT NOT NULL COMMENT 'Leads with contract signed',
  started_count BIGINT NOT NULL COMMENT 'Projects started',
  completed_count BIGINT NOT NULL COMMENT 'Projects completed'
)
PRIMARY KEY (org_id, stat_date)
PARTITION BY RANGE(stat_date) (
  PARTITION p202607 VALUES [('2026-07-01'), ('2026-08-01')),
  PARTITION p202608 VALUES [('2026-08-01'), ('2026-09-01'))
)
DISTRIBUTED BY HASH(org_id) BUCKETS 8
PROPERTIES (
  "replication_num" = "1"
);

-- Idempotent real-time examples for ORG-001. PRIMARY KEY upserts replace the same org/date rows.
INSERT INTO dws_retail_order_daily
  (org_id, stat_date, order_count, sales_amount)
VALUES
  ('ORG-001', '2026-08-05', 141, 219876.54),
  ('ORG-001', '2026-08-06', 144, 225001.25);

INSERT INTO dws_renovation_stage_daily
  (org_id, stat_date, lead_count, invited_count, measured_count, signed_count, started_count, completed_count)
VALUES
  ('ORG-001', '2026-08-05', 96, 70, 46, 23, 17, 12),
  ('ORG-001', '2026-08-06', 99, 72, 48, 24, 18, 13);

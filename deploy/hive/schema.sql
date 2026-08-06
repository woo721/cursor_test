-- Hive/Trino historical schema for the home-improvement query API.
-- Non-overlap contract:
--   Hive stores immutable historical days only: stat_date < realtime cutoff.
--   StarRocks stores real-time days only: stat_date >= realtime cutoff.
--   The API's source=auto planner joins these disjoint ranges and rejects partial results.

CREATE DATABASE IF NOT EXISTS analytics;

CREATE TABLE IF NOT EXISTS analytics.dws_retail_order_daily (
  org_id STRING NOT NULL COMMENT 'Organization identifier, for example ORG-001',
  order_count BIGINT NOT NULL COMMENT 'Retail order count for the organization and day',
  sales_amount DECIMAL(18,2) NOT NULL COMMENT 'Retail sales amount for the organization and day'
)
COMMENT 'Historical daily retail order summary. Do not load rows that also exist in StarRocks.'
PARTITIONED BY (
  stat_date DATE COMMENT 'Business statistic date'
)
STORED AS PARQUET;

CREATE TABLE IF NOT EXISTS analytics.dws_renovation_stage_daily (
  org_id STRING NOT NULL COMMENT 'Organization identifier, for example ORG-001',
  lead_count BIGINT NOT NULL COMMENT 'Renovation leads created that day',
  invited_count BIGINT NOT NULL COMMENT 'Leads invited to store or appointment',
  measured_count BIGINT NOT NULL COMMENT 'Leads with measurement completed',
  signed_count BIGINT NOT NULL COMMENT 'Leads with contract signed',
  started_count BIGINT NOT NULL COMMENT 'Projects started',
  completed_count BIGINT NOT NULL COMMENT 'Projects completed'
)
COMMENT 'Historical daily renovation funnel. Do not load rows that also exist in StarRocks.'
PARTITIONED BY (
  stat_date DATE COMMENT 'Business statistic date'
)
STORED AS PARQUET;

-- Idempotent historical examples for ORG-001. Re-running overwrites the same partitions.
INSERT OVERWRITE TABLE analytics.dws_retail_order_daily PARTITION (stat_date = '2026-07-31')
SELECT 'ORG-001', CAST(128 AS BIGINT), CAST(188888.88 AS DECIMAL(18,2));

INSERT OVERWRITE TABLE analytics.dws_retail_order_daily PARTITION (stat_date = '2026-08-01')
SELECT 'ORG-001', CAST(136 AS BIGINT), CAST(201234.56 AS DECIMAL(18,2));

INSERT OVERWRITE TABLE analytics.dws_renovation_stage_daily PARTITION (stat_date = '2026-07-31')
SELECT
  'ORG-001',
  CAST(88 AS BIGINT),
  CAST(64 AS BIGINT),
  CAST(43 AS BIGINT),
  CAST(21 AS BIGINT),
  CAST(15 AS BIGINT),
  CAST(11 AS BIGINT);

INSERT OVERWRITE TABLE analytics.dws_renovation_stage_daily PARTITION (stat_date = '2026-08-01')
SELECT
  'ORG-001',
  CAST(92 AS BIGINT),
  CAST(67 AS BIGINT),
  CAST(44 AS BIGINT),
  CAST(22 AS BIGINT),
  CAST(16 AS BIGINT),
  CAST(12 AS BIGINT);

-- Queries for the relay's usage records (see usage.tf).
--
-- Replace PROJECT with your project ID. The table appears after the first
-- matching log line arrives, a minute or two after the sink is created.
--
-- Run one with:  bq query --use_legacy_sql=false < query.sql
--
-- A record is one line of the relay's log, for example:
--   ... runlease-relay[1401]: time=... level=INFO msg="token registered"
--       token=8c4843bd… owner=ada@example.com tier=free client="runlease/0.5.0 (linux/amd64)"
--   ... msg="slot released" token=8c4843bd… owner=ada@example.com tier=free lived_s=5400

-- 0. The records as columns. The queries below read this view.
CREATE OR REPLACE VIEW `PROJECT.relay_usage.events` AS
SELECT
  timestamp,
  REGEXP_EXTRACT(textPayload, r'msg="([^"]+)"')          AS event,
  REGEXP_EXTRACT(textPayload, r' owner=(\S+)')           AS owner,
  REGEXP_EXTRACT(textPayload, r' tier=(\S+)')            AS tier,
  REGEXP_EXTRACT(textPayload, r' token=(\S+)')           AS slot,
  REGEXP_EXTRACT(textPayload, r' client="?([^"]+?)"?(?: |$)') AS client,
  SAFE_CAST(REGEXP_EXTRACT(textPayload, r' lived_s=(\d+)') AS INT64) AS lived_s
FROM `PROJECT.relay_usage.serialconsole_googleapis_com_serial_port_1_output`;

-- 1. People and launches per day.
SELECT
  DATE(timestamp)       AS day,
  COUNT(DISTINCT owner) AS active_users,
  COUNT(*)              AS launches
FROM `PROJECT.relay_usage.events`
WHERE event = 'token registered'
GROUP BY day
ORDER BY day DESC;

-- 2. New users per week: the first week each person launched anything.
SELECT first_week, COUNT(*) AS new_users
FROM (
  SELECT owner, DATE_TRUNC(MIN(DATE(timestamp)), WEEK) AS first_week
  FROM `PROJECT.relay_usage.events`
  WHERE event = 'token registered'
  GROUP BY owner
)
GROUP BY first_week
ORDER BY first_week DESC;

-- 3. Who hit their tier's limit, and how often. These people wanted more than
--    the tier allows: the clearest signal for a paid tier.
SELECT owner, tier, COUNT(*) AS refusals, MAX(timestamp) AS last_refused
FROM `PROJECT.relay_usage.events`
WHERE event = 'register rejected: identity at tier limit'
GROUP BY owner, tier
ORDER BY refusals DESC;

-- 4. How long machines live, per tier.
SELECT
  tier,
  COUNT(*)                                             AS machines,
  ROUND(APPROX_QUANTILES(lived_s, 100)[OFFSET(50)] / 60) AS median_minutes,
  ROUND(APPROX_QUANTILES(lived_s, 100)[OFFSET(90)] / 60) AS p90_minutes,
  ROUND(SUM(lived_s) / 3600, 1)                        AS machine_hours
FROM `PROJECT.relay_usage.events`
WHERE event LIKE 'slot released%' AND lived_s IS NOT NULL
GROUP BY tier;

-- 5. CLI versions in use over the last 30 days.
SELECT client, COUNT(DISTINCT owner) AS users, COUNT(*) AS launches
FROM `PROJECT.relay_usage.events`
WHERE event = 'token registered'
  AND timestamp > TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 30 DAY)
GROUP BY client
ORDER BY launches DESC;

-- 6. Most active people over the last 30 days.
SELECT owner, COUNT(*) AS launches, COUNT(DISTINCT DATE(timestamp)) AS active_days
FROM `PROJECT.relay_usage.events`
WHERE event = 'token registered'
  AND timestamp > TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 30 DAY)
GROUP BY owner
ORDER BY launches DESC
LIMIT 50;

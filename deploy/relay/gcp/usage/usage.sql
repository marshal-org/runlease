-- Queries for the relay's usage records (see main.tf in this directory).
--
-- Each query stands alone and needs read access only. Replace PROJECT with
-- your project ID, copy one query into a file, and run:
--
--   bq query --use_legacy_sql=false < query.sql
--
-- The table appears a few minutes after the first matching log line arrives.
--
-- A record is one line of the relay's log, for example:
--   ... runlease-relay[1401]: time=... level=INFO msg="token registered"
--       token=a1b2c3d4… owner=ada@example.com tier=free client="runlease/0.5.0 (linux/amd64)"
--   ... msg="slot released" token=a1b2c3d4… owner=ada@example.com tier=free lived_s=5400
--
-- In the table the quotes arrive escaped (msg=\"token registered\"), which is
-- why the patterns below allow a backslash before each quote.
--
-- Every query starts by turning those lines into columns.

-- 1. People and launches per day.
WITH events AS (
  SELECT
    timestamp,
    REGEXP_EXTRACT(textPayload, r'msg=\\?"([^"\\]+)')                 AS event,
    REGEXP_EXTRACT(textPayload, r' owner=([^\s\\]+)')                  AS owner,
    REGEXP_EXTRACT(textPayload, r' tier=([A-Za-z0-9_.-]+)')            AS tier,
    -- Quoted when it has spaces ("runlease/0.5.0 (linux/amd64)"), bare when not (other).
    COALESCE(
      REGEXP_EXTRACT(textPayload, r' client=\\?"([^"\\]+)'),
      REGEXP_EXTRACT(textPayload, r' client=([^\s\\"]+)'))               AS client,
    SAFE_CAST(REGEXP_EXTRACT(textPayload, r' lived_s=(\d+)') AS INT64) AS lived_s
  FROM `PROJECT.relay_usage.serialconsole_googleapis_com_serial_port_1_output`
)
SELECT
  DATE(timestamp)       AS day,
  COUNT(DISTINCT owner) AS active_users,
  COUNT(*)              AS launches
FROM events
WHERE event = 'token registered'
GROUP BY day
ORDER BY day DESC;

-- 2. New users per week: the first week each person launched anything.
WITH events AS (
  SELECT
    timestamp,
    REGEXP_EXTRACT(textPayload, r'msg=\\?"([^"\\]+)')                 AS event,
    REGEXP_EXTRACT(textPayload, r' owner=([^\s\\]+)')                  AS owner,
    REGEXP_EXTRACT(textPayload, r' tier=([A-Za-z0-9_.-]+)')            AS tier,
    -- Quoted when it has spaces ("runlease/0.5.0 (linux/amd64)"), bare when not (other).
    COALESCE(
      REGEXP_EXTRACT(textPayload, r' client=\\?"([^"\\]+)'),
      REGEXP_EXTRACT(textPayload, r' client=([^\s\\"]+)'))               AS client,
    SAFE_CAST(REGEXP_EXTRACT(textPayload, r' lived_s=(\d+)') AS INT64) AS lived_s
  FROM `PROJECT.relay_usage.serialconsole_googleapis_com_serial_port_1_output`
)
SELECT first_week, COUNT(*) AS new_users
FROM (
  SELECT owner, DATE_TRUNC(MIN(DATE(timestamp)), WEEK) AS first_week
  FROM events
  WHERE event = 'token registered'
  GROUP BY owner
)
GROUP BY first_week
ORDER BY first_week DESC;

-- 3. Who hit their tier's limit, and how often. These people wanted more than
--    the tier allows: the clearest signal for a paid tier.
WITH events AS (
  SELECT
    timestamp,
    REGEXP_EXTRACT(textPayload, r'msg=\\?"([^"\\]+)')                 AS event,
    REGEXP_EXTRACT(textPayload, r' owner=([^\s\\]+)')                  AS owner,
    REGEXP_EXTRACT(textPayload, r' tier=([A-Za-z0-9_.-]+)')            AS tier,
    -- Quoted when it has spaces ("runlease/0.5.0 (linux/amd64)"), bare when not (other).
    COALESCE(
      REGEXP_EXTRACT(textPayload, r' client=\\?"([^"\\]+)'),
      REGEXP_EXTRACT(textPayload, r' client=([^\s\\"]+)'))               AS client,
    SAFE_CAST(REGEXP_EXTRACT(textPayload, r' lived_s=(\d+)') AS INT64) AS lived_s
  FROM `PROJECT.relay_usage.serialconsole_googleapis_com_serial_port_1_output`
)
SELECT owner, tier, COUNT(*) AS refusals, MAX(timestamp) AS last_refused
FROM events
WHERE event = 'register rejected: identity at tier limit'
GROUP BY owner, tier
ORDER BY refusals DESC;

-- 4. How long machines live, per tier. Needs a relay new enough to record
--    lived_s; older records are skipped.
WITH events AS (
  SELECT
    timestamp,
    REGEXP_EXTRACT(textPayload, r'msg=\\?"([^"\\]+)')                 AS event,
    REGEXP_EXTRACT(textPayload, r' owner=([^\s\\]+)')                  AS owner,
    REGEXP_EXTRACT(textPayload, r' tier=([A-Za-z0-9_.-]+)')            AS tier,
    -- Quoted when it has spaces ("runlease/0.5.0 (linux/amd64)"), bare when not (other).
    COALESCE(
      REGEXP_EXTRACT(textPayload, r' client=\\?"([^"\\]+)'),
      REGEXP_EXTRACT(textPayload, r' client=([^\s\\"]+)'))               AS client,
    SAFE_CAST(REGEXP_EXTRACT(textPayload, r' lived_s=(\d+)') AS INT64) AS lived_s
  FROM `PROJECT.relay_usage.serialconsole_googleapis_com_serial_port_1_output`
)
SELECT
  tier,
  COUNT(*)                                               AS machines,
  ROUND(APPROX_QUANTILES(lived_s, 100)[OFFSET(50)] / 60) AS median_minutes,
  ROUND(APPROX_QUANTILES(lived_s, 100)[OFFSET(90)] / 60) AS p90_minutes,
  ROUND(SUM(lived_s) / 3600, 1)                          AS machine_hours
FROM events
WHERE event LIKE 'slot released%' AND lived_s IS NOT NULL
GROUP BY tier;

-- 5. CLI versions in use over the last 30 days. Needs a relay new enough to
--    record the client; older records show as null.
WITH events AS (
  SELECT
    timestamp,
    REGEXP_EXTRACT(textPayload, r'msg=\\?"([^"\\]+)')                 AS event,
    REGEXP_EXTRACT(textPayload, r' owner=([^\s\\]+)')                  AS owner,
    REGEXP_EXTRACT(textPayload, r' tier=([A-Za-z0-9_.-]+)')            AS tier,
    -- Quoted when it has spaces ("runlease/0.5.0 (linux/amd64)"), bare when not (other).
    COALESCE(
      REGEXP_EXTRACT(textPayload, r' client=\\?"([^"\\]+)'),
      REGEXP_EXTRACT(textPayload, r' client=([^\s\\"]+)'))               AS client,
    SAFE_CAST(REGEXP_EXTRACT(textPayload, r' lived_s=(\d+)') AS INT64) AS lived_s
  FROM `PROJECT.relay_usage.serialconsole_googleapis_com_serial_port_1_output`
)
SELECT client, COUNT(DISTINCT owner) AS users, COUNT(*) AS launches
FROM events
WHERE event = 'token registered'
  AND timestamp > TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 30 DAY)
GROUP BY client
ORDER BY launches DESC;

-- 6. Most active people over the last 30 days.
WITH events AS (
  SELECT
    timestamp,
    REGEXP_EXTRACT(textPayload, r'msg=\\?"([^"\\]+)')                 AS event,
    REGEXP_EXTRACT(textPayload, r' owner=([^\s\\]+)')                  AS owner,
    REGEXP_EXTRACT(textPayload, r' tier=([A-Za-z0-9_.-]+)')            AS tier,
    -- Quoted when it has spaces ("runlease/0.5.0 (linux/amd64)"), bare when not (other).
    COALESCE(
      REGEXP_EXTRACT(textPayload, r' client=\\?"([^"\\]+)'),
      REGEXP_EXTRACT(textPayload, r' client=([^\s\\"]+)'))               AS client,
    SAFE_CAST(REGEXP_EXTRACT(textPayload, r' lived_s=(\d+)') AS INT64) AS lived_s
  FROM `PROJECT.relay_usage.serialconsole_googleapis_com_serial_port_1_output`
)
SELECT owner, COUNT(*) AS launches, COUNT(DISTINCT DATE(timestamp)) AS active_days
FROM events
WHERE event = 'token registered'
  AND timestamp > TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 30 DAY)
GROUP BY owner
ORDER BY launches DESC
LIMIT 50;

-- F5 example seed: a small metrics table used by the Layer 1 / Layer 2 alert
-- rules in this directory (report §10) and by verification/alert_check.py.
--
-- Apply with SQL*Plus / your own client against the plugin's datasource user,
-- e.g.:
--
--   sqlplus -S "SYSTEM/verif_password@//localhost:1521/XEPDB1" @seed.sql
--
-- The rows are in UTC and deliberately fresh (the alert checks use a 1 h
-- relative time range), with one historical row that falls outside it.

DROP TABLE f5_alert;

CREATE TABLE f5_alert (
  ts     DATE             NOT NULL,
  value  NUMBER(10, 2)    NOT NULL,
  metric VARCHAR2(40)     NOT NULL
);

-- Current window (UTC) — 'cpu' recent samples rising to 30.5 so Layer 1's
-- Reduce(last) sees 30.5 (> 25 → firing) and Layer 2's value > 15 matches
-- the 20.0 / 25.0 / 30.5 rows (COUNT = 3 → firing).
INSERT INTO f5_alert (ts, value, metric)
SELECT SYSDATE - INTERVAL '50' MINUTE, 10.5,  'cpu' FROM DUAL UNION ALL
SELECT SYSDATE - INTERVAL '40' MINUTE, 20.0,  'cpu' FROM DUAL UNION ALL
SELECT SYSDATE - INTERVAL '30' MINUTE, 25.0,  'cpu' FROM DUAL UNION ALL
SELECT SYSDATE - INTERVAL '20' MINUTE, 30.5,  'cpu' FROM DUAL;

-- Historical row outside the alert window.
INSERT INTO f5_alert (ts, value, metric)
SELECT TO_DATE('2026-08-06 12:00:00', 'YYYY-MM-DD HH24:MI:SS'), 5.0, 'cpu' FROM DUAL;

COMMIT;

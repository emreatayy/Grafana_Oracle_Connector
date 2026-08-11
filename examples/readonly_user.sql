-- =============================================================================
-- Read-only Oracle user for the Grafana Oracle datasource (report §15 / ADR-7)
-- =============================================================================
--
-- The plugin enforces a read-only query path itself (first-keyword guard:
-- only SELECT / WITH reach the database, report §15). A dedicated read-only
-- Oracle account is the *defense in depth* layer on top: even a mis-typed or
-- mis-leading statement cannot DML against production tables, because the
-- account simply has no INSERT/UPDATE/DELETE/DDL privileges.
--
-- Run this as a DBA (e.g. SYSTEM / SYS) against the target database. Replace
-- the placeholders before running:
--
--   1.  GRAFANA_READONLY  — the account name the datasource will use
--   2.  <strong password> — pick a strong password (stored in Grafana's
--                           secureJsonData, never in the database or logs)
--   3.  APP_OWNER.*       — the application schema(s) whose tables the
--                           dashboards query (repeat the GRANT for each table,
--                           or grant the whole schema as shown)
--
-- The health "Test" button reads Oracle's version banner via V$VERSION; that
-- is why SYS.V_$VERSION needs an explicit SELECT grant (V$ views are not
-- readable by default). The editor's schema metadata uses the ALL_TAB_COMMENTS
-- / ALL_TAB_COLUMNS / ALL_COL_COMMENTS views, which are public and return only
-- objects this account can see, so no extra grant is required for autocomplete.
--
-- This account intentionally has NO DBMS_SCHEDULER privileges: the "Run
-- command" operations UI (report §12) is an administrator feature. Give the
-- command allowlist to a separate, elevated account if you want operator
-- actions to be available at all — never to the dashboards' read-only user.

-- 1. Create the account (24-byte identifier limit applies).
CREATE USER GRAFANA_READONLY IDENTIFIED BY "<strong password>";

-- 2. Minimum privilege: connect.
GRANT CREATE SESSION TO GRAFANA_READONLY;

-- 3. Health check ("Test" button) banner query.
GRANT SELECT ON SYS.V_$VERSION TO GRAFANA_READONLY;

-- 4. The application tables the Grafana dashboards read. Two styles:
--
--    a) one grant per table (least privilege, recommended):
--         GRANT SELECT ON APP_OWNER.ORDERS      TO GRAFANA_READONLY;
--         GRANT SELECT ON APP_OWNER.CUSTOMERS   TO GRAFANA_READONLY;
--         GRANT SELECT ON APP_OWNER.PAYMENTS    TO GRAFANA_READONLY;
--
--    b) or grant the whole schema (simpler, broader):
GRANT SELECT ANY TABLE TO GRAFANA_READONLY;  -- choose (a) or (b), not both

-- 5. Optional: let the account see its own privileges for convenience.
--      GRANT SELECT ON SYS.USER_TAB_PRIVS TO GRAFANA_READONLY;

-- Verify as the new account:
--   CONNECT GRAFANA_READONLY/<strong password>@<service>
--   SELECT banner FROM v$version WHERE ROWNUM = 1;   -- must return a row
--   SELECT COUNT(*) FROM APP_OWNER.ORDERS;            -- must return a count
--   DELETE FROM APP_OWNER.ORDERS;                     -- must fail with ORA-01031
--
-- The last statement failing (ORA-01031: insufficient privileges) is the
-- whole point of this account.

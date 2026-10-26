-- Template only: run as a superuser against the application database and
-- replace the passwords through your secret manager. The migrator owns DDL;
-- the runtime role can only read and write rows.
CREATE ROLE app_migrator LOGIN PASSWORD 'CHANGE_ME';
CREATE ROLE app_runtime LOGIN PASSWORD 'CHANGE_ME';

GRANT CONNECT ON DATABASE app TO app_migrator, app_runtime;
GRANT USAGE, CREATE ON SCHEMA public TO app_migrator;
GRANT USAGE ON SCHEMA public TO app_runtime;

-- Tables created by future migrations are automatically usable by the app.
ALTER DEFAULT PRIVILEGES FOR ROLE app_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE app_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO app_runtime;

-- One-time grant for tables that already exist.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_runtime;

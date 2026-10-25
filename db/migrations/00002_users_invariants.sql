-- +goose Up
-- The service validates these rules before writing, but the database is the
-- last line of defence: a future importer, migration, or direct psql session
-- must not be able to create a blank name or a case-variant duplicate email.
ALTER TABLE users
    ADD CONSTRAINT users_name_not_blank CHECK (name ~ '[^[:space:]]'),
    ADD CONSTRAINT users_name_length CHECK (char_length(name) <= 100),
    ADD CONSTRAINT users_email_not_blank CHECK (email ~ '[^[:space:]]'),
    ADD CONSTRAINT users_email_length CHECK (char_length(email) <= 320);

-- The original UNIQUE(email) is case-sensitive, so it does not stop
-- "Alice@example.com" and "alice@example.com" from both existing. Normalize
-- in the service and enforce the invariant on lower(email) here.
ALTER TABLE users DROP CONSTRAINT users_email_key;
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

-- +goose Down
DROP INDEX users_email_lower_key;
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);
ALTER TABLE users
    DROP CONSTRAINT users_email_length,
    DROP CONSTRAINT users_email_not_blank,
    DROP CONSTRAINT users_name_length,
    DROP CONSTRAINT users_name_not_blank;

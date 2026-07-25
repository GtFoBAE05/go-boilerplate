-- +goose Up
CREATE TABLE users (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL,
    email      TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Maintained by the UpdateUser query, not a trigger: the write path is
    -- explicit SQL, so a trigger would hide the mutation from readers of
    -- db/queries and from sqlc's generated types.
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE users;

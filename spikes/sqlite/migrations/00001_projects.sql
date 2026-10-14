-- +goose Up
CREATE TABLE projects (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

-- +goose Down
DROP TABLE projects;

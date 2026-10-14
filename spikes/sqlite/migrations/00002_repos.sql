-- +goose Up
CREATE TABLE repos (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    path       TEXT    NOT NULL,
    UNIQUE (project_id, name)
) STRICT;

CREATE INDEX repos_project_id ON repos (project_id);

-- +goose Down
DROP TABLE repos;

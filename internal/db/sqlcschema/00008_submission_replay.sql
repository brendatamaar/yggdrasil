-- NULL identifies legacy action rows whose full immutable input is unrecoverable.
-- Empty strings are real values for newly validated requests, never backfilled.
ALTER TABLE workspace_action_requests ADD COLUMN agent TEXT;
ALTER TABLE workspace_action_requests ADD COLUMN model TEXT;

CREATE TABLE task_submissions (
    request_id TEXT PRIMARY KEY NOT NULL CHECK (
        length(request_id) BETWEEN 1 AND 100
        AND instr(request_id, ' ') = 0 AND instr(request_id, '/') = 0
        AND instr(request_id, char(92)) = 0 AND instr(request_id, char(9)) = 0
        AND instr(request_id, char(10)) = 0 AND instr(request_id, char(13)) = 0),
    digest TEXT NOT NULL CHECK (length(digest) = 64 AND digest NOT GLOB '*[^0-9a-f]*'),
    task_id TEXT NOT NULL REFERENCES tasks(id)
);
CREATE INDEX task_submissions_task ON task_submissions(task_id);

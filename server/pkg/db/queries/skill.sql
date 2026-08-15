-- Skill CRUD

-- name: ListSkillsByWorkspace :many
SELECT * FROM skill
WHERE workspace_id = $1
ORDER BY name ASC;

-- name: ListSkillSummariesByWorkspace :many
-- Same as ListSkillsByWorkspace but omits the SKILL.md `content` column. Used
-- by list endpoints (CLI table, web list page) where the body is never read;
-- shipping it everywhere blew up payload size on workspaces with many skills
-- and caused 15s CLI timeouts from high-latency regions (GH multica-ai/multica#2174).
SELECT id, workspace_id, name, description, config, created_by, created_at, updated_at
FROM skill
WHERE workspace_id = $1
ORDER BY name ASC;

-- name: GetSkill :one
SELECT * FROM skill
WHERE id = $1;

-- name: GetSkillInWorkspace :one
SELECT * FROM skill
WHERE id = $1 AND workspace_id = $2;

-- name: GetSkillByWorkspaceAndName :one
-- Used by skill import and runtime-local skill discovery to reuse a workspace
-- skill by name rather than violating UNIQUE(workspace_id, name).
SELECT * FROM skill
WHERE workspace_id = $1 AND name = $2;

-- name: CreateSkill :one
INSERT INTO skill (workspace_id, name, description, content, config, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: MaterializeRoleSourceSkills :many
-- Create or update one bounded source-owned Skill batch. New identities are
-- allocated by the caller. Updates preserve config, creator and all fields not
-- listed in the SET clause. The caller verifies the exact returned ID set.
WITH input AS MATERIALIZED (
    SELECT
        (item ->> 'id')::UUID AS id,
        item ->> 'operation' AS operation,
        item ->> 'name' AS name,
        item ->> 'description' AS description,
        item ->> 'content' AS content,
        item -> 'config' AS config,
        (item ->> 'created_by')::UUID AS created_by
    FROM jsonb_array_elements(@skills::jsonb) AS item
), updated AS (
    UPDATE skill target
    SET name = input.name,
        description = input.description,
        content = input.content,
        updated_at = now()
    FROM input
    WHERE input.operation = 'update'
      AND target.id = input.id
      AND target.workspace_id = @workspace_id
    RETURNING target.id
), inserted AS (
    INSERT INTO skill (id, workspace_id, name, description, content, config, created_by)
    SELECT id, @workspace_id, name, description, content, config, created_by
    FROM input
    WHERE operation = 'create'
    RETURNING skill.id
)
SELECT id FROM updated
UNION ALL
SELECT id FROM inserted
ORDER BY id;

-- name: UpdateSkill :one
UPDATE skill SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    content = COALESCE(sqlc.narg('content'), content),
    config = COALESCE(sqlc.narg('config'), config),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteSkill :exec
-- Defense-in-depth: workspace_id is a SQL-layer tenant guard. See DeleteIssue.
DELETE FROM skill WHERE id = $1 AND workspace_id = $2;

-- Skill File CRUD

-- name: ListSkillFiles :many
SELECT * FROM skill_file
WHERE skill_id = $1
ORDER BY path ASC;

-- name: GetSkillFile :one
SELECT * FROM skill_file
WHERE id = $1;

-- name: UpsertSkillFile :one
INSERT INTO skill_file (skill_id, path, content)
VALUES ($1, $2, $3)
ON CONFLICT (skill_id, path) DO UPDATE SET
    content = EXCLUDED.content,
    updated_at = now()
RETURNING *;

-- name: DeleteSkillFile :exec
DELETE FROM skill_file WHERE id = $1;

-- name: DeleteSkillFilesBySkill :exec
DELETE FROM skill_file WHERE skill_id = $1;

-- name: LockRoleSourceSkillsForFileSync :many
-- Lock every target Skill in canonical order before any supporting-file work.
-- The caller verifies the exact returned ID set before using the file cache.
SELECT id
FROM skill
WHERE id = ANY(@skill_ids::uuid[]) AND workspace_id = @workspace_id
ORDER BY id
FOR UPDATE;

-- name: ListRoleSourceSkillFilesForUpdateByTargets :many
WITH requested AS MATERIALIZED (
    SELECT
        (item ->> 'skill_id')::UUID AS skill_id,
        item ->> 'path' AS path
    FROM jsonb_array_elements(@targets::jsonb) AS item
)
SELECT file.*
FROM skill_file file
JOIN requested ON requested.skill_id = file.skill_id AND requested.path = file.path
JOIN skill ON skill.id = file.skill_id
WHERE skill.workspace_id = @workspace_id
ORDER BY file.skill_id, file.path
FOR UPDATE OF file;

-- name: MaterializeRoleSourceSkillFiles :many
-- Apply one bounded, canonical final-state batch. Inserts intentionally have no
-- conflict handler: a user-created path racing the source apply must fail and
-- roll back instead of being overwritten. Updates/deletes are limited to the
-- already locked tenant Skill targets and exact paths selected by the caller.
WITH input AS MATERIALIZED (
    SELECT
        (item ->> 'skill_id')::UUID AS skill_id,
        item ->> 'path' AS path,
        item ->> 'operation' AS operation,
        item ->> 'content' AS content
    FROM jsonb_array_elements(@files::jsonb) AS item
), deleted AS (
    DELETE FROM skill_file target
    USING input
    WHERE input.operation = 'delete'
      AND target.skill_id = input.skill_id
      AND target.path = input.path
      AND EXISTS (
          SELECT 1 FROM skill
          WHERE skill.id = target.skill_id AND skill.workspace_id = @workspace_id
      )
    RETURNING target.skill_id, target.path, 'delete'::TEXT AS operation
), updated AS (
    UPDATE skill_file target
    SET content = input.content,
        updated_at = now()
    FROM input
    WHERE input.operation = 'update'
      AND target.skill_id = input.skill_id
      AND target.path = input.path
      AND EXISTS (
          SELECT 1 FROM skill
          WHERE skill.id = target.skill_id AND skill.workspace_id = @workspace_id
      )
    RETURNING target.skill_id, target.path, 'update'::TEXT AS operation
), inserted AS (
    INSERT INTO skill_file (skill_id, path, content)
    SELECT input.skill_id, input.path, input.content
    FROM input
    JOIN skill ON skill.id = input.skill_id AND skill.workspace_id = @workspace_id
    WHERE input.operation = 'insert'
    RETURNING skill_file.skill_id, skill_file.path, 'insert'::TEXT AS operation
)
SELECT skill_id, path, operation FROM deleted
UNION ALL
SELECT skill_id, path, operation FROM updated
UNION ALL
SELECT skill_id, path, operation FROM inserted
ORDER BY skill_id, path;

-- Agent-Skill junction

-- name: ListAgentSkills :many
SELECT s.* FROM skill s
JOIN agent_skill ask ON ask.skill_id = s.id
WHERE ask.agent_id = $1 AND ask.enabled = TRUE
ORDER BY s.name ASC;

-- name: ListAgentSkillSummaries :many
-- Summary variant for the agent skills list endpoint — omits `content` for
-- the same reason as ListSkillSummariesByWorkspace.
SELECT s.id, s.workspace_id, s.name, s.description, s.config, s.created_by, s.created_at, s.updated_at, ask.enabled
FROM skill s
JOIN agent_skill ask ON ask.skill_id = s.id
WHERE ask.agent_id = $1
ORDER BY s.name ASC;

-- name: ListAgentSkillNamesByAgentIDs :many
SELECT ask.agent_id, s.name
FROM agent_skill ask
JOIN skill s ON s.id = ask.skill_id
WHERE ask.agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
  AND ask.enabled = TRUE
ORDER BY ask.agent_id, s.name ASC;

-- name: AddAgentSkill :exec
INSERT INTO agent_skill (agent_id, skill_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: EnsureRoleSourceAgentSkills :many
-- A large source apply can bind thousands of newly materialized skills. Keep
-- each caller-bounded association batch set-based and tenant-validate both
-- endpoints before insertion. Existing disabled associations remain disabled,
-- matching the single-row AddAgentSkill ownership behavior.
WITH requested AS (
    SELECT
        (binding ->> 'agent_id')::UUID AS requested_agent_id,
        (binding ->> 'skill_id')::UUID AS requested_skill_id
    FROM jsonb_array_elements(@bindings::jsonb) AS binding
), valid AS (
    SELECT requested.requested_agent_id AS agent_id, requested.requested_skill_id AS skill_id
    FROM requested
    JOIN agent ON agent.id = requested.requested_agent_id
              AND agent.workspace_id = @workspace_id
              AND agent.kind = 'user'
              AND agent.archived_at IS NULL
    JOIN skill ON skill.id = requested.requested_skill_id
              AND skill.workspace_id = @workspace_id
), inserted AS (
    INSERT INTO agent_skill (agent_id, skill_id)
    SELECT agent_id, skill_id FROM valid
    ON CONFLICT DO NOTHING
    RETURNING agent_id, skill_id
)
SELECT inserted.agent_id, inserted.skill_id FROM inserted
UNION ALL
SELECT valid.agent_id, valid.skill_id
FROM valid
JOIN agent_skill existing
  ON existing.agent_id = valid.agent_id
 AND existing.skill_id = valid.skill_id
WHERE NOT EXISTS (
    SELECT 1 FROM inserted
    WHERE inserted.agent_id = valid.agent_id
      AND inserted.skill_id = valid.skill_id
)
ORDER BY 1, 2;

-- name: SetAgentSkillEnabled :execrows
UPDATE agent_skill
SET enabled = $3
WHERE agent_id = $1 AND skill_id = $2;

-- name: RemoveAgentSkill :exec
DELETE FROM agent_skill
WHERE agent_id = $1 AND skill_id = $2;

-- name: RemoveAllAgentSkills :exec
DELETE FROM agent_skill WHERE agent_id = $1;

-- name: ListAgentSkillsByWorkspace :many
SELECT ask.agent_id, s.id, s.name, s.description, ask.enabled
FROM agent_skill ask
JOIN skill s ON s.id = ask.skill_id
WHERE s.workspace_id = $1
ORDER BY s.name ASC;

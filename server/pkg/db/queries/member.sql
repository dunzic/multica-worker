-- name: ListMembers :many
SELECT * FROM member
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: ListWorkspaceManagerUserIDs :many
SELECT user_id FROM member
WHERE workspace_id = $1 AND role IN ('owner', 'admin')
ORDER BY created_at ASC;

-- name: GetMember :one
SELECT * FROM member
WHERE id = $1;

-- name: GetMemberByUserAndWorkspace :one
SELECT * FROM member
WHERE user_id = $1 AND workspace_id = $2;

-- name: GetRealtimeMemberships :many
SELECT m.user_id, m.workspace_id,
       requested.conversation_id::uuid AS conversation_id,
       requested.topic_id::uuid AS topic_id
FROM member m
JOIN (
    SELECT unnest(sqlc.arg(user_ids)::uuid[]) AS user_id,
           unnest(sqlc.arg(workspace_ids)::uuid[]) AS workspace_id,
           unnest(sqlc.arg(conversation_ids)::uuid[]) AS conversation_id,
           unnest(sqlc.arg(topic_ids)::uuid[]) AS topic_id
) AS requested
    ON m.user_id = requested.user_id AND m.workspace_id = requested.workspace_id
WHERE (requested.conversation_id IS NULL AND requested.topic_id IS NULL) OR EXISTS (
    SELECT 1 FROM conversation c
    JOIN conversation_participant p ON p.conversation_id = c.id
    WHERE c.workspace_id = m.workspace_id
      AND (requested.conversation_id IS NULL OR c.id = requested.conversation_id)
      AND (requested.topic_id IS NULL OR EXISTS (
          SELECT 1 FROM topic t WHERE t.id = requested.topic_id AND t.conversation_id = c.id
      ))
      AND p.principal_type = 'member' AND p.principal_id = m.user_id
      AND p.left_at IS NULL
);

-- name: CreateMember :one
INSERT INTO member (workspace_id, user_id, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateMemberRole :one
UPDATE member SET role = $2
WHERE id = $1
RETURNING *;

-- name: DeleteMember :exec
DELETE FROM member WHERE id = $1;

-- name: ListMembersWithUser :many
SELECT m.id, m.workspace_id, m.user_id, m.role, m.created_at,
       u.name as user_name, u.email as user_email, u.avatar_url as user_avatar_url
FROM member m
JOIN "user" u ON u.id = m.user_id
WHERE m.workspace_id = $1
ORDER BY m.created_at ASC;

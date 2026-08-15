#!/bin/sh
set -eu

docker_bin=${DOCKER_BIN:-docker}
run_id=$$
prefix="multica-rs06-dr-$run_id"
network="$prefix"
postgres="$prefix-postgres"
postgres_volume="$prefix-postgres-data"
restore_worker="$prefix-restore-worker"
pg_image=${MULTICA_RS06_POSTGRES_IMAGE:-pgvector/pgvector:pg17}
backend_image=${MULTICA_RS06_BACKEND_IMAGE:-multica-backend:dev}
work_dir=$(mktemp -d "/tmp/$prefix.XXXXXX")
source_storage="$work_dir/source-storage"
target_storage="$work_dir/target-storage"
backup_dir="$work_dir/backup"
failed_backup_dir="$work_dir/failed-backup"
host_uid=$(id -u)
host_gid=$(id -g)
pg_password=$(openssl rand -hex 24)
source_database=rs06_source
target_database=rs06_restore
source_url="postgres://multica:$pg_password@rs06-pg:5432/$source_database?sslmode=disable"
target_url="postgres://multica:$pg_password@rs06-pg:5432/$target_database?sslmode=disable"
target_safe_url="postgres://multica@rs06-pg:5432/$target_database?sslmode=disable"
workspace_id=00000000-0000-4000-8000-000000000601
runtime_id=00000000-0000-4000-8000-000000000602
source_id=00000000-0000-4000-8000-000000000603
scan_id=00000000-0000-4000-8000-000000000604
artifact_body='multica-role-source-dr-gate-2026-08-15'
artifact_tail=$(printf '%s' "$artifact_body" | cut -c 2-)
artifact_size=${#artifact_body}
artifact_hex=$(printf '%s' "$artifact_body" | openssl dgst -sha256 | awk '{print $NF}')
artifact_digest="sha256:$artifact_hex"
storage_key="role-source-artifacts/$workspace_id/$artifact_hex"
target_artifact="$target_storage/$storage_key"
interrupt_artifact_bytes=67108864
interrupt_artifact_hex=3b6a07d0d404fab4e23b6d34bc6696a6a312dd92821332385e5af7c01c421351
interrupt_artifact_digest="sha256:$interrupt_artifact_hex"
interrupt_storage_key="role-source-artifacts/$workspace_id/$interrupt_artifact_hex"
source_interrupt_artifact="$source_storage/$interrupt_storage_key"
target_interrupt_artifact="$target_storage/$interrupt_storage_key"
target_interrupt_temporary="$target_storage/role-source-artifacts/$workspace_id/.$interrupt_artifact_hex.tmp"
gate_started=$(date +%s)

cleanup() {
    "$docker_bin" rm -f "$restore_worker" >/dev/null 2>&1 || true
    "$docker_bin" rm -f "$postgres" >/dev/null 2>&1 || true
    "$docker_bin" volume rm "$postgres_volume" >/dev/null 2>&1 || true
    "$docker_bin" network rm "$network" >/dev/null 2>&1 || true
    case "$work_dir" in
        /tmp/multica-rs06-dr-*.??????) rm -rf -- "$work_dir" ;;
        *) echo "refusing unsafe work directory cleanup: $work_dir" >&2 ;;
    esac
}

diagnose_failure() {
    echo "RS-06 local DR gate failed; collecting bounded diagnostics" >&2
    if "$docker_bin" inspect "$postgres" >/dev/null 2>&1; then
        "$docker_bin" inspect --format '{{.Name}}|{{.State.Status}}|{{.State.ExitCode}}|{{.State.Error}}' "$postgres" >&2 || true
        "$docker_bin" logs --tail 40 "$postgres" >&2 || true
    fi
    find "$work_dir" -maxdepth 3 -type f -print >&2 2>/dev/null || true
}

finish() {
    exit_code=$?
    trap - EXIT INT TERM
    if [ "$exit_code" -ne 0 ]; then
        diagnose_failure
    fi
    cleanup
    exit "$exit_code"
}
trap finish EXIT
trap 'exit 130' INT TERM

stage() {
    echo "[rs06-dr] $1"
}

require_image() {
    image=$1
    if ! "$docker_bin" image inspect "$image" >/dev/null 2>&1; then
        "$docker_bin" pull "$image"
    fi
}

wait_for_postgres() {
    count=0
    while :; do
        # The quoted substitutions expand inside the PostgreSQL container.
        # shellcheck disable=SC2016
        if "$docker_bin" exec "$postgres" sh -c \
            'test "$(cat /proc/1/comm)" = postgres && exec psql -v ON_ERROR_STOP=1 -U multica -d postgres -A -t -c "SELECT 1"' \
            >/dev/null 2>&1; then
            return 0
        fi
        count=$((count + 1))
        if [ "$count" -ge 120 ]; then
            echo "timed out waiting for final PostgreSQL postmaster" >&2
            return 1
        fi
        sleep 0.5
    done
}

run_dr() {
    database_url=$1
    storage_dir=$2
    shift 2
    "$docker_bin" run --rm --network "$network" --user "$host_uid:$host_gid" \
        -e DATABASE_URL="$database_url" -e LOCAL_UPLOAD_DIR=/storage \
        -e MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS="$trusted_keys" \
        -v "$work_dir:/dr" -v "$storage_dir:/storage" \
        --entrypoint /app/role_source_dr "$backend_image" "$@"
}

assert_report() {
    report=$1
    expected=$2
    if ! grep -Fq "$expected" "$report"; then
        echo "report $report does not contain $expected" >&2
        return 1
    fi
}

require_image "$pg_image"
require_image "$backend_image"

stage "create isolated PostgreSQL 17 source and restore databases"
mkdir -p "$source_storage/$(dirname "$storage_key")" "$target_storage"
"$docker_bin" network create "$network" >/dev/null
"$docker_bin" volume create "$postgres_volume" >/dev/null
"$docker_bin" run -d --name "$postgres" --network "$network" --network-alias rs06-pg \
    -e POSTGRES_DB="$source_database" -e POSTGRES_USER=multica -e POSTGRES_PASSWORD="$pg_password" \
    -v "$postgres_volume:/var/lib/postgresql/data" "$pg_image" >/dev/null
wait_for_postgres

stage "migrate source database and seed two byte-verified immutable artifacts"
"$docker_bin" run --rm --network "$network" -e DATABASE_URL="$source_url" \
    --entrypoint /app/migrate "$backend_image" up >/dev/null
printf '%s' "$artifact_body" > "$source_storage/$storage_key"
dd if=/dev/zero of="$source_interrupt_artifact" bs=1048576 count=64 2>/dev/null
test "$(wc -c < "$source_interrupt_artifact" | tr -d ' ')" = "$interrupt_artifact_bytes"
test "$(openssl dgst -sha256 "$source_interrupt_artifact" | awk '{print $NF}')" = "$interrupt_artifact_hex"
"$docker_bin" exec "$postgres" psql -v ON_ERROR_STOP=1 -U multica -d "$source_database" \
    -c "INSERT INTO workspace (id,name,slug) VALUES ('$workspace_id','RS-06 DR Gate','rs06-dr-$run_id')" \
    -c "INSERT INTO role_source_artifact (workspace_id,digest,size_bytes,storage_key,uploaded_by_runtime_id,first_source_id,first_scan_request_id) VALUES ('$workspace_id','$artifact_digest',$artifact_size,'$storage_key','$runtime_id','$source_id','$scan_id')" \
    -c "INSERT INTO role_source_artifact_integrity (workspace_id,artifact_digest,storage_key,size_bytes,state,last_outcome,next_check_at,last_checked_at,last_verified_at) VALUES ('$workspace_id','$artifact_digest','$storage_key',$artifact_size,'healthy','healthy',now()+interval '1 day',now(),now())" \
    -c "INSERT INTO role_source_artifact (workspace_id,digest,size_bytes,storage_key,uploaded_by_runtime_id,first_source_id,first_scan_request_id) VALUES ('$workspace_id','$interrupt_artifact_digest',$interrupt_artifact_bytes,'$interrupt_storage_key','$runtime_id','$source_id','$scan_id')" \
    -c "INSERT INTO role_source_artifact_integrity (workspace_id,artifact_digest,storage_key,size_bytes,state,last_outcome,next_check_at,last_checked_at,last_verified_at) VALUES ('$workspace_id','$interrupt_artifact_digest','$interrupt_storage_key',$interrupt_artifact_bytes,'healthy','healthy',now()+interval '1 day',now(),now())" >/dev/null

stage "generate an independent Ed25519 backup signer and create a signed bundle"
"$docker_bin" run --rm --user "$host_uid:$host_gid" -v "$work_dir:/dr" \
    --entrypoint /app/role_source_dr "$backend_image" generate-signing-key \
    --key-id backup-v1 --private-key-file /dr/backup-v1.private --public-key-file /dr/backup-v1.public >/dev/null
private_key=$(tr -d '\r\n' < "$work_dir/backup-v1.private")
public_key=$(tr -d '\r\n' < "$work_dir/backup-v1.public")
trusted_keys="{\"backup-v1\":\"$public_key\"}"
backup_started=$(date +%s)
"$docker_bin" run --rm --network "$network" --user "$host_uid:$host_gid" \
    -e DATABASE_URL="$source_url" -e LOCAL_UPLOAD_DIR=/storage \
    -e MULTICA_ROLE_SOURCE_DR_SIGNING_PROVIDER=private_key \
    -e MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID=backup-v1 \
    -e MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY="$private_key" \
    -v "$work_dir:/dr" -v "$source_storage:/storage:ro" \
    --entrypoint /app/role_source_dr "$backend_image" backup --output-dir /dr/backup
backup_finished=$(date +%s)

test ! -e "$backup_dir/INCOMPLETE"
for required in database.dump artifacts.tar manifest.json; do
    test -s "$backup_dir/$required"
done
grep -Fq '"signature_scheme": "ed25519-sha512-commitment-v2"' "$backup_dir/manifest.json"
# The stat substitutions expand inside the validation container.
# shellcheck disable=SC2016
"$docker_bin" run --rm -v "$work_dir:/dr:ro" --entrypoint /bin/sh "$backend_image" -c \
    'test "$(stat -c %a /dr/backup)" = 700 && test "$(stat -c %a /dr/backup/database.dump)" = 600 && test "$(stat -c %a /dr/backup/artifacts.tar)" = 600 && test "$(stat -c %a /dr/backup/manifest.json)" = 600'

stage "prove failed backup remains visibly incomplete"
if "$docker_bin" run --rm --network "$network" --user "$host_uid:$host_gid" \
    -e DATABASE_URL="$source_url" -e LOCAL_UPLOAD_DIR=/storage \
    -e MULTICA_ROLE_SOURCE_DR_SIGNING_PROVIDER=private_key \
    -e MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID=backup-v1 \
    -e MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY="$private_key" \
    -v "$work_dir:/dr" -v "$source_storage:/storage:ro" \
    --entrypoint /app/role_source_dr "$backend_image" backup --output-dir /dr/failed-backup --pg-dump /bin/false \
    >/dev/null 2>&1; then
    echo "invalid pg_dump unexpectedly produced a successful backup" >&2
    exit 1
fi
test -f "$failed_backup_dir/INCOMPLETE"
test ! -e "$failed_backup_dir/manifest.json"
private_key=

stage "restore the database dump into a fresh database"
restore_started=$(date +%s)
"$docker_bin" exec "$postgres" createdb -U multica "$target_database"
"$docker_bin" run --rm --network "$network" --user "$host_uid:$host_gid" \
    -e PGPASSWORD="$pg_password" -v "$work_dir:/dr:ro" \
    --entrypoint pg_restore "$backend_image" --dbname="$target_safe_url" \
    --no-owner --no-privileges /dr/backup/database.dump

stage "kill artifact restore mid-stream, then resume and verify the complete signed recovery"
"$docker_bin" run -d --name "$restore_worker" --network "$network" --user "$host_uid:$host_gid" --cpus 0.10 \
    -e DATABASE_URL="$target_url" -e LOCAL_UPLOAD_DIR=/storage \
    -e MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS="$trusted_keys" \
    -v "$work_dir:/dr" -v "$target_storage:/storage" \
    --entrypoint /app/role_source_dr "$backend_image" restore-artifacts \
    --manifest /dr/backup/manifest.json --artifact-archive /dr/backup/artifacts.tar >/dev/null
wait_count=0
while [ ! -s "$target_interrupt_temporary" ]; do
    if [ "$("$docker_bin" inspect --format '{{.State.Running}}' "$restore_worker")" != true ]; then
        "$docker_bin" logs --tail 40 "$restore_worker" >&2 || true
        echo "restore worker exited before the process-kill point" >&2
        exit 1
    fi
    wait_count=$((wait_count + 1))
    if [ "$wait_count" -ge 1000 ]; then
        echo "timed out waiting for an in-flight atomic artifact upload" >&2
        exit 1
    fi
    sleep 0.02
done
"$docker_bin" pause "$restore_worker" >/dev/null
test ! -e "$target_interrupt_artifact"
interrupted_bytes=$(wc -c < "$target_interrupt_temporary" | tr -d ' ')
test "$interrupted_bytes" -gt 0
"$docker_bin" kill --signal KILL "$restore_worker" >/dev/null
"$docker_bin" rm "$restore_worker" >/dev/null
test ! -e "$target_interrupt_artifact"
test -s "$target_interrupt_temporary"

run_dr "$target_url" "$target_storage" restore-artifacts \
    --manifest /dr/backup/manifest.json --artifact-archive /dr/backup/artifacts.tar
run_dr "$target_url" "$target_storage" restore-artifacts \
    --manifest /dr/backup/manifest.json --artifact-archive /dr/backup/artifacts.tar
run_dr "$target_url" "$target_storage" verify \
    --manifest /dr/backup/manifest.json --database-dump /dr/backup/database.dump \
    --artifact-archive /dr/backup/artifacts.tar --report /dr/report-valid.json
assert_report "$work_dir/report-valid.json" '"status": "passed"'
assert_report "$work_dir/report-valid.json" '"verified": 2'
restore_finished=$(date +%s)

restored_digest=$(openssl dgst -sha256 "$target_artifact" | awk '{print $NF}')
test "$restored_digest" = "$artifact_hex"
test "$(openssl dgst -sha256 "$target_interrupt_artifact" | awk '{print $NF}')" = "$interrupt_artifact_hex"
test ! -e "$target_interrupt_temporary"
row_state=$("$docker_bin" exec "$postgres" psql -v ON_ERROR_STOP=1 -U multica -d "$target_database" -A -t -F '|' \
    -c "SELECT (SELECT count(*) FROM role_source_artifact),(SELECT count(*) FROM role_source_artifact_integrity),(SELECT state FROM role_source_artifact_integrity WHERE workspace_id='$workspace_id' AND artifact_digest='$artifact_digest')")
test "$row_state" = '2|2|healthy'

stage "prove archive, object and restored-database tamper fail closed"
cp "$backup_dir/artifacts.tar" "$work_dir/artifacts-tampered.tar"
printf 'X' | dd of="$work_dir/artifacts-tampered.tar" bs=1 seek=1024 conv=notrunc >/dev/null 2>&1
if run_dr "$target_url" "$target_storage" verify \
    --manifest /dr/backup/manifest.json --artifact-archive /dr/artifacts-tampered.tar \
    --report /dr/report-archive-tamper.json >/dev/null 2>&1; then
    echo "tampered artifact archive passed verification" >&2
    exit 1
fi
assert_report "$work_dir/report-archive-tamper.json" 'artifact_archive_digest_mismatch'

# STORAGE_KEY expands inside the mutation container.
# shellcheck disable=SC2016
"$docker_bin" run --rm --user "$host_uid:$host_gid" \
    -v "$work_dir:/dr" -v "$target_storage:/storage" -e STORAGE_KEY="$storage_key" \
    --entrypoint /bin/sh "$backend_image" -c \
    'mv "/storage/$STORAGE_KEY" /dr/restored-artifact.saved && test ! -e "/storage/$STORAGE_KEY"'
test ! -e "$target_artifact"
# STORAGE_KEY expands inside the validation container.
# shellcheck disable=SC2016
"$docker_bin" run --rm -v "$target_storage:/storage:ro" -e STORAGE_KEY="$storage_key" \
    --entrypoint /bin/sh "$backend_image" -c 'test ! -e "/storage/$STORAGE_KEY"'
if run_dr "$target_url" "$target_storage" verify \
    --manifest /dr/backup/manifest.json --report /dr/report-object-missing.json >/dev/null 2>&1; then
    echo "missing restored artifact passed verification" >&2
    sed -n '1,160p' "$work_dir/report-object-missing.json" >&2
    exit 1
fi
assert_report "$work_dir/report-object-missing.json" 'artifact_object_missing'

# Fixture variables expand inside the mutation container.
# shellcheck disable=SC2016
"$docker_bin" run --rm --user "$host_uid:$host_gid" \
    -v "$target_storage:/storage" -e STORAGE_KEY="$storage_key" -e ARTIFACT_TAIL="$artifact_tail" \
    --entrypoint /bin/sh "$backend_image" -c 'printf "X%s" "$ARTIFACT_TAIL" > "/storage/$STORAGE_KEY"'
if run_dr "$target_url" "$target_storage" verify \
    --manifest /dr/backup/manifest.json --report /dr/report-object-invalid.json >/dev/null 2>&1; then
    echo "changed restored artifact passed verification" >&2
    exit 1
fi
assert_report "$work_dir/report-object-invalid.json" 'artifact_object_invalid'
# STORAGE_KEY expands inside the mutation container.
# shellcheck disable=SC2016
"$docker_bin" run --rm --user "$host_uid:$host_gid" \
    -v "$work_dir:/dr" -v "$target_storage:/storage" -e STORAGE_KEY="$storage_key" \
    --entrypoint /bin/sh "$backend_image" -c 'mv -f /dr/restored-artifact.saved "/storage/$STORAGE_KEY"'

"$docker_bin" exec "$postgres" psql -v ON_ERROR_STOP=1 -U multica -d "$target_database" \
    -c "UPDATE role_source_artifact_integrity SET attempt=attempt+1 WHERE workspace_id='$workspace_id' AND artifact_digest='$artifact_digest'" >/dev/null
if run_dr "$target_url" "$target_storage" verify \
    --manifest /dr/backup/manifest.json --report /dr/report-database-tamper.json >/dev/null 2>&1; then
    echo "changed restored database passed verification" >&2
    exit 1
fi
assert_report "$work_dir/report-database-tamper.json" 'backup_manifest_mismatch'
"$docker_bin" exec "$postgres" psql -v ON_ERROR_STOP=1 -U multica -d "$target_database" \
    -c "UPDATE role_source_artifact_integrity SET attempt=0 WHERE workspace_id='$workspace_id' AND artifact_digest='$artifact_digest'" >/dev/null

run_dr "$target_url" "$target_storage" verify \
    --manifest /dr/backup/manifest.json --database-dump /dr/backup/database.dump \
    --artifact-archive /dr/backup/artifacts.tar --report /dr/report-final.json >/dev/null
assert_report "$work_dir/report-final.json" '"status": "passed"'

database_dump_bytes=$(wc -c < "$backup_dir/database.dump" | tr -d ' ')
artifact_archive_bytes=$(wc -c < "$backup_dir/artifacts.tar" | tr -d ' ')
manifest_bytes=$(wc -c < "$backup_dir/manifest.json" | tr -d ' ')
bundle_bytes=$((database_dump_bytes + artifact_archive_bytes + manifest_bytes))
gate_finished=$(date +%s)
echo "RS-06 local disaster-recovery gate passed"
echo "postgres=17 signed_manifest=true signature_scheme=ed25519-sha512-commitment-v2 artifacts=2 artifact_bytes=$((artifact_size + interrupt_artifact_bytes))"
echo "restore_process_kill=true interrupted_bytes=$interrupted_bytes atomic_partial_hidden=true resume=true restore_idempotent=true fixture_rows=2 archive_tamper=refused object_missing=refused object_changed=refused database_changed=refused"
echo "failed_backup_incomplete_marker=true"
echo "database_dump_bytes=$database_dump_bytes artifact_archive_bytes=$artifact_archive_bytes manifest_bytes=$manifest_bytes bundle_bytes=$bundle_bytes"
echo "backup_seconds=$((backup_finished - backup_started)) restore_verify_seconds=$((restore_finished - restore_started)) gate_seconds=$((gate_finished - gate_started))"

#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
docker_bin=${DOCKER_BIN:-docker}
run_id=$$
prefix="multica-rs07-failover-$run_id"
network="$prefix"
primary="$prefix-primary"
standby="$prefix-standby"
router="$prefix-router"
redis="$prefix-redis"
backend_a="$prefix-backend-a"
backend_b="$prefix-backend-b"
test_runner="$prefix-test"
basebackup="$prefix-basebackup"
primary_volume="$prefix-primary-data"
standby_volume="$prefix-standby-data"
signal_dir=$(mktemp -d "/tmp/$prefix-signals.XXXXXX")
host_uid=$(id -u)
host_gid=$(id -g)
pg_image=${MULTICA_RS07_POSTGRES_IMAGE:-pgvector/pgvector:pg17}
redis_image=${MULTICA_RS07_REDIS_IMAGE:-redis:7-alpine}
haproxy_image=${MULTICA_RS07_HAPROXY_IMAGE:-haproxy:3.2-alpine}
backend_image=${MULTICA_RS07_BACKEND_IMAGE:-multica-backend:dev}
go_image=${MULTICA_RS07_GO_IMAGE:-golang:1.26-alpine}
pg_password=$(openssl rand -hex 24)
replication_password=$(openssl rand -hex 24)
jwt_secret=$(openssl rand -hex 32)
database_url="postgres://multica:$pg_password@rs07-pg-router:5432/multica?sslmode=disable"

cleanup() {
    for container in "$test_runner" "$backend_b" "$backend_a" "$router" "$redis" "$standby" "$primary" "$basebackup"; do
        "$docker_bin" rm -f "$container" >/dev/null 2>&1 || true
    done
    "$docker_bin" volume rm "$standby_volume" >/dev/null 2>&1 || true
    "$docker_bin" volume rm "$primary_volume" >/dev/null 2>&1 || true
    "$docker_bin" network rm "$network" >/dev/null 2>&1 || true
    rm -f "$signal_dir/prepared" "$signal_dir/start" "$signal_dir/outage_observed" "$signal_dir/verified" "$signal_dir/finish"
    rmdir "$signal_dir" >/dev/null 2>&1 || true
}

diagnose_failure() {
    echo "RS-07 failover gate failed; collecting bounded container diagnostics" >&2
    for container in "$primary" "$standby" "$router" "$redis" "$backend_a" "$backend_b" "$test_runner"; do
        if "$docker_bin" inspect "$container" >/dev/null 2>&1; then
            "$docker_bin" inspect --format '{{.Name}}|{{.State.Status}}|{{.State.ExitCode}}|{{.State.Error}}' "$container" >&2 || true
            "$docker_bin" logs --tail 30 "$container" >&2 || true
        fi
    done
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
    echo "[rs07-failover] $1"
}

require_image() {
    image=$1
    if ! "$docker_bin" image inspect "$image" >/dev/null 2>&1; then
        "$docker_bin" pull "$image"
    fi
}

wait_for_command() {
    label=$1
    shift
    count=0
    until "$@" >/dev/null 2>&1; do
        count=$((count + 1))
        if [ "$count" -ge 120 ]; then
            echo "timed out waiting for $label" >&2
            return 1
        fi
        sleep 0.5
    done
}

wait_for_output() {
    label=$1
    expected=$2
    shift 2
    count=0
    while :; do
        output=$("$@" 2>/dev/null || true)
        if [ "$output" = "$expected" ]; then
            return 0
        fi
        count=$((count + 1))
        if [ "$count" -ge 120 ]; then
            echo "timed out waiting for $label; last output: $output" >&2
            return 1
        fi
        sleep 0.5
    done
}

wait_for_test_file() {
    file=$1
    label=$2
    count=0
    until [ -f "$file" ]; do
        if [ "$("$docker_bin" inspect --format '{{.State.Status}}' "$test_runner")" != "running" ]; then
            "$docker_bin" logs "$test_runner" >&2
            echo "test runner exited before $label" >&2
            return 1
        fi
        count=$((count + 1))
        if [ "$count" -ge 240 ]; then
            echo "timed out waiting for $label" >&2
            return 1
        fi
        sleep 0.25
    done
}

wait_for_exit() {
    container=$1
    count=0
    while [ "$("$docker_bin" inspect --format '{{.State.Status}}' "$container")" = "running" ]; do
        count=$((count + 1))
        if [ "$count" -ge 240 ]; then
            echo "timed out waiting for $container" >&2
            return 1
        fi
        sleep 0.5
    done
}

backend_ready() {
    "$docker_bin" exec "$1" wget -T 3 -qO- http://127.0.0.1:8080/readyz | grep -q '"status":"ok"'
}

postgres_main_ready() {
    container=$1
    database=$2
    # Require the entrypoint's final PID 1, not its temporary init postmaster.
    # shellcheck disable=SC2016
    "$docker_bin" exec "$container" sh -c \
        'test "$(cat /proc/1/comm)" = postgres && exec psql -v ON_ERROR_STOP=1 -U multica -d "$1" -A -t -c "SELECT 1"' sh "$database"
}

require_image "$pg_image"
require_image "$redis_image"
require_image "$haproxy_image"
require_image "$backend_image"
require_image "$go_image"

stage "create isolated network and PostgreSQL volumes"
"$docker_bin" network create "$network" >/dev/null
network_cidr=$("$docker_bin" network inspect --format '{{(index .IPAM.Config 0).Subnet}}' "$network")
case "$network_cidr" in
    */*) ;;
    *) echo "invalid isolated network CIDR: $network_cidr" >&2; exit 1 ;;
esac
"$docker_bin" volume create "$primary_volume" >/dev/null
"$docker_bin" volume create "$standby_volume" >/dev/null

"$docker_bin" run -d --name "$primary" --network "$network" --network-alias rs07-pg-primary \
    -e POSTGRES_DB=multica -e POSTGRES_USER=multica -e POSTGRES_PASSWORD="$pg_password" \
    -v "$primary_volume:/var/lib/postgresql/data" \
    "$pg_image" postgres -c wal_level=replica -c max_wal_senders=10 \
    -c max_replication_slots=10 -c hot_standby=on -c synchronous_commit=on >/dev/null
wait_for_command "primary PostgreSQL" postgres_main_ready "$primary" multica

stage "create replication identity and physical base backup"
"$docker_bin" exec "$primary" psql -v ON_ERROR_STOP=1 -U multica -d postgres \
    -c "CREATE ROLE rs07_replicator WITH REPLICATION LOGIN PASSWORD '$replication_password'" >/dev/null
# The quoted variables expand inside the primary container, not on the host.
# shellcheck disable=SC2016
"$docker_bin" exec "$primary" sh -c \
    'printf "host replication rs07_replicator %s scram-sha-256\n" "$1" >> "$PGDATA/pg_hba.conf"' sh "$network_cidr"
"$docker_bin" exec "$primary" psql -v ON_ERROR_STOP=1 -U multica -d postgres \
    -c "SELECT pg_reload_conf()" >/dev/null

replication_dsn="host=rs07-pg-primary port=5432 user=rs07_replicator password=$replication_password application_name=rs07_standby"
# PGDATA and REPLICATION_DSN expand inside the base-backup container.
# shellcheck disable=SC2016
"$docker_bin" run --rm --name "$basebackup" --network "$network" \
    -e REPLICATION_DSN="$replication_dsn" -v "$standby_volume:/var/lib/postgresql/data" \
    --entrypoint sh "$pg_image" -c \
    'mkdir -p "$PGDATA" && chown -R postgres:postgres "$PGDATA" && exec gosu postgres pg_basebackup -d "$REPLICATION_DSN" -D "$PGDATA" -Fp -Xs -P -R' >/dev/null

"$docker_bin" run -d --name "$standby" --network "$network" --network-alias rs07-pg-standby \
    -v "$standby_volume:/var/lib/postgresql/data" "$pg_image" postgres -c hot_standby=on >/dev/null
wait_for_command "standby PostgreSQL" postgres_main_ready "$standby" multica
wait_for_output "streaming replication" "1" "$docker_bin" exec "$primary" psql -U multica -d postgres -A -t \
    -c "SELECT 1 FROM pg_stat_replication WHERE application_name='rs07_standby' AND state='streaming'"

"$docker_bin" exec "$primary" psql -v ON_ERROR_STOP=1 -U multica -d postgres \
    -c "ALTER SYSTEM SET synchronous_standby_names='FIRST 1 (rs07_standby)'" \
    -c "SELECT pg_reload_conf()" >/dev/null
wait_for_output "synchronous standby" "1" "$docker_bin" exec "$primary" psql -U multica -d postgres -A -t \
    -c "SELECT 1 FROM pg_stat_replication WHERE application_name='rs07_standby' AND sync_state='sync'"

stage "start HAProxy, Redis and two backend replicas"
"$docker_bin" run -d --name "$router" --network "$network" --network-alias rs07-pg-router \
    -v "$script_dir/rs07-haproxy.cfg:/usr/local/etc/haproxy/haproxy.cfg:ro" "$haproxy_image" >/dev/null
"$docker_bin" run -d --name "$redis" --network "$network" --network-alias rs07-redis \
    "$redis_image" redis-server --save '' --appendonly no >/dev/null
wait_for_output "Redis" "PONG" "$docker_bin" exec "$redis" redis-cli ping

for backend in "$backend_a" "$backend_b"; do
    "$docker_bin" run -d --name "$backend" --network "$network" \
        -e DATABASE_URL="$database_url" -e REDIS_URL=redis://rs07-redis:6379 \
        -e JWT_SECRET="$jwt_secret" -e PORT=8080 -e APP_ENV=production \
        -e FRONTEND_ORIGIN=http://localhost:3000 -e ANALYTICS_DISABLED=true \
        "$backend_image" >/dev/null
    wait_for_command "$backend readiness" backend_ready "$backend"
done

stage "prepare one signed reconciliation authorization"
"$docker_bin" run -d --name "$test_runner" --network "$network" --user "$host_uid:$host_gid" \
    -v "$repo_root:/src:ro" -v "$signal_dir:/signals" -w /src/server \
    -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go-path -e DATABASE_URL="$database_url" \
    -e MULTICA_LIVE_CHANNEL_DELIVERY_FAILOVER_TEST=1 \
    -e MULTICA_CHANNEL_DELIVERY_FAILOVER_SIGNAL_DIR=/signals \
    "$go_image" go test ./internal/integrations/delivery \
    -run '^TestChannelDeliveryReconciliationPrimaryFailoverPostgres$' -count=1 -v >/dev/null

wait_for_test_file "$signal_dir/prepared" "prepared reconciliation authorization"
delivery_id=$(cat "$signal_dir/prepared")
case "$delivery_id" in
    ????????-????-????-????-????????????) ;;
    *) echo "invalid fixture delivery id" >&2; exit 1 ;;
esac

stage "hard-stop primary and require an observed write outage"
failover_started=$(date +%s)
"$docker_bin" kill "$primary" >/dev/null
: > "$signal_dir/start"
wait_for_test_file "$signal_dir/outage_observed" "database outage observation"
stage "promote physical standby and converge all authorization retries"
"$docker_bin" exec "$standby" psql -v ON_ERROR_STOP=1 -U multica -d postgres \
    -c "SELECT pg_promote(true, 60)" >/dev/null
wait_for_output "standby promotion" "1" "$docker_bin" exec "$standby" psql -U multica -d postgres -A -t \
    -c "SELECT CASE WHEN pg_is_in_recovery() THEN 0 ELSE 1 END"
wait_for_test_file "$signal_dir/verified" "reconciliation convergence"
failover_recovered=$(date +%s)

row_state=$("$docker_bin" exec "$standby" psql -v ON_ERROR_STOP=1 -U multica -d multica -A -t -F '|' \
    -c "SELECT pg_is_in_recovery(), d.status, d.reconciliation_count, count(r.id) FROM channel_delivery d LEFT JOIN channel_delivery_reconciliation r ON r.delivery_id=d.id WHERE d.id='$delivery_id' GROUP BY d.id, d.status, d.reconciliation_count")
if [ "$row_state" != "f|retry_authorized|1|1" ]; then
    echo "unexpected promoted-primary evidence: $row_state" >&2
    exit 1
fi

stage "release test cleanup and verify both backend replicas"
: > "$signal_dir/finish"
wait_for_exit "$test_runner"
"$docker_bin" logs "$test_runner"
test_exit=$("$docker_bin" inspect --format '{{.State.ExitCode}}' "$test_runner")
if [ "$test_exit" -ne 0 ]; then
    echo "failover live test exited $test_exit" >&2
    exit "$test_exit"
fi

wait_for_command "backend A post-failover readiness" backend_ready "$backend_a"
wait_for_command "backend B post-failover readiness" backend_ready "$backend_b"
wait_for_output "Redis after failover" "PONG" "$docker_bin" exec "$redis" redis-cli ping
residue=$("$docker_bin" exec "$standby" psql -U multica -d multica -A -t \
    -c "SELECT (SELECT count(*) FROM channel_delivery WHERE id='$delivery_id')+(SELECT count(*) FROM channel_delivery_reconciliation WHERE delivery_id='$delivery_id')")
if [ "$residue" -ne 0 ]; then
    echo "failover fixture residue=$residue" >&2
    exit 1
fi

echo "RS-07 local failover gate passed"
echo "postgres=17 physical_standby=promoted synchronous_before_failure=true"
echo "backends=2 redis=shared receipt_rows=1 fixture_residue=0"
echo "orchestrated_failover_seconds=$((failover_recovered - failover_started))"

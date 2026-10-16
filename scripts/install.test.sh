#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Build a self-contained sandbox with stub `curl` and a release tarball.
# No test may contact a release server or replace the user's installed CLI.
_setup_sandbox() {
  local tmp="$1"
  local stub_bin="$tmp/stub-bin"
  local install_bin="$tmp/install-bin"
  local payload_dir="$tmp/payload"
  mkdir -p "$stub_bin" "$install_bin" "$payload_dir" "$tmp/home"

  cat >"$payload_dir/multica" <<'STUB'
#!/usr/bin/env bash
if [[ "${MULTICA_TEST_INCOMPATIBLE_BINARY:-}" == 1 && "$*" == *"--check-compatibility"* ]]; then exit 1; fi
echo "multica v0.3.2 (commit: test)"
STUB
  chmod +x "$payload_dir/multica"
  tar -czf "$tmp/multica.tar.gz" -C "$payload_dir" multica

  cat >"$stub_bin/curl" <<'STUB'
#!/usr/bin/env bash
if [[ -n "${MULTICA_TEST_RELEASE_LOG:-}" ]]; then
  printf '%s\n' "$*" >>"$MULTICA_TEST_RELEASE_LOG"
fi
if [[ "$*" == *"-sI"* || "$*" == *"-fsSI"* ]]; then
  case "${MULTICA_TEST_RELEASE_MODE:-}" in
    no-release)
      printf 'HTTP/2 302\r\nlocation: https://github.com/dunzic/multica-worker/releases\r\n' ;;
    invalid-tag)
      printf 'HTTP/2 302\r\nlocation: https://github.com/dunzic/multica-worker/releases/tag/not-a-version\r\n' ;;
    prerelease)
      printf 'HTTP/2 302\r\nlocation: https://github.com/dunzic/multica-worker/releases/tag/v0.6.4-rc.1\r\n' ;;
    leading-zero)
      printf 'HTTP/2 302\r\nlocation: https://github.com/dunzic/multica-worker/releases/tag/v0.06.4\r\n' ;;
    foreign-repository)
      printf 'HTTP/2 302\r\nlocation: https://github.com/multica-ai/multica/releases/tag/v0.3.2\r\n' ;;
    no-location)
      printf 'HTTP/2 200\r\ncontent-type: text/html\r\n' ;;
    network-error)
      echo 'curl: (7) simulated connection failure' >&2
      exit 7 ;;
    http-error)
      echo 'curl: (22) simulated HTTP 403' >&2
      exit 22 ;;
    mixed-case)
      printf 'HTTP/2 302\r\nLoCaTiOn: https://github.com/dunzic/multica-worker/releases/tag/v0.3.2\r\n' ;;
    *)
      printf 'HTTP/2 302\r\nlocation: https://github.com/dunzic/multica-worker/releases/tag/v0.3.2\r\n' ;;
  esac
  exit 0
fi

out=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    http*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -z "$out" ]]; then
  echo "stub curl expected -o" >&2
  exit 2
fi
if [[ "$url" == *checksums.txt ]]; then
  if [[ "${MULTICA_TEST_RELEASE_MODE:-}" == missing-checksums ]]; then exit 22; fi
  if command -v sha256sum >/dev/null; then
    hash=$(sha256sum "$MULTICA_TEST_ARCHIVE" | awk '{print $1}')
  else
    hash=$(shasum -a 256 "$MULTICA_TEST_ARCHIVE" | awk '{print $1}')
  fi
  if [[ "${MULTICA_TEST_BAD_CHECKSUM:-}" == 1 ]]; then hash=invalid; fi
  for os in darwin linux; do
    for arch in amd64 arm64; do
      printf '%s  multica-cli-0.3.2-%s-%s.tar.gz\n' "$hash" "$os" "$arch"
    done
  done >"$out"
else
  if [[ "${MULTICA_TEST_RELEASE_MODE:-}" == missing-asset ]]; then exit 22; fi
  cp "$MULTICA_TEST_ARCHIVE" "$out"
fi
STUB
  chmod +x "$stub_bin/curl"
}

_invoke_installer() {
  local tmp="$1"
  shift
  : >"$tmp/release.log"
  PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    HOME="$tmp/home" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TEST_RELEASE_LOG="$tmp/release.log" \
    env "$@" bash "$ROOT_DIR/scripts/install.sh" >"$tmp/install.out" 2>"$tmp/install.err"
}

_run_installer() {
  local tmp="$1"
  shift
  local out="$tmp/install.out"
  local err="$tmp/install.err"
  if ! _invoke_installer "$tmp" "$@"; then
    echo "install.sh exited non-zero" >&2
    cat "$out" >&2 || true
    cat "$err" >&2 || true
    return 1
  fi

  if [[ ! -x "$tmp/install-bin/multica" ]]; then
    echo "expected fallback binary at $tmp/install-bin/multica" >&2
    cat "$out" >&2 || true
    cat "$err" >&2 || true
    return 1
  fi

  if ! grep -q "https://github.com/dunzic/multica-worker/releases/download/" "$out"; then
    echo "expected the owned release channel" >&2
    cat "$err" >&2 || true
    return 1
  fi
  if grep -q "via Homebrew" "$out"; then
    echo "installer must not select a community Homebrew package" >&2
    return 1
  fi
}

test_incompatible_binary_preserves_existing_installation() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  _setup_sandbox "$tmp"
  cp "$tmp/payload/multica" "$tmp/install-bin/multica"
  if _invoke_installer "$tmp" MULTICA_TEST_INCOMPATIBLE_BINARY=1; then
    echo "installer must reject an incompatible binary" >&2
    return 1
  fi
  cmp "$tmp/payload/multica" "$tmp/install-bin/multica"
  if [[ -e "$tmp/home/.bashrc" || -e "$tmp/home/.zshrc" ]]; then
    echo "incompatibility must not change shell startup files" >&2
    return 1
  fi
}

test_invalid_release_metadata_stops_before_download() {
  local tmp mode expected
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  _setup_sandbox "$tmp"
  cp "$tmp/payload/multica" "$tmp/install-bin/multica"

  while IFS='|' read -r mode expected; do
    if _invoke_installer "$tmp" MULTICA_TEST_RELEASE_MODE="$mode"; then
      echo "installer must reject $mode" >&2
      return 1
    fi
    if ! grep -q "$expected" "$tmp/install.err"; then
      echo "$mode: expected diagnostic: $expected" >&2
      cat "$tmp/install.err" >&2
      return 1
    fi
    if grep -q '/releases/download/' "$tmp/release.log"; then
      echo "$mode: invalid metadata must not trigger an asset download" >&2
      return 1
    fi
    cmp "$tmp/payload/multica" "$tmp/install-bin/multica"
    if [[ -e "$tmp/home/.bashrc" || -e "$tmp/home/.zshrc" ]]; then
      echo "$mode: failure must not modify shell startup files" >&2
      return 1
    fi
  done <<'CASES'
no-release|No published CLI release
invalid-tag|Invalid release tag
prerelease|Invalid release tag
leading-zero|Invalid release tag
foreign-repository|Unexpected release redirect
no-location|Unexpected release redirect
network-error|Could not check releases
http-error|Could not check releases
CASES
}

test_missing_release_assets_preserve_existing_installation() {
  local tmp mode expected
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  _setup_sandbox "$tmp"
  cp "$tmp/payload/multica" "$tmp/install-bin/multica"

  while IFS='|' read -r mode expected; do
    if _invoke_installer "$tmp" MULTICA_TEST_RELEASE_MODE="$mode"; then
      echo "installer must reject $mode" >&2
      return 1
    fi
    if ! grep -q "$expected" "$tmp/install.err"; then
      echo "$mode: expected diagnostic: $expected" >&2
      cat "$tmp/install.err" >&2
      return 1
    fi
    cmp "$tmp/payload/multica" "$tmp/install-bin/multica"
    if [[ -e "$tmp/home/.bashrc" || -e "$tmp/home/.zshrc" ]]; then
      echo "$mode: failure must not modify shell startup files" >&2
      return 1
    fi
  done <<'CASES'
missing-asset|Could not download CLI asset
missing-checksums|Could not download checksums
CASES
}

test_release_redirect_headers_are_case_insensitive() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  _setup_sandbox "$tmp"
  _run_installer "$tmp" MULTICA_TEST_RELEASE_MODE=mixed-case
  if ! grep -q '/download/v0.3.2/multica-cli-0.3.2-' "$tmp/release.log"; then
    echo 'expected a versioned CLI archive URL' >&2
    return 1
  fi
}

test_installs_owned_binary_without_homebrew() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    exit 0
    ;;
  install)
    echo "simulated brew install failure" >&2
    exit 42
    ;;
  list)
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  _run_installer "$tmp"
}

test_replaces_same_version_community_binary() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    echo "simulated brew tap failure" >&2
    exit 17
    ;;
  *)
    echo "brew $* should not be reached after tap failure" >&2
    exit 99
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  cp "$tmp/payload/multica" "$tmp/stub-bin/multica"
  _run_installer "$tmp"
}

test_corrupted_archive_is_not_installed() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  _setup_sandbox "$tmp"
  if PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    HOME="$tmp/home" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TEST_BAD_CHECKSUM=1 \
    bash "$ROOT_DIR/scripts/install.sh" >"$tmp/install.out" 2>"$tmp/install.err"; then
    echo "installer accepted an invalid checksum" >&2
    return 1
  fi
  if [ -e "$tmp/install-bin/multica" ] || ! grep -q "incorrect archive checksum" "$tmp/install.err"; then
    echo "checksum failure must stop before installing" >&2
    return 1
  fi
}

test_remote_ssh_install_prints_token_login_hint() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    exit 0
    ;;
  install)
    echo "simulated brew install failure" >&2
    exit 42
    ;;
  list)
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  (
    export SSH_CONNECTION="192.0.2.10 54321 198.51.100.20 22"
    _run_installer "$tmp"
  )

  if ! grep -q "Looks like a remote/SSH session" "$tmp/install.out"; then
    echo "expected remote/SSH token-login hint in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if ! grep -q "https://workers.turingomni.com/settings?tab=tokens" "$tmp/install.out"; then
    echo "expected direct API Tokens settings URL in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if ! grep -q "Settings > API Tokens" "$tmp/install.out"; then
    echo "expected API Tokens tab name in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if ! grep -q "multica --profile turingomni login --token <YOUR_TOKEN>" "$tmp/install.out"; then
    echo "expected token login command in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if grep -q "multica config set server_url" "$tmp/install.out"; then
    echo "did not expect default cloud server config command in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if grep -q "multica config set app_url" "$tmp/install.out"; then
    echo "did not expect default cloud app config command in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
}

test_local_install_does_not_print_token_login_hint() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    exit 0
    ;;
  install)
    echo "simulated brew install failure" >&2
    exit 42
    ;;
  list)
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  (
    unset SSH_CONNECTION SSH_CLIENT SSH_TTY
    _run_installer "$tmp"
  )

  if grep -q "Looks like a remote/SSH session" "$tmp/install.out"; then
    echo "did not expect remote/SSH token-login hint in local installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if grep -q "multica --profile turingomni login --token <YOUR_TOKEN>" "$tmp/install.out"; then
    echo "did not expect token login command in local installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
}

# ---------------------------------------------------------------------------
# --with-server: the probed port and the printed port must both be the port
# Docker Compose reported (#6145)
#
# The installer used to derive the port from .env with its own copy of the alias
# chain. Compose gives the *calling environment* precedence over .env, so any
# ambient PORT / BACKEND_PORT / API_PORT / SERVER_PORT / FRONTEND_PORT moved the
# published port while the installer kept probing and printing the file value.
#
# Here the docker stub plays Compose: it answers `port` from the same resolution
# Compose performs, environment first, then .env. The installer must take that
# answer as given for both the health check and the summary — which is exactly
# what a .env-only derivation cannot do, because the two disagree in every case
# below. That real Compose resolves this way is proven separately, against real
# `docker compose config`, in scripts/selfhost-config.test.sh; that test needs a
# Docker CLI, which this job deliberately does not require.
# ---------------------------------------------------------------------------
_setup_server_sandbox() {
  local tmp="$1"
  _setup_sandbox "$tmp"
  mv "$tmp/stub-bin/curl" "$tmp/stub-bin/release-curl"
  local stub_bin="$tmp/stub-bin"
  local server_dir="$tmp/server"
  mkdir -p "$stub_bin" "$server_dir/.git"

  # Minimal self-host assets: only the port mapping matters here.
  cat >"$server_dir/.env.example" <<'ENVFILE'
PORT=8080
# BACKEND_PORT=8080
# API_PORT=8080
# SERVER_PORT=8080
FRONTEND_PORT=3000
JWT_SECRET=change-me-in-production
POSTGRES_PASSWORD=multica
DATABASE_URL=postgres://multica:multica@localhost:5432/multica?sslmode=disable
ENVFILE
  touch "$server_dir/docker-compose.selfhost.yml"

  # Compose stand-in. Resolves the published host port the way Compose does:
  # the process environment wins over .env, then the alias chain decides.
  cat >"$stub_bin/docker" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail

_env_file_value() {
  local key="$1" line
  line="$(grep -E "^${key}=" .env 2>/dev/null | tail -n 1 || true)"
  [ -n "$line" ] || return 1
  printf '%s' "${line#*=}"
}

# Environment first (Compose interpolation), then the env file.
_resolve() {
  local key="$1" from_env
  eval "from_env=\${$key-__unset__}"
  if [ "$from_env" != "__unset__" ]; then
    printf '%s' "$from_env"
    return 0
  fi
  _env_file_value "$key"
}

_published_backend_port() {
  local value
  for key in BACKEND_PORT API_PORT SERVER_PORT PORT; do
    if value="$(_resolve "$key")" && [ -n "$value" ]; then
      printf '%s' "$value"
      return
    fi
  done
  printf '8080'
}

_published_frontend_port() {
  local value
  if value="$(_resolve FRONTEND_PORT)" && [ -n "$value" ]; then
    printf '%s' "$value"
    return
  fi
  printf '3000'
}

case "${1:-}" in
  info) exit 0 ;;
  compose)
    shift
    subcommand=""
    for arg in "$@"; do
      case "$arg" in
        pull | up | port | version | ps | logs | down) subcommand="$arg"; break ;;
      esac
    done
    case "$subcommand" in
      port)
        service=""
        for arg in "$@"; do
          case "$arg" in
            backend | frontend) service="$arg"; break ;;
          esac
        done
        case "$service" in
          backend) printf '127.0.0.1:%s\n' "$(_published_backend_port)" ;;
          frontend) printf '127.0.0.1:%s\n' "$(_published_frontend_port)" ;;
          *) exit 1 ;;
        esac
        ;;
      version) echo "2.30.0" ;;
    esac
    exit 0
    ;;
esac
exit 0
STUB
  chmod +x "$stub_bin/docker"

  # git: the installer takes the "existing installation" path, so only the
  # fetch/checkout calls run and they are all tolerant of failure.
  printf '#!/usr/bin/env bash\nexit 0\n' >"$stub_bin/git"
  chmod +x "$stub_bin/git"

  # brew: pretend the CLI installs cleanly so the run reaches the summary.
  printf '#!/usr/bin/env bash\nexit 0\n' >"$stub_bin/brew"
  chmod +x "$stub_bin/brew"

  printf '#!/usr/bin/env bash\necho "multica v0.3.2 (commit: test)"\n' >"$stub_bin/multica"
  chmod +x "$stub_bin/multica"

  # curl records every probed URL so the health-check port can be asserted.
  cat >"$stub_bin/curl" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
for arg in "$@"; do
  case "$arg" in
    http://localhost*) printf '%s\n' "$arg" >>"$MULTICA_TEST_CURL_LOG"; exit 0 ;;
  esac
done
exec "$(dirname "$0")/release-curl" "$@"
STUB
  chmod +x "$stub_bin/curl"

  printf '#!/usr/bin/env bash\nhead -c 32 /dev/zero | od -An -tx1 | tr -d " \\n"\n' >"$stub_bin/openssl"
  chmod +x "$stub_bin/openssl"
}

# Runs `install.sh --with-server` with the sandbox stubs. Remaining arguments are
# ambient environment assignments, so each case controls the environment
# explicitly instead of inheriting a CI runner's PORT.
_run_with_server() {
  local tmp="$1"
  shift

  : >"$tmp/curl.log"
  if ! env -i \
    PATH="$tmp/stub-bin:/usr/bin:/bin" \
    HOME="$tmp" \
    MULTICA_INSTALL_DIR="$tmp/server" \
    MULTICA_SELFHOST_REF="main" \
    MULTICA_TEST_CURL_LOG="$tmp/curl.log" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    "$@" \
    bash "$ROOT_DIR/scripts/install.sh" --with-server \
    >"$tmp/install.out" 2>"$tmp/install.err"; then
    echo "install.sh --with-server exited non-zero" >&2
    cat "$tmp/install.out" >&2 || true
    cat "$tmp/install.err" >&2 || true
    return 1
  fi
}

# Asserts the probed port and the printed ports all match the stub's answer.
_require_server_ports() {
  local tmp="$1" label="$2" expected_backend="$3" expected_frontend="$4"
  local probed printed_backend printed_frontend

  probed="$(sed -n '1s#.*localhost:\([0-9]*\)/health#\1#p' "$tmp/curl.log")"
  printed_backend="$(sed -n 's#.*Backend:[^0-9]*http://localhost:\([0-9]*\).*#\1#p' "$tmp/install.out" | head -n 1)"
  printed_frontend="$(sed -n 's#.*Frontend:[^0-9]*http://localhost:\([0-9]*\).*#\1#p' "$tmp/install.out" | head -n 1)"

  if [ "$probed" != "$expected_backend" ] ||
    [ "$printed_backend" != "$expected_backend" ] ||
    [ "$printed_frontend" != "$expected_frontend" ]; then
    echo "[$label] installer ports disagree with the port Compose published" >&2
    echo "  compose published:  backend=$expected_backend frontend=$expected_frontend" >&2
    echo "  health check probed: ${probed:-<none>}" >&2
    echo "  printed:            backend=${printed_backend:-<none>} frontend=${printed_frontend:-<none>}" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
}

test_with_server_uses_compose_published_ports() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # label | .env mutation (sed) | ambient env | expected backend | expected frontend
  local cases='defaults|||8080|3000
env-file PORT|s/^PORT=8080/PORT=9100/||9100|3000
env-file BACKEND_PORT|s/^# BACKEND_PORT=8080/BACKEND_PORT=9200/||9200|3000
env-file API_PORT|s/^# API_PORT=8080/API_PORT=9300/||9300|3000
env-file SERVER_PORT|s/^# SERVER_PORT=8080/SERVER_PORT=9400/||9400|3000
env-file FRONTEND_PORT|s/^FRONTEND_PORT=3000/FRONTEND_PORT=3100/||8080|3100
ambient PORT beats .env|s/^PORT=8080/PORT=9100/|PORT=9500|9500|3000
ambient BACKEND_PORT beats .env|s/^PORT=8080/PORT=9100/|BACKEND_PORT=9600|9600|3000
ambient API_PORT beats .env|s/^PORT=8080/PORT=9100/|API_PORT=9700|9700|3000
ambient SERVER_PORT beats .env|s/^PORT=8080/PORT=9100/|SERVER_PORT=9800|9800|3000
ambient FRONTEND_PORT beats .env|s/^FRONTEND_PORT=3000/FRONTEND_PORT=3100/|FRONTEND_PORT=3200|8080|3200
empty ambient BACKEND_PORT falls through|s/^PORT=8080/PORT=9100/|BACKEND_PORT=|9100|3000
empty env-file BACKEND_PORT falls through|s/^PORT=8080/PORT=9100/;s/^# BACKEND_PORT=8080/BACKEND_PORT=/||9100|3000'

  local label mutation ambient expect_backend expect_frontend
  while IFS='|' read -r label mutation ambient expect_backend expect_frontend; do
    [ -n "$label" ] || continue

    rm -rf "$tmp/server" "$tmp/stub-bin"
    _setup_server_sandbox "$tmp"
    cp "$tmp/server/.env.example" "$tmp/server/.env"
    if [ -n "$mutation" ]; then
      sed "$mutation" "$tmp/server/.env" >"$tmp/server/.env.new"
      mv "$tmp/server/.env.new" "$tmp/server/.env"
    fi

    if [ -n "$ambient" ]; then
      _run_with_server "$tmp" "$ambient" || return 1
    else
      _run_with_server "$tmp" || return 1
    fi
    _require_server_ports "$tmp" "$label" "$expect_backend" "$expect_frontend" || return 1
  done <<<"$cases"
}

test_with_server_fails_when_compose_port_is_unavailable() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_server_sandbox "$tmp"
  cp "$tmp/server/.env.example" "$tmp/server/.env"

  # Compose cannot report a port, e.g. the container never came up.
  cat >"$tmp/stub-bin/docker" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
case "${1:-}" in
  info) exit 0 ;;
  compose)
    for arg in "$@"; do
      case "$arg" in
        port) exit 1 ;;
        version) echo "2.30.0"; exit 0 ;;
      esac
    done
    exit 0
    ;;
esac
exit 0
STUB
  chmod +x "$tmp/stub-bin/docker"

  if _run_with_server "$tmp" >/dev/null 2>&1; then
    echo "installer must not report success when Compose cannot report the port" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if ! grep -q "could not read the backend host port" "$tmp/install.err"; then
    echo "expected an explicit failure about the backend host port" >&2
    cat "$tmp/install.err" >&2 || true
    return 1
  fi
  if grep -q "server is running and CLI is ready" "$tmp/install.out"; then
    echo "installer claimed success despite an unresolved port" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
}

test_installs_owned_binary_without_homebrew
test_incompatible_binary_preserves_existing_installation
test_replaces_same_version_community_binary
test_corrupted_archive_is_not_installed
test_remote_ssh_install_prints_token_login_hint
test_local_install_does_not_print_token_login_hint
test_invalid_release_metadata_stops_before_download
test_missing_release_assets_preserve_existing_installation
test_release_redirect_headers_are_case_insensitive
test_with_server_uses_compose_published_ports
test_with_server_fails_when_compose_port_is_unavailable
echo "install.sh tests passed"

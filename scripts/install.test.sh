#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Build a self-contained sandbox with stub `curl` and a tarball that the
# release-binary fallback path will download. Each test supplies its own
# `brew` stub to model a specific Homebrew failure mode.
_setup_sandbox() {
  local tmp="$1"
  local stub_bin="$tmp/stub-bin"
  local install_bin="$tmp/install-bin"
  local payload_dir="$tmp/payload"
  mkdir -p "$stub_bin" "$install_bin" "$payload_dir"

  cat >"$payload_dir/multica" <<'STUB'
#!/usr/bin/env bash
echo "multica v0.3.2 (commit: test)"
STUB
  chmod +x "$payload_dir/multica"
  tar -czf "$tmp/multica.tar.gz" -C "$payload_dir" multica
  printf '%s  multica-cli-0.3.2-%s-%s.tar.gz\n' \
    "$(sha256sum "$tmp/multica.tar.gz" | awk '{print $1}')" \
    "$(uname -s | tr '[:upper:]' '[:lower:]')" \
    "$(case "$(uname -m)" in x86_64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) uname -m ;; esac)" \
    > "$tmp/checksums.txt"

  cat >"$stub_bin/curl" <<'STUB'
#!/usr/bin/env bash
if [[ "$*" == *"-sI"* ]]; then
  printf 'HTTP/2 302\r\nlocation: https://github.com/multica-ai/multica/releases/tag/v0.3.2\r\n'
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
    http://*|https://*)
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
if [[ "$url" == *"checksums.txt" ]]; then
  cp "$MULTICA_TEST_CHECKSUMS" "$out"
else
  cp "$MULTICA_TEST_ARCHIVE" "$out"
fi
STUB
  chmod +x "$stub_bin/curl"
}

_run_installer() {
  local tmp="$1"
  local out="$tmp/install.out"
  local err="$tmp/install.err"
  if ! PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TEST_CHECKSUMS="$tmp/checksums.txt" \
    bash "$ROOT_DIR/scripts/install.sh" >"$out" 2>"$err"; then
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

  if ! grep -q "Homebrew output (last 80 lines):" "$err"; then
    echo "expected diagnostic tail in stderr" >&2
    cat "$err" >&2 || true
    return 1
  fi
}

test_checksum_mismatch_blocks_install() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  printf '%064d  multica-cli-0.3.2-%s-%s.tar.gz\n' 0 \
    "$(uname -s | tr '[:upper:]' '[:lower:]')" \
    "$(case "$(uname -m)" in x86_64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) uname -m ;; esac)" \
    > "$tmp/checksums.txt"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
exit 1
STUB
  chmod +x "$tmp/stub-bin/brew"

  if PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TEST_CHECKSUMS="$tmp/checksums.txt" \
    bash "$ROOT_DIR/scripts/install.sh" >"$tmp/install.out" 2>"$tmp/install.err"; then
    echo "installer accepted a mismatched checksum" >&2
    return 1
  fi
  if [[ -e "$tmp/install-bin/multica" ]]; then
    echo "installer wrote the binary before checksum verification" >&2
    return 1
  fi
}

test_brew_install_failure_falls_back_to_release_binary() {
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

test_brew_tap_failure_falls_back_to_release_binary() {
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

  _run_installer "$tmp"
}

test_brew_install_failure_falls_back_to_release_binary
test_brew_tap_failure_falls_back_to_release_binary
test_checksum_mismatch_blocks_install
echo "install.sh tests passed"

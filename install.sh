#!/bin/sh
# Talon installer.
#
# Downloads the release asset matching your OS and architecture, verifies it
# against the published SHA-256 checksum, and installs it into a directory on
# your PATH. Nothing downloaded is executed before it has been verified.
#
#   curl -fsSL https://raw.githubusercontent.com/CRISTOP-bot/talon/main/install.sh | sh
#
# Environment:
#   TALON_VERSION   release tag to install (default: the latest release)
#   TALON_REPO      owner/name (default: CRISTOP-bot/talon)
#   TALON_INSTALL   install directory (default: /usr/local/bin, or ~/.local/bin)
#   TALON_NOCHECK   set to 1 to skip checksum verification (not recommended)

set -eu

REPO="${TALON_REPO:-CRISTOP-bot/talon}"
PKG="github.com/$REPO/cmd/talon"
API="https://api.github.com"
INSTALL_DIR="${TALON_INSTALL:-}"

log()  { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

detect_platform() {
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux|darwin) : ;;
        *) fail "unsupported OS: $os (build from source instead: https://github.com/$REPO)" ;;
    esac
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) fail "unsupported architecture: $arch" ;;
    esac
    printf '%s-%s' "$os" "$arch"
}

fetch() {
    # fetch <url> ; writes the body to stdout
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "$1"
    else
        fail "curl or wget is required"
    fi
}

fetch_to() {
    # fetch_to <url> <destination>
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$2" "$1"
    else
        fail "curl or wget is required"
    fi
}

# fetch_release asks the API for a release and reports why it failed, because
# "no releases yet" and "the network is down" need very different responses.
# Sets api_status and api_body.
fetch_release() {
    api_body=$(mktemp)
    if command -v curl >/dev/null 2>&1; then
        api_status=$(curl -sSL -o "$api_body" -w '%{http_code}' "$1" || printf '000')
    elif command -v wget >/dev/null 2>&1; then
        if wget -qO "$api_body" "$1"; then
            api_status=200
        else
            api_status=000
        fi
    else
        fail "curl or wget is required"
    fi
}

explain_release_failure() {
    case "$api_status" in
        404)
            if [ -n "${TALON_VERSION:-}" ]; then
                fail "there is no release tagged $TALON_VERSION in $REPO"
            fi
            fail "there is no published release in $REPO yet.
  Nothing is installed when there is nothing to verify.
  Build from source instead:
    go install $PKG@latest"
            ;;
        403|429)
            fail "the GitHub API refused the request (HTTP $api_status), usually a rate limit.
  Wait a minute and try again, or install from source:
    go install $PKG@latest"
            ;;
        000)
            fail "could not reach the GitHub API. Check your network or proxy settings."
            ;;
        *)
            fail "the GitHub API returned HTTP $api_status for $1"
            ;;
    esac
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        fail "sha256sum or shasum is required to verify the download"
    fi
}

pick_install_dir() {
    if [ -n "$INSTALL_DIR" ]; then
        printf '%s' "$INSTALL_DIR"
        return
    fi
    if [ "$(id -u)" = "0" ] && [ -d /usr/local/bin ]; then
        printf '%s' "/usr/local/bin"
        return
    fi
    printf '%s' "$HOME/.local/bin"
}

main() {
    platform=$(detect_platform)
    log "Talon installer → $platform"

    if [ -n "${TALON_VERSION:-}" ]; then
        release_url="$API/repos/$REPO/releases/tags/$TALON_VERSION"
    else
        release_url="$API/repos/$REPO/releases/latest"
    fi

    fetch_release "$release_url"
    [ "$api_status" = "200" ] || explain_release_failure "$release_url"
    release_json=$(cat "$api_body")
    rm -f "$api_body"
    tag=$(printf '%s' "$release_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
    [ -n "$tag" ] || fail "could not read the release tag from the GitHub API response"

    asset_name="talon-${tag}-${platform}"

    log "release: $tag"
    log "asset:   $asset_name"

    tmp=$(mktemp -d)
    # shellcheck disable=SC2064
    trap "rm -rf '$tmp'" EXIT

    download_url="https://github.com/$REPO/releases/download/$tag/$asset_name"
    fetch_to "$download_url" "$tmp/talon" || fail "download failed: $download_url"

    if [ "${TALON_NOCHECK:-0}" = "1" ]; then
        log "checksum verification skipped (TALON_NOCHECK=1)"
    else
        if fetch_to "https://github.com/$REPO/releases/download/$tag/checksums.txt" "$tmp/checksums.txt"; then
            expected=$(awk -v name="$asset_name" '$2 == name || $2 == "*"name {print $1}' "$tmp/checksums.txt" | head -n1)
            if [ -z "$expected" ]; then
                fail "no checksum published for $asset_name; refusing to install"
            fi
            actual=$(sha256_of "$tmp/talon")
            if [ "$expected" != "$actual" ]; then
                fail "checksum mismatch for $asset_name (expected $expected, got $actual)"
            fi
            log "checksum verified"
        else
            fail "checksums.txt is missing from the release; refusing to install an unverified binary"
        fi
    fi

    chmod +x "$tmp/talon"
    dir=$(pick_install_dir)
    mkdir -p "$dir"
    cp "$tmp/talon" "$dir/talon.tmp"
    mv "$dir/talon.tmp" "$dir/talon"
    log "installed $dir/talon"

    case ":$PATH:" in
        *":$dir:"*) ;;
        *) log ""
           log "$dir is not on your PATH. Add this to your shell profile:"
           log "  export PATH=\"$dir:\$PATH\""
           log "" ;;
    esac

    log ""
    log "next steps:"
    log "  talon doctor                 # check the environment"
    log "  export AI_API_KEY=...        # or configure a local model"
    log "  cd your-project && talon"
}

main "$@"

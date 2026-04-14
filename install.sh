#!/usr/bin/env bash
# install.sh — install hn-parser as a daily systemd user timer.
#
# Idempotent: safe to re-run. Only restarts the timer if unit files changed.
#
# Flags:
#   --schedule <OnCalendar>   systemd OnCalendar expression (default: daily)
#   --binary <path>           path to the hn-parser binary (default: ./hn-parser)
#   --uninstall               remove the service, timer, and unit files
#   --no-linger               skip `loginctl enable-linger`
#   -h | --help               show this help

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

SCHEDULE="daily"
BINARY="${SCRIPT_DIR}/hn-parser"
UNINSTALL=0
SKIP_LINGER=0

usage() {
    sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --schedule)    SCHEDULE="$2"; shift 2 ;;
        --binary)      BINARY="$2"; shift 2 ;;
        --uninstall)   UNINSTALL=1; shift ;;
        --no-linger)   SKIP_LINGER=1; shift ;;
        -h|--help)     usage; exit 0 ;;
        *)             echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
    esac
done

UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
SERVICE_UNIT="${UNIT_DIR}/hn-parser.service"
TIMER_UNIT="${UNIT_DIR}/hn-parser.timer"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()  { printf '\033[1;32m ok\033[0m  %s\n' "$*"; }
warn(){ printf '\033[1;33m !!\033[0m  %s\n' "$*" >&2; }

# ---- uninstall -------------------------------------------------------------

if [[ $UNINSTALL -eq 1 ]]; then
    log "stopping and disabling timer"
    systemctl --user disable --now hn-parser.timer 2>/dev/null || true
    systemctl --user stop hn-parser.service 2>/dev/null || true

    for f in "$SERVICE_UNIT" "$TIMER_UNIT"; do
        if [[ -f "$f" ]]; then
            rm -f "$f"
            ok "removed $f"
        fi
    done

    systemctl --user daemon-reload
    ok "uninstalled"
    exit 0
fi

# ---- preflight -------------------------------------------------------------

if ! command -v systemctl >/dev/null 2>&1; then
    warn "systemctl not found — this script needs systemd"; exit 1
fi

if ! systemctl --user show-environment >/dev/null 2>&1; then
    warn "systemd user instance not reachable (try: loginctl enable-linger \$USER)"; exit 1
fi

if [[ ! -x "$BINARY" ]]; then
    if [[ -f "${SCRIPT_DIR}/main.go" ]]; then
        log "binary not found at $BINARY — building"
        ( cd "$SCRIPT_DIR" && go build -o hn-parser . )
        ok "built $BINARY"
    else
        warn "binary not found and no main.go to build from: $BINARY"; exit 1
    fi
fi

BINARY_ABS="$(readlink -f -- "$BINARY")"

# ---- render units to tempfiles --------------------------------------------

mkdir -p "$UNIT_DIR"

SERVICE_CONTENT="$(cat <<EOF
[Unit]
Description=Publish HN top-stories digest to denislee.github.io
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${BINARY_ABS}
# Uncomment if your SSH key is passphrase-protected and you rely on ssh-agent:
# Environment=SSH_AUTH_SOCK=%t/ssh-agent.socket
Nice=10
EOF
)"

TIMER_CONTENT="$(cat <<EOF
[Unit]
Description=Run hn-parser on a schedule

[Timer]
OnCalendar=${SCHEDULE}
Persistent=true
RandomizedDelaySec=30m
Unit=hn-parser.service

[Install]
WantedBy=timers.target
EOF
)"

# ---- write only if changed -------------------------------------------------

write_if_changed() {
    local path="$1" content="$2" changed_var="$3"
    local tmp
    tmp="$(mktemp)"
    printf '%s\n' "$content" > "$tmp"
    if [[ -f "$path" ]] && cmp -s "$tmp" "$path"; then
        rm -f "$tmp"
        ok "$path unchanged"
        printf -v "$changed_var" '%s' "0"
    else
        mv -f "$tmp" "$path"
        chmod 0644 "$path"
        ok "wrote $path"
        printf -v "$changed_var" '%s' "1"
    fi
}

write_if_changed "$SERVICE_UNIT" "$SERVICE_CONTENT" SERVICE_CHANGED
write_if_changed "$TIMER_UNIT"   "$TIMER_CONTENT"   TIMER_CHANGED

# ---- reload / enable / start ----------------------------------------------

if [[ "$SERVICE_CHANGED" == "1" || "$TIMER_CHANGED" == "1" ]]; then
    log "reloading systemd user daemon"
    systemctl --user daemon-reload
fi

if ! systemctl --user is-enabled --quiet hn-parser.timer 2>/dev/null; then
    log "enabling hn-parser.timer"
    systemctl --user enable hn-parser.timer
else
    ok "hn-parser.timer already enabled"
fi

if ! systemctl --user is-active --quiet hn-parser.timer; then
    log "starting hn-parser.timer"
    systemctl --user start hn-parser.timer
elif [[ "$TIMER_CHANGED" == "1" ]]; then
    log "restarting hn-parser.timer (unit changed)"
    systemctl --user restart hn-parser.timer
else
    ok "hn-parser.timer already active"
fi

# ---- linger (so the timer runs even when you aren't logged in) ------------

if [[ $SKIP_LINGER -eq 0 ]]; then
    if command -v loginctl >/dev/null 2>&1; then
        linger_state="$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo no)"
        if [[ "$linger_state" != "yes" ]]; then
            log "enabling linger for $USER (may prompt for sudo)"
            if sudo loginctl enable-linger "$USER"; then
                ok "linger enabled"
            else
                warn "could not enable linger — timer will only run while you're logged in"
            fi
        else
            ok "linger already enabled"
        fi
    fi
fi

# ---- summary ---------------------------------------------------------------

echo
log "status"
systemctl --user list-timers hn-parser.timer --no-pager || true
echo
echo "useful commands:"
echo "  journalctl --user -u hn-parser.service -n 50"
echo "  systemctl --user start hn-parser.service   # run once, now"
echo "  $0 --uninstall                             # remove"

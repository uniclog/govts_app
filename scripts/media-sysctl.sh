#!/usr/bin/env bash
# Raise net.core.rmem_max so the media server can reserve its 4 MiB UDP receive
# buffer (apps/server/internal/media/manager.go mediaReadBuffer). A larger existing limit is
# kept. rmem_default is never changed. Run as root before (re)starting the
# server: existing sockets keep the buffer size they were created with.
#
#   media-sysctl.sh apply     persist via sysctl.d and save the previous value
#   media-sysctl.sh rollback  remove the sysctl.d file and restore the saved value
set -Eeuo pipefail

REQUIRED=4194304
CONF="${CONF:-/etc/sysctl.d/90-sonoryx-media.conf}"
BACKUP="${BACKUP:-/etc/sysctl.d/.sonoryx-media.rmem_max.previous}"

current="$(sysctl -n net.core.rmem_max)"

case "${1:-}" in
  apply)
    [[ -f "$BACKUP" ]] || echo "$current" > "$BACKUP"
    if (( current >= REQUIRED )); then
      echo "net.core.rmem_max=$current already >= $REQUIRED; unchanged"
      exit 0
    fi
    printf '# sonoryx media UDP receive buffer; see scripts/media-sysctl.sh\nnet.core.rmem_max = %d\n' "$REQUIRED" > "$CONF"
    sysctl -q -w "net.core.rmem_max=$REQUIRED"
    echo "net.core.rmem_max: $current -> $REQUIRED (previous saved in $BACKUP)"
    ;;
  rollback)
    rm -f "$CONF"
    if [[ -f "$BACKUP" ]]; then
      previous="$(cat "$BACKUP")"
      sysctl -q -w "net.core.rmem_max=$previous"
      rm -f "$BACKUP"
      echo "net.core.rmem_max: $current -> $previous"
    else
      echo "no saved value; net.core.rmem_max=$current unchanged"
    fi
    ;;
  *)
    echo "usage: $0 apply|rollback" >&2
    exit 2
    ;;
esac

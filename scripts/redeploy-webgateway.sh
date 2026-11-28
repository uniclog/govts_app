#!/usr/bin/env bash
set -Eeuo pipefail

root="${1:?installation directory required}"
service="${2:?systemd unit required}"
archive="${3:?archive required}"
component="${4:-all}"
case "$component" in
  all|gateway|web) ;;
  *) echo "Unknown web release component: $component" >&2; exit 1 ;;
esac
[[ -f "$root/webgateway.json" && -s "$archive" ]] || exit 1
[[ "$(systemctl show -p LoadState --value "$service")" == loaded ]] || {
  echo "Install $service before deploying" >&2
  exit 1
}

exec 9>"$root/.deploy.lock"
flock -n 9 || { echo 'A web gateway deploy is already running' >&2; exit 1; }
previous=''
if [[ -L "$root/current" ]]; then
  previous="$(readlink "$root/current")"
elif [[ -e "$root/current" ]]; then
  echo 'The current release path must be a symlink' >&2
  exit 1
fi
if [[ "$component" != all && -z "$previous" ]]; then
  echo 'Deploy the complete web release before updating either component separately' >&2
  exit 1
fi

mkdir -p "$root/releases"
release="$(mktemp -d "$root/releases/release.XXXXXXXX")"
tar -xzf "$archive" -C "$release"
case "$component" in
  gateway) cp -a -- "$root/current/web" "$release/web" ;;
  web)
    mv -- "$release/dist" "$release/web"
    cp -a -- "$root/current/sonoryx-webgateway" "$release/sonoryx-webgateway"
    ;;
esac
[[ -s "$release/sonoryx-webgateway" && -s "$release/web/index.html" ]] || {
  echo 'The release must contain the gateway binary and web assets' >&2
  exit 1
}
chmod 0755 "$release/sonoryx-webgateway"
version="$("$release/sonoryx-webgateway" -version)"

switch_release() {
  ln -sfn -- "$1" "$root/.current.next"
  mv -Tf -- "$root/.current.next" "$root/current"
}

rollback_on_exit() {
  local status="$?"
  if (( status == 0 )) || [[ "$(readlink "$root/current" 2>/dev/null || true)" != "$release" ]]; then
    return
  fi

  echo 'Web gateway startup failed. Restoring the previous release.' >&2
  if [[ -n "$previous" ]]; then
    switch_release "$previous"
    sudo systemctl reset-failed "$service"
    sudo systemctl restart "$service"
  else
    sudo systemctl stop "$service"
    rm -- "$root/current"
  fi
}

sudo -v
trap rollback_on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
switch_release "$release"
sudo systemctl restart "$service"
trap - EXIT HUP INT TERM
printf 'Web release deployed: component=%s gateway=%s (%s)\n' "$component" "$version" "$release"

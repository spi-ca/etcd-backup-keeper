#!/usr/bin/env bash
# Host setup is authorized ONLY on a disposable GitHub-hosted Ubuntu CI VM.
set -euo pipefail
export LC_ALL=C
umask 077
fail() { printf 'ci-bootstrap: %s\n' "$*" >&2; exit 1; }
[[ ${CI:-} == true && ${GITHUB_ACTIONS:-} == true && \
   ${RUNNER_ENVIRONMENT:-} == github-hosted && ${RUNNER_OS:-} == Linux ]] || \
    fail 'requires an ephemeral GitHub-hosted Linux Actions VM (not local/self-hosted)'
[[ $(id -u) != 0 && $(uname -m) == x86_64 ]] || fail 'requires a non-root amd64 runner'
# shellcheck source=/dev/null
source /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 ]] || fail 'requires Ubuntu 24.04'
[[ -d ${RUNNER_TEMP:-} && -f ${GITHUB_ENV:-} ]] || fail 'missing Actions runtime paths'
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
user=$(id -un)
uid=$(id -u)

sudo -n apt-get update
sudo -n env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    podman uidmap slirp4netns fuse-overlayfs dbus-user-session apparmor \
    python3 openssl binutils dpkg-dev ca-certificates

# New hosted users occasionally lack subordinate IDs. Allocate a common unused
# range above ALL existing mappings; preserve every existing user's mappings.
read -r need_uid need_gid start < <(python3 - "$user" "$uid" <<'PY'
import sys
from pathlib import Path
user, uid = sys.argv[1:]
missing = []
end = 100000
for path in ('/etc/subuid', '/etc/subgid'):
    enough = False
    for line in Path(path).read_text().splitlines():
        if not line.strip() or line.startswith('#'):
            continue
        owner, first, count = line.split(':')
        first, count = int(first), int(count)
        end = max(end, first + count)
        enough |= owner in (user, uid) and count >= 65536
    missing.append(int(not enough))
assert end + 65535 < 2**32 - 1, 'no subordinate ID range available'
print(*missing, end)
PY
)
if [[ $need_uid == 1 ]]; then sudo -n usermod --add-subuids "$start-$((start + 65535))" "$user"; fi
if [[ $need_gid == 1 ]]; then sudo -n usermod --add-subgids "$start-$((start + 65535))" "$user"; fi

# A real user session gives rootless Podman delegated cgroup v2 limits. Do not
# disable cgroups or drop the lifecycle test's memory/pids restrictions.
sudo -n loginctl enable-linger "$user"
sudo -n systemctl start "user@$uid.service"
export XDG_RUNTIME_DIR="/run/user/$uid"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
[[ -d $XDG_RUNTIME_DIR && -S $XDG_RUNTIME_DIR/bus ]] || fail 'missing user session bus'
[[ $(stat -c '%u' "$XDG_RUNTIME_DIR") == "$uid" ]] || fail 'runtime directory has wrong owner'
printf 'XDG_RUNTIME_DIR=%s\nDBUS_SESSION_BUS_ADDRESS=%s\n' \
    "$XDG_RUNTIME_DIR" "$DBUS_SESSION_BUS_ADDRESS" >> "$GITHUB_ENV"

# Ubuntu 24.04 can restrict unprivileged namespaces. If needed, grant userns to
# /usr/bin/podman alone, not a global sysctl/security-policy relaxation. No sudo
# Podman, privileged containers, or AppArmor disablement is used.
if ! podman info >/dev/null 2>"$RUNNER_TEMP/podman-preflight.log"; then
    [[ $(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null || true) == 1 ]] || \
        fail 'Podman preflight failed; see runner-local podman-preflight.log'
    profile="$RUNNER_TEMP/etcd-backup-keeper-ci-podman.apparmor"
    cat > "$profile" <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>
profile etcd-backup-keeper-ci-podman /usr/bin/podman flags=(unconfined) {
  userns,
}
PROFILE
    sudo -n apparmor_parser -r "$profile"
fi
[[ $(podman info --format '{{.Host.Security.Rootless}}') == true ]] || fail 'Podman must be rootless'
[[ $(podman info --format '{{.Host.CgroupsVersion}}') == v2 ]] || fail 'cgroup v2 required'

etcd=gcr.io/etcd-development/etcd
etcd_digest=sha256:b4a41c6864baf573f5213dce18b2a95e1002ec6c5a1a26ad900c2ea166ac6d0b
podman pull --platform linux/amd64 "$etcd@$etcd_digest"
podman tag "$etcd@$etcd_digest" "$etcd:v3.7.3"
[[ $(podman image inspect "$etcd:v3.7.3" --format '{{.Os}}/{{.Architecture}}') == linux/amd64 ]] || \
    fail 'etcd image must be Linux amd64'
image=localhost/etcd-backup-keeper-test-ubuntu:24.04
# Empty context: never send checkout contents or credentials into the image.
context=$(mktemp -d "$RUNNER_TEMP/package-image.XXXXXXXX")
trap 'rm -rf -- "$context"' EXIT
podman build --pull=always --tag "$image" --file "$repo/packaging/tests/ubuntu.Dockerfile" "$context"
# Exercise the same rootless isolation/resource limits used by package-test.sh.
podman run --rm --pull=never --network=none --security-opt=no-new-privileges \
    --pids-limit=128 --memory=1g "$image" bash -ec \
    'command -v dpkg-deb; command -v systemctl; command -v systemd-analyze; command -v timeout'
for tool in python3 openssl readelf; do command -v "$tool" >/dev/null || fail "missing $tool"; done
mkdir -p "$RUNNER_TEMP/ci-evidence"
{
    printf 'etcd_image=%s@%s\n' "$etcd" "$etcd_digest"
    printf 'package_image=%s\n' "$(podman image inspect "$image" --format '{{.Id}}')"
    podman --version
    python3 --version
    openssl version
    dpkg-deb --version | head -n 1
} > "$RUNNER_TEMP/ci-evidence/bootstrap.log"

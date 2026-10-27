#!/usr/bin/env bash
# Container-only test helper: do not run on a host or against a production root.
set -euo pipefail
export LC_ALL=C
[[ -f /run/.containerenv && ${SYSTEMD_OFFLINE:-} == 1 && -d /evidence ]] || {
    echo 'Refusing to run outside the disposable offline Podman test container' >&2
    exit 1
}
version=$1
upgrade=$2
kind=$3
binary_hash=$4
package=etcd-backup-keeper
initial=/evidence/initial/${package}_${version}_amd64.deb
updated=/evidence/upgrade/${package}_${upgrade}_amd64.deb
config=/etc/$package/config.toml
backup=/var/lib/$package
service=/usr/lib/systemd/system/$package.service
timer=/usr/lib/systemd/system/$package.timer
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_mode() { [[ $(stat -c '%a:%u:%g' "$1") == "$2:0:0" ]] || fail "mode/owner $1"; }
assert_no_activation() {
    local links enabled
    local roots=(/etc/systemd/system /usr/lib/systemd/system)
    if [[ -d /run/systemd/system ]]; then roots+=(/run/systemd/system); fi
    links=$(find "${roots[@]}" \
        -type l \( -name '*etcd-backup-keeper*' -o -lname '*etcd-backup-keeper*' \) -print)
    [[ -z $links ]] || fail "unit enablement symlinks: $links"
    enabled=$(systemctl is-enabled "$package.timer" 2>/dev/null || true)
    [[ $enabled == disabled || $enabled == not-found || -z $enabled ]] || fail "timer state: $enabled"
    [[ ! -e /tmp/etcd-backup-keeper-unexpected-execution ]] || fail 'stub executed a non-version command'
}
assert_backup() {
    [[ -f $backup/operator-sentinel ]] || fail 'backup sentinel missing'
    [[ $(cat "$backup/operator-sentinel") == 'packaging lifecycle test, not an etcd snapshot' ]] || fail 'backup sentinel changed'
    assert_mode "$backup" 700
    assert_mode "$backup/operator-sentinel" 600
}
printf 'MODE: %s; systemd offline; no backup command will be invoked\n' "$kind"
dpkg-deb --version | head -n 1
systemd-analyze --version | head -n 1
dpkg --compare-versions "$upgrade" gt "$version" || fail 'upgrade version must be newer'
# Confirm no package-supplied maintainer script can execute, and no backup data
# path is shipped. These checks happen before any dpkg installation.
for deb in "$initial" "$updated"; do
    control=$work/$(basename "$deb").control
    dpkg-deb --control "$deb" "$control"
    [[ $(find "$control" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort) == $'conffiles\ncontrol' ]] || fail 'unexpected control entry / maintainer script'
    [[ $(cat "$control/conffiles") == "$config" ]] || fail 'conffiles registration'
    [[ $(dpkg-deb -f "$deb" Architecture) == amd64 ]] || fail 'architecture'
    dpkg-deb --fsys-tarfile "$deb" > "$work/payload.tar"
    tar -tf "$work/payload.tar" > "$work/payload.list"
    if grep -Eq '^\./var(/|$)' "$work/payload.list"; then fail 'package owns backup/state path'; fi
    tar --numeric-owner -tvf "$work/payload.tar" > "$work/payload.details"
    awk '$2 != "0/0" { bad=1 } END { exit bad }' "$work/payload.details" || fail 'non-root archive ownership'
done
install -d -m 0700 "$backup"
printf '%s\n' 'packaging lifecycle test, not an etcd snapshot' > "$backup/operator-sentinel"
chmod 0600 "$backup/operator-sentinel"
assert_no_activation
# Minimal Ubuntu images can exclude /usr/share/doc. Override only our doc path
# so this test checks the shipped documentation, not an image size optimization.
dpkg_doc_options=("--path-include=/usr/share/doc/$package" "--path-include=/usr/share/doc/$package/*")
printf '\nSTEP install\n'
dpkg "${dpkg_doc_options[@]}" --install "$initial"
assert_mode "$config" 600
assert_mode "$(dirname "$config")" 700
assert_mode /usr/bin/$package 755
assert_mode "$service" 644
assert_mode "$timer" 644
cmp "$config" /evidence/original-assets/config.example.toml
for relative in README.md docs/quickstart.md docs/requirements.md docs/operations.md docs/troubleshooting.md docs/restore-drill.md integration/README.md .github/README.md config.example.toml LICENSE THIRD_PARTY_NOTICES; do
    cmp "/usr/share/doc/$package/$relative" "/evidence/original-assets/$relative"
done
cmp "/usr/share/doc/$package/copyright" /evidence/original-assets/LICENSE
[[ -s /usr/share/doc/$package/packaging/README.md ]] || fail 'packaging documentation missing'
[[ $(sha256sum /usr/bin/$package | cut -d ' ' -f 1) == "$binary_hash" ]] || fail 'installed binary differs'
# This is a syntax/dependency check, not service execution or sandbox proof.
systemd-analyze verify "$service" "$timer"
timeout 15 /usr/bin/$package version > "$work/version-output" 2>&1
cat "$work/version-output"
if [[ $kind == stub ]]; then
    grep -Fq 'PACKAGING TEST STUB' "$work/version-output" || fail 'stub not identified'
else
    if grep -Fq 'PACKAGING TEST STUB' "$work/version-output"; then fail 'real binary is a stub'; fi
fi
assert_no_activation
assert_backup
printf '\nSTEP operator config edit\n'
printf '\n# Operator edit: preserve this on upgrade and remove.\n' >> "$config"
cp "$config" "$work/operator-config"
printf '\nSTEP upgrade (explicit dpkg conffile choice: keep operator version)\n'
dpkg "${dpkg_doc_options[@]}" --force-confold --install "$updated"
[[ $(dpkg-query -W -f='${Version}' "$package") == "$upgrade" ]] || fail 'version not upgraded'
cmp "$config" "$work/operator-config"
cmp "$config.dpkg-dist" /evidence/upgrade-assets/config.example.toml
assert_mode "$config" 600
assert_no_activation
assert_backup
printf '\nSTEP remove\n'
dpkg --remove "$package"
cmp "$config" "$work/operator-config"
assert_mode "$config" 600
[[ ! -e /usr/bin/$package && ! -e $service && ! -e $timer ]] || fail 'installed files remain after removal'
assert_no_activation
assert_backup
printf '\nSTEP purge\n'
dpkg --purge "$package"
[[ ! -e $config && ! -e $config.dpkg-dist ]] || fail 'conffile remains after purge'
assert_no_activation
assert_backup
printf '\nPASS: install -> edit -> upgrade -> remove -> purge; config protected; backup sentinel preserved; no enablement; static units verified; version smoke only (%s).\n' "$kind"
printf 'LIMIT: offline systemd does not prove live service execution, sandbox compatibility, or real backups.\n'

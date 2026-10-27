#!/usr/bin/env bash
# Container-only: never run pacman on the host or a production root.
set -euo pipefail
export LC_ALL=C
[[ -f /run/.containerenv && ${SYSTEMD_OFFLINE:-} == 1 && -d /evidence && $(id -u) == 0 ]] || {
    echo 'Refusing to run outside the disposable offline Podman test container' >&2
    exit 1
}
version=$1 upgrade=$2 kind=$3 binary_hash=$4 epoch=$5 expected=$6
package=etcd-backup-keeper
initial=/evidence/initial/$package-$version-x86_64.pkg.tar.zst
updated=/evidence/upgrade/$package-$upgrade-x86_64.pkg.tar.zst
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
    links=$(find "${roots[@]}" -type l \( -name '*etcd-backup-keeper*' -o -lname '*etcd-backup-keeper*' \) -print)
    [[ -z $links ]] || fail "unit enablement symlinks: $links"
    enabled=$(systemctl is-enabled "$package.timer" 2>/dev/null || true)
    [[ $enabled == disabled || $enabled == not-found || -z $enabled ]] || fail "timer state: $enabled"
    [[ ! -e /run/systemd/system && ! -e /tmp/etcd-backup-keeper-unexpected-execution ]] || fail 'unexpected systemd/stub activation'
}
assert_backup() {
    [[ -f $backup/operator-sentinel ]] || fail 'backup sentinel missing'
    [[ $(cat "$backup/operator-sentinel") == 'packaging lifecycle test, not an etcd snapshot' ]] || fail 'backup sentinel changed'
    assert_mode "$backup" 700
    assert_mode "$backup/operator-sentinel" 600
    if pacman -Qo "$backup/operator-sentinel" >/dev/null 2>&1; then fail 'package owns operator backup'; fi
}
assert_installed() {
    local relative
    assert_mode "$config" 600
    assert_mode "$(dirname "$config")" 700
    assert_mode /usr/bin/$package 755
    assert_mode "$service" 644
    assert_mode "$timer" 644
    [[ $(sha256sum /usr/bin/$package | cut -d ' ' -f 1) == "$binary_hash" ]] || fail 'installed binary differs'
    for relative in README.md docs/quickstart.md docs/requirements.md docs/operations.md docs/troubleshooting.md docs/restore-drill.md integration/README.md .github/README.md config.example.toml LICENSE THIRD_PARTY_NOTICES; do
        cmp "/usr/share/doc/$package/$relative" "/evidence/$1/$relative"
        assert_mode "/usr/share/doc/$package/$relative" 644
    done
    cmp "/usr/share/doc/$package/copyright" /evidence/original-assets/LICENSE
    cmp "/usr/share/licenses/$package/LICENSE" /evidence/original-assets/LICENSE
    cmp "/usr/share/doc/$package/packaging/README.md" /evidence/packaging-assets/README.md
    cmp "$service" /evidence/packaging-assets/$package.service
    cmp "$timer" /evidence/packaging-assets/$package.timer
}
printf 'MODE: %s; systemd offline; version smoke only; no backup or endpoint access\n' "$kind"
pacman --version
systemd-analyze --version | head -n 1
[[ $(vercmp "$upgrade" "$version") == 1 ]] || fail 'upgrade must be newer'
# Exact allowlist excludes .INSTALL, hooks, symlinks and all /var payload.
cat > "$work/allowed" <<EOF
.BUILDINFO
.MTREE
.PKGINFO
etc
etc/$package
etc/$package/config.toml
usr
usr/bin
usr/bin/$package
usr/lib
usr/lib/systemd
usr/lib/systemd/system
usr/lib/systemd/system/$package.service
usr/lib/systemd/system/$package.timer
usr/share
usr/share/doc
usr/share/doc/$package
usr/share/doc/$package/.github
usr/share/doc/$package/.github/README.md
usr/share/doc/$package/LICENSE
usr/share/doc/$package/README.md
usr/share/doc/$package/THIRD_PARTY_NOTICES
usr/share/doc/$package/config.example.toml
usr/share/doc/$package/copyright
usr/share/doc/$package/docs
usr/share/doc/$package/docs/operations.md
usr/share/doc/$package/docs/quickstart.md
usr/share/doc/$package/docs/requirements.md
usr/share/doc/$package/docs/restore-drill.md
usr/share/doc/$package/docs/troubleshooting.md
usr/share/doc/$package/integration
usr/share/doc/$package/integration/README.md
usr/share/doc/$package/packaging
usr/share/doc/$package/packaging/README.md
usr/share/licenses
usr/share/licenses/$package
usr/share/licenses/$package/LICENSE
EOF
sort -o "$work/allowed" "$work/allowed"
for archive in "$initial" "$updated"; do
    bsdtar -tf "$archive" | sed 's:/$::' | sort > "$work/list"
    cmp "$work/allowed" "$work/list" || fail 'unexpected archive payload or install script/hook'
    bsdtar --numeric-owner -tvf "$archive" > "$work/details"
    awk '$3 != "0" || $4 != "0" || $1 !~ /^[-d]/ { bad=1 } END { exit bad }' "$work/details" || fail 'non-root archive ownership or link'
    bsdtar -xOf "$archive" .PKGINFO > "$work/pkginfo"
    for field in "pkgname = $package" 'arch = x86_64' 'license = BSD-3-Clause' 'depend = systemd' \
        "backup = etc/$package/config.toml" 'packager = Packaging Test <packaging-test@example.invalid>' "builddate = $epoch"; do
        grep -Fxq "$field" "$work/pkginfo" || fail "missing metadata: $field"
    done
    [[ $(grep -c '^depend = ' "$work/pkginfo") == 1 && $(grep -c '^backup = ' "$work/pkginfo") == 1 ]] || fail 'unexpected dependency/backup fields'
    want_version=$version
    [[ $archive != "$updated" ]] || want_version=$upgrade
    grep -Fxq "pkgver = $want_version" "$work/pkginfo" || fail 'package version metadata'
    bsdtar -xOf "$archive" .BUILDINFO > "$work/buildinfo"
    grep -Fxq "builddate = $epoch" "$work/buildinfo" || fail 'buildinfo epoch'
done
[[ ! -e $backup && ! -e $config && ! -e /usr/bin/$package ]] || fail 'container is not pristine'
install -d -m 0700 "$backup"
printf '%s\n' 'packaging lifecycle test, not an etcd snapshot' > "$backup/operator-sentinel"
chmod 0600 "$backup/operator-sentinel"
assert_no_activation
# Official minimal Arch images exclude docs. Restore only this package's doc
# subtree in this disposable container; retain all other image exclusions.
printf '\n[options]\nNoExtract = !usr/share/doc/%s !usr/share/doc/%s/*\n' "$package" "$package" >> /etc/pacman.conf
printf '\nSTEP install (offline pacman; systemd dependency already in image)\n'
pacman -U --noconfirm "$initial"
[[ $(pacman -Q "$package") == "$package $version" ]] || fail 'installed version'
cmp "$config" /evidence/original-assets/config.example.toml
assert_installed original-assets
systemd-analyze verify "$service" "$timer"
timeout 15 /usr/bin/$package version > "$work/version-output" 2>&1
cat "$work/version-output"
if [[ $kind == stub ]]; then
    grep -Fq 'PACKAGING TEST STUB' "$work/version-output" || fail 'stub not identified'
else
    if grep -Fq 'PACKAGING TEST STUB' "$work/version-output"; then fail 'real binary is a stub'; fi
fi
[[ -z $expected || $(cat "$work/version-output") == "$package $expected" ]] || fail 'binary version does not match expected provenance'
assert_no_activation
assert_backup
printf '\nSTEP operator edit\n'
printf '\n# Operator edit: preserve this on upgrade and remove.\n' >> "$config"
cp "$config" "$work/operator-config"
printf '\nSTEP upgrade (changed packaged default; operator config kept, .pacnew generated)\n'
pacman -U --noconfirm "$updated"
[[ $(pacman -Q "$package") == "$package $upgrade" ]] || fail 'version not upgraded'
cmp "$config" "$work/operator-config"
cmp "$config.pacnew" /evidence/upgrade-assets/config.example.toml
assert_mode "$config.pacnew" 600
assert_installed upgrade-assets
assert_no_activation
assert_backup
printf '\nSTEP remove (modified backup config becomes .pacsave)\n'
pacman -R --noconfirm "$package"
[[ ! -e $config && -f $config.pacsave ]] || fail 'config not saved on remove'
cmp "$config.pacsave" "$work/operator-config"
assert_mode "$(dirname "$config")" 700
assert_mode "$config.pacsave" 600
[[ ! -e /usr/bin/$package && ! -e $service && ! -e $timer ]] || fail 'installed executable/units remain'
if pacman -Q "$package" >/dev/null 2>&1; then fail 'package still installed'; fi
assert_no_activation
assert_backup
printf '\nPASS: reproducible install -> edit -> upgrade -> remove; .pacnew/.pacsave protected; root-owned private config and backup sentinel preserved; no enablement; static units verified; version only (%s).\n' "$kind"
printf 'LIMIT: offline systemd does not prove live service execution, sandbox compatibility, or real backups.\n'

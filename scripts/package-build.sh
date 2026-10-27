#!/usr/bin/env bash
# Build only: no compiler, downloads, package installation, or service activation.
set -euo pipefail
export LC_ALL=C
umask 077
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
image=localhost/etcd-backup-keeper-test-ubuntu:24.04
binary= version= output= assets= maintainer=
usage() {
    cat <<'EOF'
Usage: scripts/package-build.sh --binary PATH --version VERSION --output-dir DIR
       --assets-dir PROJECT_ROOT --maintainer 'Name <email>' [--image LOCAL_IMAGE]
Requires a static Linux amd64 ELF and these project assets:
README.md, docs/{quickstart,requirements,operations,troubleshooting,restore-drill}.md,
integration/README.md, .github/README.md, config.example.toml,
LICENSE, and THIRD_PARTY_NOTICES. Uses local dpkg-deb, or rootless Podman without pulling.
SOURCE_DATE_EPOCH defaults to 0. No Go build or network access is performed.
EOF
}
die() { printf 'package-build: %s\n' "$*" >&2; exit 1; }
while (($#)); do
    case "$1" in
        --binary|--version|--output-dir|--assets-dir|--maintainer|--image)
            (($# >= 2)) || die "missing value for $1"
            case "$1" in
                --binary) binary=$2 ;; --version) version=$2 ;;
                --output-dir) output=$2 ;; --assets-dir) assets=$2 ;;
                --maintainer) maintainer=$2 ;; --image) image=$2 ;;
            esac
            shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done
[[ -n $binary && -n $version && -n $output && -n $assets && -n $maintainer ]] || { usage >&2; exit 1; }
# Deliberately support a conservative subset of Debian versions (no epoch).
[[ ${#version} -le 128 && $version =~ ^[0-9][A-Za-z0-9.+~]*(-[A-Za-z0-9.+~]+)?$ ]] || die 'invalid version (no epoch or whitespace allowed)'
[[ $maintainer != *$'\n'* && $maintainer != *$'\r'* && $maintainer =~ ^[^\<\>]+\ \<[^\<\>\ ]+@[^\<\>\ ]+\>$ ]] || die 'maintainer must be a single Name <email> field'
epoch=${SOURCE_DATE_EPOCH:-0}
[[ $epoch =~ ^[0-9]+$ && ${#epoch} -le 10 ]] || die 'invalid SOURCE_DATE_EPOCH'
[[ $image =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] || die 'invalid local image reference'
for tool in readelf install realpath find touch; do command -v "$tool" >/dev/null || die "missing tool: $tool"; done
[[ -f $binary && ! -L $binary && -r $binary && -x $binary ]] || die 'binary must be a readable executable regular file, not a symlink'
binary=$(realpath -e -- "$binary")
assets=$(realpath -e -- "$assets")
[[ -d $assets ]] || die 'assets source must be a directory'
for relative in README.md docs/quickstart.md docs/requirements.md docs/operations.md docs/troubleshooting.md docs/restore-drill.md integration/README.md .github/README.md config.example.toml LICENSE THIRD_PARTY_NOTICES; do
    file=$assets/$relative
    [[ -f $file && ! -L $file && -r $file ]] || die "missing/nonregular/symlink asset: $relative"
    [[ $(realpath -e -- "$file") == "$assets/"* ]] || die "asset escapes source directory: $relative"
done
# Never execute an input binary during the build. Dynamic ELF inputs cannot run
# reliably on the target without undeclared runtime dependencies.
header=$(readelf -hW -- "$binary") || die 'not an ELF binary'
[[ $header == *'Class:'*ELF64* && $header == *'Data:'*'little endian'* && $header == *'Machine:'*'Advanced Micro Devices X86-64'* ]] || die 'binary must be Linux amd64 ELF64 little-endian'
[[ $header == *'Type:'*EXEC* || $header == *'Type:'*DYN* ]] || die 'binary must be an executable ELF'
segments=$(readelf -lW -- "$binary") || die 'cannot read program headers'
dynamic=$(readelf -dW -- "$binary") || die 'cannot read dynamic section'
[[ $segments != *INTERP* && $dynamic != *'(NEEDED)'* ]] || die 'binary must be statically linked (no interpreter or shared libraries)'
mkdir -p -- "$output"
output=$(realpath -e -- "$output")
[[ -d $output && -w $output && $output != *:* && $output != *$'\n'* ]] || die 'output directory must be writable and contain no colon/newline'
filename=etcd-backup-keeper_${version}_amd64.deb
[[ ! -e $output/$filename && ! -L $output/$filename ]] || die "refusing to overwrite $output/$filename"
work=$(mktemp -d "$repo/packaging/.tmp-build.XXXXXXXX")
partial=
container=
cleanup() {
    if [[ -n $container ]]; then podman rm -f "$container" >/dev/null 2>&1 || true; fi
    [[ -z $partial ]] || rm -f -- "$partial"
    rm -rf -- "$work"
}
trap cleanup EXIT
root=$work/root
install -d -m 0755 "$root/DEBIAN" "$root/usr/bin" "$root/usr/lib/systemd/system" \
    "$root/usr/share/doc/etcd-backup-keeper/docs" "$root/usr/share/doc/etcd-backup-keeper/packaging" \
    "$root/usr/share/doc/etcd-backup-keeper/integration" "$root/usr/share/doc/etcd-backup-keeper/.github" "$root/etc"
install -d -m 0700 "$root/etc/etcd-backup-keeper"
install -m 0755 "$binary" "$root/usr/bin/etcd-backup-keeper"
install -m 0600 "$assets/config.example.toml" "$root/etc/etcd-backup-keeper/config.toml"
install -m 0644 "$repo/packaging/systemd/etcd-backup-keeper.service" "$repo/packaging/systemd/etcd-backup-keeper.timer" "$root/usr/lib/systemd/system/"
install -m 0644 "$assets/README.md" "$root/usr/share/doc/etcd-backup-keeper/README.md"
install -m 0644 "$assets/LICENSE" "$root/usr/share/doc/etcd-backup-keeper/copyright"
install -m 0644 "$assets/THIRD_PARTY_NOTICES" "$root/usr/share/doc/etcd-backup-keeper/THIRD_PARTY_NOTICES"
for relative in docs/quickstart.md docs/requirements.md docs/operations.md docs/troubleshooting.md docs/restore-drill.md integration/README.md .github/README.md config.example.toml LICENSE; do
    install -m 0644 "$assets/$relative" "$root/usr/share/doc/etcd-backup-keeper/$relative"
done
install -m 0644 "$repo/packaging/README.md" "$root/usr/share/doc/etcd-backup-keeper/packaging/README.md"
cat > "$root/DEBIAN/control" <<EOF
Package: etcd-backup-keeper
Version: $version
Section: admin
Priority: optional
Architecture: amd64
Maintainer: $maintainer
Depends: systemd
Description: Local etcd snapshot backup and verification CLI
 Static Linux CLI with an operator-activated systemd timer.
 Installation and upgrade do not run backups or enable units.
EOF
printf '/etc/etcd-backup-keeper/config.toml\n' > "$root/DEBIAN/conffiles"
chmod 0644 "$root/DEBIAN/control" "$root/DEBIAN/conffiles"
# Normalize every payload/control entry, not just the ar header. Single-threaded
# compression makes equal inputs + epoch + dpkg version byte reproducible.
find "$root" -exec touch -h -d "@$epoch" -- {} +
partial=$(mktemp "$output/.package-build.XXXXXXXX.deb")
if command -v dpkg-deb >/dev/null; then
    SOURCE_DATE_EPOCH=$epoch dpkg-deb --root-owner-group -Zxz -z9 --threads-max=1 --build "$root" "$partial"
else
    command -v podman >/dev/null || die 'dpkg-deb unavailable; rootless Podman required'
    [[ $(podman info --format '{{.Host.Security.Rootless}}') == true ]] || die 'fallback requires rootless Podman'
    [[ $(podman image inspect "$image" --format '{{.Os}}/{{.Architecture}}') == linux/amd64 ]] || die 'local image must be Linux amd64'
    container=etcd-backup-keeper-package-build-$$-$RANDOM
    podman run --rm --name "$container" --pull=never --network=none --userns=keep-id \
        --user "$(id -u):$(id -g)" --security-opt=no-new-privileges \
        --volume "$root:/stage:ro" --volume "$output:/out:rw" \
        --env "SOURCE_DATE_EPOCH=$epoch" "$image" \
        dpkg-deb --root-owner-group -Zxz -z9 --threads-max=1 --build /stage "/out/$(basename -- "$partial")"
    container=
fi
# Atomic publish without replacing an existing artifact.
chmod 0644 "$partial"
ln -- "$partial" "$output/$filename"
printf '%s\n' "$output/$filename"

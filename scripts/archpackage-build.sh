#!/usr/bin/env bash
# Offline build only: rootless, non-root makepkg; never install host tools.
set -euo pipefail
export LC_ALL=C
umask 077
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
image=docker.io/library/archlinux:base-devel
binary= version= output= assets= packager=
usage() {
    cat <<'EOF'
Usage: scripts/archpackage-build.sh --binary PATH --version YYYYMMDD-PKGREL
       --assets-dir PROJECT_ROOT --output-dir DIR --packager 'Name <email>'
       [--image LOCAL_IMAGE]
Builds an x86_64 .pkg.tar.zst from a supplied static amd64 ELF, without executing
it. Requires cached Arch base-devel and rootless Podman. No pulls or network.
Required project assets: README.md,
docs/{quickstart,requirements,operations,troubleshooting,restore-drill}.md,
integration/README.md, .github/README.md, config.example.toml, LICENSE,
and THIRD_PARTY_NOTICES.
SOURCE_DATE_EPOCH defaults to 0; equal inputs/tool image/epoch are reproducible.
EOF
}
die() { printf 'archpackage-build: %s\n' "$*" >&2; exit 1; }
while (($#)); do
    case "$1" in
        --binary|--version|--assets-dir|--output-dir|--packager|--image)
            (($# >= 2)) || die "missing value for $1"
            case "$1" in
                --binary) binary=$2 ;; --version) version=$2 ;;
                --assets-dir) assets=$2 ;; --output-dir) output=$2 ;;
                --packager) packager=$2 ;; --image) image=$2 ;;
            esac
            shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done
[[ -n $binary && -n $version && -n $assets && -n $output && -n $packager ]] || { usage >&2; exit 1; }
# No shell-sourced user input: only validated digits enter the PKGBUILD.
[[ $version =~ ^([0-9]{8})-([1-9][0-9]{0,5})$ ]] || die 'version must be YYYYMMDD-PKGREL (positive integer pkgrel)'
pkgver=${BASH_REMATCH[1]}
pkgrel=${BASH_REMATCH[2]}
[[ $(date -u -d "$pkgver" +%Y%m%d 2>/dev/null) == "$pkgver" ]] || die 'invalid calendar date in version'
[[ ${#packager} -le 200 && $packager =~ ^[A-Za-z0-9][A-Za-z0-9._+\ -]*\ \<[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]+\>$ ]] || die 'packager must be a single conservative Name <email> field'
epoch=${SOURCE_DATE_EPOCH:-0}
[[ $epoch =~ ^[0-9]+$ && ${#epoch} -le 10 ]] || die 'invalid SOURCE_DATE_EPOCH'
[[ $image =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] || die 'invalid local image reference'
for tool in podman readelf install realpath find touch sed date; do command -v "$tool" >/dev/null || die "missing tool: $tool"; done
[[ $(podman info --format '{{.Host.Security.Rootless}}') == true ]] || die 'requires rootless Podman'
[[ $(podman image inspect "$image" --format '{{.Os}}/{{.Architecture}}') == linux/amd64 ]] || die 'cached image must be Linux amd64'
[[ -f $binary && ! -L $binary && -r $binary && -x $binary ]] || die 'binary must be a readable executable regular file, not a symlink'
binary=$(realpath -e -- "$binary")
assets=$(realpath -e -- "$assets")
[[ -d $assets ]] || die 'assets source must be a directory'
asset_files=(README.md docs/quickstart.md docs/requirements.md docs/operations.md docs/troubleshooting.md docs/restore-drill.md integration/README.md .github/README.md config.example.toml LICENSE THIRD_PARTY_NOTICES)
for relative in "${asset_files[@]}"; do
    file=$assets/$relative
    [[ -f $file && ! -L $file && -r $file ]] || die "missing/nonregular/symlink asset: $relative"
    [[ $(realpath -e -- "$file") == "$assets/"* ]] || die "asset escapes source: $relative"
done
for relative in arch/PKGBUILD.in systemd/etcd-backup-keeper.service systemd/etcd-backup-keeper.timer README.md; do
    [[ -f $repo/packaging/$relative && ! -L $repo/packaging/$relative && -r $repo/packaging/$relative ]] || die "invalid packaging asset: $relative"
done
header=$(readelf -hW -- "$binary") || die 'not an ELF binary'
[[ $header == *'Class:'*ELF64* && $header == *'Data:'*'little endian'* && $header == *'Machine:'*'Advanced Micro Devices X86-64'* ]] || die 'binary must be Linux amd64 ELF64 little-endian'
[[ $header == *'Type:'*EXEC* || $header == *'Type:'*DYN* ]] || die 'binary must be an executable ELF'
segments=$(readelf -lW -- "$binary") || die 'cannot read program headers'
dynamic=$(readelf -dW -- "$binary") || die 'cannot read dynamic section'
[[ $segments != *INTERP* && $dynamic != *'(NEEDED)'* ]] || die 'binary must be static (no interpreter or shared libraries)'
mkdir -p -- "$output"
output=$(realpath -e -- "$output")
[[ -d $output && -w $output && $output != *:* && $output != *$'\n'* && $output != *$'\r'* ]] || die 'output path must be writable without colon/newline'
filename=etcd-backup-keeper-$version-x86_64.pkg.tar.zst
[[ ! -e $output/$filename && ! -L $output/$filename ]] || die "refusing to overwrite $output/$filename"
work=$(mktemp -d "$output/.arch-build.XXXXXXXX")
partial= container=
cleanup() {
    if [[ -n $container ]]; then podman rm -f "$container" >/dev/null 2>&1 || true; fi
    [[ -z $partial ]] || rm -f -- "$partial"
    rm -rf -- "$work"
}
trap cleanup EXIT
root=$work/payload
install -d -m 0755 "$root/usr/bin" "$root/usr/lib/systemd/system" \
    "$root/usr/share/doc/etcd-backup-keeper/docs" "$root/usr/share/doc/etcd-backup-keeper/packaging" \
    "$root/usr/share/doc/etcd-backup-keeper/integration" "$root/usr/share/doc/etcd-backup-keeper/.github" \
    "$root/usr/share/licenses/etcd-backup-keeper" "$root/etc"
install -d -m 0700 "$root/etc/etcd-backup-keeper"
install -m 0755 "$binary" "$root/usr/bin/etcd-backup-keeper"
install -m 0600 "$assets/config.example.toml" "$root/etc/etcd-backup-keeper/config.toml"
install -m 0644 "$repo/packaging/systemd/etcd-backup-keeper.service" "$repo/packaging/systemd/etcd-backup-keeper.timer" "$root/usr/lib/systemd/system/"
for relative in "${asset_files[@]}"; do
    install -m 0644 "$assets/$relative" "$root/usr/share/doc/etcd-backup-keeper/$relative"
done
install -m 0644 "$assets/LICENSE" "$root/usr/share/doc/etcd-backup-keeper/copyright"
install -m 0644 "$assets/LICENSE" "$root/usr/share/licenses/etcd-backup-keeper/LICENSE"
install -m 0644 "$repo/packaging/README.md" "$root/usr/share/doc/etcd-backup-keeper/packaging/README.md"
# install -d honors the private umask for implicit parents; normalize them too.
find "$root" -type d ! -path "$root/etc/etcd-backup-keeper" -exec chmod 0755 {} +
find "$root" -exec touch -h -d "@$epoch" -- {} +
sed -e "s/@PKGVER@/$pkgver/g" -e "s/@PKGREL@/$pkgrel/g" "$repo/packaging/arch/PKGBUILD.in" > "$work/PKGBUILD"
install -d -m 0700 "$work/home" "$work/out"
# Fixed container paths + epoch, no ambient user config, single-thread zstd.
cat > "$work/makepkg.conf" <<'EOF'
source /etc/makepkg.conf
PKGDEST=/build/out
COMPRESSZST=(zstd -c -T1 -19 -)
PKGEXT='.pkg.tar.zst'
BUILDENV=('!distcc' '!color' '!ccache' '!check' '!sign')
EOF
container=etcd-backup-keeper-arch-build-$$-$RANDOM
podman run --rm --name "$container" --pull=never --network=none \
    --userns=keep-id --user "$(id -u):$(id -g)" --security-opt=no-new-privileges \
    --pids-limit=128 --memory=1g --volume "$work:/build:rw" --workdir /build \
    --env HOME=/build/home --env "SOURCE_DATE_EPOCH=$epoch" --env "PACKAGER=$packager" \
    --env LC_ALL=C --env TZ=UTC "$image" \
    bash -ec '[[ $(id -u) != 0 && -w $HOME ]]; makepkg --config /build/makepkg.conf --nodeps --nocheck --noconfirm' >&2
container=
[[ -f $work/out/$filename && ! -L $work/out/$filename ]] || die 'makepkg did not produce expected package'
partial=$(mktemp "$output/.arch-package.XXXXXXXX")
install -m 0644 "$work/out/$filename" "$partial"
# Atomic no-clobber publication even if another invocation won the race.
ln -- "$partial" "$output/$filename"
printf '%s\n' "$output/$filename"

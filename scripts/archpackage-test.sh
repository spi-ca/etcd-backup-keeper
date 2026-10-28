#!/usr/bin/env bash
# Offline pacman lifecycle in a disposable rootless Arch container, NEVER host pacman.
set -euo pipefail
export LC_ALL=C
umask 077
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
binary= assets= evidence= kind= expected=
image=docker.io/library/archlinux:base-devel
version=20261027-1
usage() {
    cat <<'EOF'
Usage: scripts/archpackage-test.sh --binary PATH --binary-kind stub|real
       --assets-dir PROJECT_ROOT --evidence-dir NEW_DIR [--image LOCAL_IMAGE]
       [--version YYYYMMDD-PKGREL] [--expected-version dev|vYYYYMMDD-PKGREL]
Builds twice and compares bytes; upgrades to pkgrel+1 with a changed config
example. Tests pacman install/edit/upgrade/remove, .pacnew/.pacsave preservation,
private ownership, units and version only. No endpoints or backups are invoked.
EOF
}
die() { printf 'archpackage-test: %s\n' "$*" >&2; exit 1; }
while (($#)); do
    case "$1" in
        --binary|--assets-dir|--evidence-dir|--binary-kind|--image|--version|--expected-version)
            (($# >= 2)) || die "missing value for $1"
            case "$1" in
                --binary) binary=$2 ;; --assets-dir) assets=$2 ;;
                --evidence-dir) evidence=$2 ;; --binary-kind) kind=$2 ;;
                --image) image=$2 ;; --version) version=$2 ;; --expected-version) expected=$2 ;;
            esac
            shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done
[[ -n $binary && -n $assets && -n $evidence && ( $kind == stub || $kind == real ) ]] || { usage >&2; exit 1; }
[[ $version =~ ^([0-9]{8})-([1-9][0-9]{0,5})$ ]] || die 'invalid version'
pkgver=${BASH_REMATCH[1]}
pkgrel=${BASH_REMATCH[2]}
((pkgrel < 999999)) || die 'no supported pkgrel+1 upgrade'
upgrade=$pkgver-$((pkgrel + 1))
[[ -z $expected || $expected =~ ^(dev|v[0-9]{8}-[1-9][0-9]{0,5})$ ]] || die 'invalid expected binary version'
[[ $image =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] || die 'invalid local image reference'
[[ -f $binary && ! -L $binary && -r $binary && -x $binary ]] || die 'binary must be a readable executable regular file, not a symlink'
command -v podman >/dev/null || die 'rootless Podman required'
[[ $(podman info --format '{{.Host.Security.Rootless}}') == true ]] || die 'requires rootless Podman'
[[ $(podman image inspect "$image" --format '{{.Os}}/{{.Architecture}}') == linux/amd64 ]] || die 'cached image must be Linux amd64'
[[ ! -e $evidence && ! -L $evidence ]] || die 'evidence directory must not exist'
binary=$(realpath -e -- "$binary")
assets=$(realpath -e -- "$assets")
mkdir -p -- "$evidence"
evidence=$(realpath -e -- "$evidence")
[[ $evidence != *:* && $evidence != *$'\n'* && $evidence != *$'\r'* ]] || die 'evidence path contains colon/newline'
container=
cleanup() {
    if [[ -n $container ]]; then podman rm -f "$container" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
# Explicit stable example assets only; never mount the checkout into the lifecycle.
for tree in original-assets upgrade-assets; do
    install -d -m 0700 "$evidence/$tree/docs" "$evidence/$tree/integration"
    for relative in README.md docs/quickstart.md docs/requirements.md docs/operations.md docs/troubleshooting.md docs/restore-drill.md integration/README.md config.example.toml LICENSE THIRD_PARTY_NOTICES; do
        [[ -f $assets/$relative && ! -L $assets/$relative && -r $assets/$relative ]] || die "missing/nonregular/symlink asset: $relative"
        [[ $(realpath -e -- "$assets/$relative") == "$assets/"* ]] || die "asset escapes source: $relative"
        install -m 0600 "$assets/$relative" "$evidence/$tree/$relative"
    done
done
install -d -m 0700 "$evidence/packaging-assets"
install -m 0600 "$repo/packaging/README.md" "$repo/packaging/systemd/etcd-backup-keeper.service" \
    "$repo/packaging/systemd/etcd-backup-keeper.timer" "$evidence/packaging-assets/"
printf '\n# Changed default template for the Arch packaging lifecycle test only.\n' >> "$evidence/upgrade-assets/config.example.toml"
packager='Packaging Test <packaging-test@example.invalid>'
for target in initial reproducibility; do
    "$repo/scripts/archpackage-build.sh" --binary "$binary" --version "$version" \
        --assets-dir "$evidence/original-assets" --output-dir "$evidence/$target" \
        --packager "$packager" --image "$image" > "$evidence/build-$target.log" 2>&1
done
"$repo/scripts/archpackage-build.sh" --binary "$binary" --version "$upgrade" \
    --assets-dir "$evidence/upgrade-assets" --output-dir "$evidence/upgrade" \
    --packager "$packager" --image "$image" > "$evidence/build-upgrade.log" 2>&1
first=etcd-backup-keeper-$version-x86_64.pkg.tar.zst
second=etcd-backup-keeper-$upgrade-x86_64.pkg.tar.zst
cmp "$evidence/initial/$first" "$evidence/reproducibility/$first"
{
    printf 'binary_kind=%s\n' "$kind"
    printf 'scope=offline Arch packaging scaffold; not backup or live service acceptance\n'
    printf 'image=%s\n' "$(podman image inspect "$image" --format '{{.Id}}')"
    printf 'image_digest=%s\n' "$(podman image inspect "$image" --format '{{.Digest}}')"
    printf 'SOURCE_DATE_EPOCH=%s\n' "${SOURCE_DATE_EPOCH:-0}"
    printf 'package_version=%s\nupgrade_version=%s\nexpected_binary_version=%s\n' "$version" "$upgrade" "${expected:-not-asserted}"
    sha256sum "$binary" "$evidence/initial/$first" "$evidence/reproducibility/$first" "$evidence/upgrade/$second"
} | tee "$evidence/manifest.txt"
container=etcd-backup-keeper-arch-test-$$-$RANDOM
podman run --rm --name "$container" --pull=never --network=none \
    --security-opt=no-new-privileges --pids-limit=128 --memory=1g \
    --volume "$evidence:/evidence:ro" \
    --volume "$repo/packaging/tests/arch-lifecycle.sh:/checks/arch-lifecycle.sh:ro" \
    --env SYSTEMD_OFFLINE=1 "$image" bash /checks/arch-lifecycle.sh \
    "$version" "$upgrade" "$kind" "$(sha256sum "$binary" | cut -d ' ' -f 1)" "${SOURCE_DATE_EPOCH:-0}" "$expected" \
    2>&1 | tee "$evidence/lifecycle.log"
container=
printf 'PASS: %s Arch packaging lifecycle (offline only); evidence: %s\n' "$kind" "$evidence"

#!/usr/bin/env bash
# Offline dpkg lifecycle in a disposable rootless Ubuntu container, never host dpkg.
set -euo pipefail
export LC_ALL=C
umask 077
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
binary= assets= evidence= kind=
image=localhost/etcd-backup-keeper-test-ubuntu:24.04
version=0.0.0~packaging1
upgrade=0.0.0~packaging2
usage() {
    cat <<'EOF'
Usage: scripts/package-test.sh --binary PATH --binary-kind stub|real
       --assets-dir PROJECT_ROOT --evidence-dir NEW_DIR [--image LOCAL_IMAGE]
       [--version VERSION --upgrade-version NEWER_VERSION]
Builds twice to check reproducibility, then tests dpkg install, config edit,
upgrade with changed template, remove and purge. Runs only the binary's version
command, never backup. The local image must contain dpkg and systemd tools.
EOF
}
die() { printf 'package-test: %s\n' "$*" >&2; exit 1; }
while (($#)); do
    case "$1" in
        --binary|--assets-dir|--evidence-dir|--binary-kind|--image|--version|--upgrade-version)
            (($# >= 2)) || die "missing value for $1"
            case "$1" in
                --binary) binary=$2 ;; --assets-dir) assets=$2 ;;
                --evidence-dir) evidence=$2 ;; --binary-kind) kind=$2 ;;
                --image) image=$2 ;; --version) version=$2 ;; --upgrade-version) upgrade=$2 ;;
            esac
            shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done
[[ -n $binary && -n $assets && -n $evidence && ( $kind == stub || $kind == real ) ]] || { usage >&2; exit 1; }
[[ $image =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] || die 'invalid local image reference'
[[ -f $binary && ! -L $binary && -r $binary && -x $binary ]] || die 'binary must be a readable executable regular file, not a symlink'
command -v podman >/dev/null || die 'rootless Podman required'
[[ $(podman info --format '{{.Host.Security.Rootless}}') == true ]] || die 'requires rootless Podman'
[[ $(podman image inspect "$image" --format '{{.Os}}/{{.Architecture}}') == linux/amd64 ]] || die 'local image must be Linux amd64'
[[ ! -e $evidence && ! -L $evidence ]] || die 'evidence directory must not exist'
binary=$(realpath -e -- "$binary")
assets=$(realpath -e -- "$assets")
mkdir -p -- "$evidence"
evidence=$(realpath -e -- "$evidence")
[[ $evidence != *:* && $evidence != *$'\n'* ]] || die 'evidence path contains colon/newline'
container=
cleanup() {
    if [[ -n $container ]]; then podman rm -f "$container" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
# Copy only explicit stable assets, never mount a source checkout into the test.
for tree in original-assets upgrade-assets; do
    install -d -m 0700 "$evidence/$tree/docs" "$evidence/$tree/integration"
    for relative in README.md docs/quickstart.md docs/requirements.md docs/operations.md docs/troubleshooting.md docs/restore-drill.md integration/README.md config.example.toml LICENSE THIRD_PARTY_NOTICES; do
        [[ -f $assets/$relative && ! -L $assets/$relative ]] || die "missing/nonregular/symlink asset: $relative"
        [[ $(realpath -e -- "$assets/$relative") == "$assets/"* ]] || die "asset escapes source: $relative"
        install -m 0600 "$assets/$relative" "$evidence/$tree/$relative"
    done
done
# Force a real conffile conflict without changing the product example source.
printf '\n# Changed default template for the packaging lifecycle test only.\n' >> "$evidence/upgrade-assets/config.example.toml"
maintainer='Packaging Test <packaging-test@example.invalid>'
for target in initial reproducibility; do
    "$repo/scripts/package-build.sh" --binary "$binary" --version "$version" \
        --assets-dir "$evidence/original-assets" --output-dir "$evidence/$target" \
        --maintainer "$maintainer" --image "$image" > "$evidence/build-$target.log" 2>&1
done
"$repo/scripts/package-build.sh" --binary "$binary" --version "$upgrade" \
    --assets-dir "$evidence/upgrade-assets" --output-dir "$evidence/upgrade" \
    --maintainer "$maintainer" --image "$image" > "$evidence/build-upgrade.log" 2>&1
first=etcd-backup-keeper_${version}_amd64.deb
second=etcd-backup-keeper_${upgrade}_amd64.deb
cmp "$evidence/initial/$first" "$evidence/reproducibility/$first"
{
    printf 'binary_kind=%s\n' "$kind"
    printf 'scope=offline packaging scaffold; not backup or service acceptance\n'
    printf 'image=%s\n' "$(podman image inspect "$image" --format '{{.Id}}')"
    printf 'SOURCE_DATE_EPOCH=%s\n' "${SOURCE_DATE_EPOCH:-0}"
    sha256sum "$binary" "$evidence/initial/$first" "$evidence/reproducibility/$first" "$evidence/upgrade/$second"
} | tee "$evidence/manifest.txt"
container=etcd-backup-keeper-package-test-$$-$RANDOM
podman run --rm --name "$container" --pull=never --network=none \
    --security-opt=no-new-privileges --pids-limit=128 --memory=1g \
    --volume "$evidence:/evidence:ro" \
    --volume "$repo/packaging/tests/lifecycle.sh:/checks/lifecycle.sh:ro" \
    --env SYSTEMD_OFFLINE=1 "$image" bash /checks/lifecycle.sh \
    "$version" "$upgrade" "$kind" "$(sha256sum "$binary" | cut -d ' ' -f 1)" \
    2>&1 | tee "$evidence/lifecycle.log"
container=
printf 'PASS: %s packaging lifecycle (offline only); evidence: %s\n' "$kind" "$evidence"

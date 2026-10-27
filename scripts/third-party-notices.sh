#!/usr/bin/env bash
# Generate notices for modules actually imported by the product, not test-only deps.
set -euo pipefail
export LC_ALL=C
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$repo"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
# Keep Go's resolved dependency graph authoritative; no source/cache guessing.
go list -deps -f '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}} {{.Module.Version}} {{.Module.Dir}}{{end}}{{end}}' ./cmd/etcd-backup-keeper | sed '/^$/d' | sort -u > "$work/modules"
{
    printf 'Third-party notices for etcd-backup-keeper\n\n'
    printf 'Generated from the product Go dependency graph. These licenses apply to\ntheir respective dependencies; the project license is in LICENSE.\n\n'
    printf '===== Go standard library =====\n\n'
    cat "$(go env GOROOT)/LICENSE"
    while read -r module version dir; do
        printf '\n===== %s %s =====\n' "$module" "$version"
        found=false
        for name in LICENSE LICENSE.txt LICENSE.md LICENSE.bsd COPYING COPYING.txt NOTICE NOTICE.txt; do
            if [[ -f $dir/$name ]]; then
                printf '\n--- %s ---\n\n' "$name"
                cat "$dir/$name"
                printf '\n'
                case "$name" in LICENSE*|COPYING*) found=true ;; esac
            fi
        done
        if [[ $found != true ]]; then
            printf 'Missing module license: %s %s\n' "$module" "$version" >&2
            exit 1
        fi
    done < "$work/modules"
} > "$work/notices"
# Publish only after every module has an attributable license.
# The final section separator is not part of a license; omit its empty line.
sed '${/^$/d;}' "$work/notices"

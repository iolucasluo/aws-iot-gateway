#!/bin/sh

create_release_source() (
    release_source_repository=$1
    release_source_commit=$2
    release_source_destination=$3
    release_source_archive=$(mktemp "${TMPDIR:-/tmp}/gateway-release-source.XXXXXX.tar") || return 1

    if [ ! -d "$release_source_destination" ] ||
        [ -n "$(find "$release_source_destination" -mindepth 1 -print -quit)" ]; then
        rm -f "$release_source_archive"
        return 1
    fi
    if ! git -C "$release_source_repository" cat-file -e "$release_source_commit^{commit}" 2>/dev/null ||
        ! git -C "$release_source_repository" archive --format=tar \
            --output "$release_source_archive" "$release_source_commit" ||
        ! tar -xf "$release_source_archive" -C "$release_source_destination"; then
        rm -f "$release_source_archive"
        return 1
    fi
    rm -f "$release_source_archive"
)

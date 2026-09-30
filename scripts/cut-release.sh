#!/usr/bin/env bash
# Computes the next SemVer tag from the latest v* tag on origin/main,
# creates an annotated tag on origin/main's HEAD, and pushes it — which
# triggers the CI release workflow. This is the "fully automated
# deployment" path; CI builds and publishes that tag.
#
# Usage: cut-release.sh <patch|minor|major>
set -euo pipefail

bump="${1:?usage: cut-release.sh <patch|minor|major>}"
case "$bump" in
    patch|minor|major) ;;
    *)
        echo "error: bump must be patch, minor, or major (got: $bump)" >&2
        exit 64
        ;;
esac

git fetch origin main --tags --quiet

existing="$(git tag --points-at origin/main --list 'v*')"
if [ -n "$existing" ]; then
    echo "error: origin/main is already tagged for release: $existing" >&2
    exit 1
fi

latest="$(git describe --tags --abbrev=0 --match 'v*' origin/main 2>/dev/null || echo "v0.0.0")"
if [[ ! "$latest" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "error: latest tag '$latest' is not plain vX.Y.Z — bump it manually" >&2
    exit 1
fi
IFS=. read -r major minor patch <<< "${latest#v}"

case "$bump" in
    major) tag="v$((major + 1)).0.0" ;;
    minor) tag="v${major}.$((minor + 1)).0" ;;
    patch) tag="v${major}.${minor}.$((patch + 1))" ;;
esac

echo "bumping ${latest} -> ${tag} (${bump})"

# Check main's CI result before creating a release tag so failed commits
# do not consume a version number.
sha="$(git rev-parse origin/main)"

if ! command -v gh >/dev/null 2>&1; then
    echo "error: gh is required to verify ci before tagging (brew install gh)" >&2
    exit 1
fi

# A run record appears a beat after the push that created it, so an empty
# list this soon after `git push` means "not registered yet" rather than
# "never ran" — poll briefly before believing it.
deadline=$((SECONDS + 120))
while :; do
    if ! run="$(gh run list --workflow ci.yml --branch main --commit "$sha" \
        --limit 1 --json databaseId,status,conclusion,url \
        --jq '.[0] // empty | [.databaseId, .status, .conclusion // "pending", .url] | @tsv')"; then
        echo "error: could not query ci runs — check 'gh auth status'" >&2
        exit 1
    fi
    [ -n "$run" ] && break
    if [ "$SECONDS" -ge "$deadline" ]; then
        echo "error: no ci run for ${sha:0:8} on main after 120s — is the commit pushed?" >&2
        exit 1
    fi
    sleep 5
done
# Tab counts as IFS whitespace, so a run of them collapses into one
# delimiter and an empty field would shift every later field left —
# hence the "pending" placeholder above for a run that has not concluded.
IFS=$'\t' read -r run_id run_status run_conclusion run_url <<< "$run"

if [ "$run_status" != "completed" ]; then
    echo "waiting for ci on ${sha:0:8} — $run_url"
    if ! gh run watch "$run_id" --exit-status; then
        echo "error: ci failed — no tag created: $run_url" >&2
        exit 1
    fi
elif [ "$run_conclusion" != "success" ]; then
    echo "error: ci concluded '$run_conclusion' — no tag created: $run_url" >&2
    exit 1
fi

# main can move while ci runs; re-reading it keeps the tag on the exact
# commit that run vouched for.
git fetch origin main --quiet
if [ "$(git rev-parse origin/main)" != "$sha" ]; then
    echo "error: origin/main moved while ci ran — no tag created" >&2
    exit 1
fi

git tag --annotate "$tag" --message "$tag" origin/main
git push origin "$tag"
echo "pushed $tag — the CI release workflow is now shipping it:"
# Derived from the remote so the link survives a move between owners.
slug="$(git remote get-url origin | sed -E 's#^.*github\.com[:/]##; s#\.git$##')"
echo "  https://github.com/${slug}/actions"

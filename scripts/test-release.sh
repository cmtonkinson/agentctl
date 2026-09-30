#!/usr/bin/env bash
# Exercise tagging against a disposable remote and inspect local release archives.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
remote="$fixture/remote.git"
work="$fixture/work"
export GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.invalid

fail() { printf 'release test: %s\n' "$*" >&2; exit 1; }
git init --bare -q "$remote"
git init -q -b main "$work"
git -C "$work" remote add origin "$remote"
printf 'first\n' > "$work/source.txt"
git -C "$work" add source.txt
git -C "$work" commit -qm first
git -C "$work" push -q -u origin main
mkdir -p "$work/scripts" "$fixture/bin"
cp "$repo_root/Makefile" "$work/Makefile"
cp "$repo_root/scripts/cut-release.sh" "$work/scripts/cut-release.sh"

# gh reports a chosen CI state; in the move case it advances main mid-deploy.
cat > "$fixture/bin/gh" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1 $2" == "run list" ]]; then
  [[ " $* " == *" --workflow ci.yml "* ]] || exit 9
  case "$MOCK_CI" in
    success) printf '1\tcompleted\tsuccess\thttps://example.invalid/run\n' ;;
    failed) printf '1\tcompleted\tfailure\thttps://example.invalid/run\n' ;;
    pending) printf '1\tin_progress\tpending\thttps://example.invalid/run\n' ;;
    move)
      printf 'moved\n' >> "$MOVER/source.txt"
      git -C "$MOVER" add source.txt
      git -C "$MOVER" commit -qm moved
      git -C "$MOVER" push -q origin main
      printf '1\tcompleted\tsuccess\thttps://example.invalid/run\n'
      ;;
  esac
elif [[ "$1 $2" == "run watch" ]]; then
  [[ "$MOCK_CI" == pending ]] || exit 9
else
  exit 9
fi
MOCK
chmod +x "$fixture/bin/gh"
export PATH="$fixture/bin:$PATH"
export MOCK_CI=success

# The Make interface tags remote main even while the local tree has untracked files.
make -s -C "$work" deploy patch > "$fixture/output" 2>&1
git -C "$work" ls-remote --exit-code --tags --refs origin v0.0.1 > /dev/null || fail 'patch tag missing'
if make -s -C "$work" deploy patch > "$fixture/output" 2>&1; then
  fail 'already tagged commit accepted another release'
fi
grep -q 'already tagged' "$fixture/output" || fail 'duplicate tag error missing'

advance() {
  printf '%s\n' "$1" >> "$work/source.txt"
  git -C "$work" add source.txt
  git -C "$work" commit -qm "$1"
  git -C "$work" push -q origin main
}
advance second
export MOCK_CI=pending
make -s -C "$work" deploy minor > "$fixture/output" 2>&1
grep -q 'waiting for ci' "$fixture/output" || fail 'pending CI was not watched'
git -C "$work" ls-remote --exit-code --tags --refs origin v0.1.0 > /dev/null || fail 'minor tag missing'

advance third
export MOCK_CI=success
make -s -C "$work" deploy major > "$fixture/output" 2>&1
git -C "$work" ls-remote --exit-code --tags --refs origin v1.0.0 > /dev/null || fail 'major tag missing'

advance fourth
export MOCK_CI=failed
if make -s -C "$work" deploy patch > "$fixture/output" 2>&1; then
  fail 'failed CI created a release'
fi
grep -q "ci concluded 'failure'" "$fixture/output" || fail 'failed CI error missing'
if git -C "$work" ls-remote --exit-code --tags --refs origin v1.0.1 > /dev/null; then
  fail 'failed CI pushed a tag'
fi

export MOCK_CI=move
export MOVER="$fixture/mover"
git clone -q -b main "$remote" "$MOVER"
if make -s -C "$work" deploy patch > "$fixture/output" 2>&1; then
  fail 'moving main created a release'
fi
grep -q 'origin/main moved' "$fixture/output" || fail 'moving main error missing'
if git -C "$work" ls-remote --exit-code --tags --refs origin v1.0.1 > /dev/null; then
  fail 'moving main pushed a tag'
fi

git -C "$work" pull -q --ff-only origin main
git -C "$work" tag -a v1.2 -m v1.2
git -C "$work" push -q origin v1.2
advance fifth
export MOCK_CI=success
if make -s -C "$work" deploy patch > "$fixture/output" 2>&1; then
  fail 'malformed previous tag was used as SemVer'
fi
grep -q 'not plain vX.Y.Z' "$fixture/output" || fail 'malformed tag error missing'

if make -s -C "$work" deploy > "$fixture/output" 2>&1; then
  fail 'deploy without a bump succeeded'
fi
grep -q 'usage: make deploy' "$fixture/output" || fail 'deploy usage error missing'

if "$repo_root/scripts/build-release.sh" v01.2.3 "$fixture/dist" > "$fixture/output" 2>&1; then
  fail 'invalid SemVer accepted'
fi
(cd "$repo_root" && ./scripts/build-release.sh v1.2.3 "$fixture/dist")
archive_dir="$fixture/dist/v1.2.3"
[[ "$(find "$archive_dir" -type f | wc -l | tr -d ' ')" == 6 ]] || fail 'wrong archive count'
(cd "$archive_dir" && shasum -a 256 -c SHA256SUMS > /dev/null)
host="$(go env GOOS)_$(go env GOARCH)"
mkdir "$fixture/extracted"
tar -xzf "$archive_dir/agentctl_v1.2.3_${host}.tar.gz" -C "$fixture/extracted"
[[ "$("$fixture/extracted/agentctl" --version)" == 'agentctl v1.2.3' ]] || fail 'archive version mismatch'
windows_entries="$(unzip -Z -1 "$archive_dir/agentctl_v1.2.3_windows_amd64.zip")"
grep -qx 'agentctl.exe' <<< "$windows_entries" || fail 'Windows binary missing'
printf 'release tests passed\n'

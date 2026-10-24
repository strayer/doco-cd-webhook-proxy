#!/usr/bin/env bash
# Tag the next release. Lists the PRs merged since the last final (non
# pre-release) v* tag, computes the version bump from their labels, and
# confirms with the user before creating a signed annotated tag and pushing
# it. The push triggers the repository's tag-based release workflow.
#
# Label → bump mapping (highest wins):
#   breaking              → major (minor while still on 0.x)
#   feature, enhancement  → minor
#   anything else         → patch
#
# Pre-releases use the canonical form vX.Y.Z-rc.N. With --rc the next
# version's release candidate is proposed (v1.3.0-rc.1, then v1.3.0-rc.2, ...).
# A plain run after release candidates releases the final version; the bump
# is always computed against the last *final* tag. Legacy tags such as
# v1.0.0-rc1 are understood as release candidates of v1.0.0.
#
# Usage: scripts/release.sh [--rc] [--dry-run] [vX.Y.Z | vX.Y.Z-rc.N]
#   --rc       propose a release candidate instead of the final version
#   --dry-run  print the PR list and proposed version, then exit without
#              tagging (branch/clean/sync checks only warn)
#   <version>  explicit version, skips the computation (cannot combine
#              with --rc)
set -euo pipefail

die() { echo "error: $*" >&2; exit 1; }

FINAL_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'
RC_RE='^v([0-9]+\.[0-9]+\.[0-9]+)-rc\.?([0-9]+)$'

valid_version() { [[ "$1" =~ $FINAL_RE || "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]]; }

# last_final_tag <tags>: highest vX.Y.Z tag (empty when none)
last_final_tag() { { grep -E "$FINAL_RE" <<<"$1" || true; } | sort -V | tail -n1; }

# bump_version <vX.Y.Z> <major|minor|patch>: apply a bump (breaking on 0.x → minor)
bump_version() {
  local major minor patch
  IFS=. read -r major minor patch <<<"${1#v}"
  if [[ "$2" == "major" && "$major" == "0" ]]; then
    set -- "$1" minor
  fi
  case "$2" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
  esac
  echo "v$major.$minor.$patch"
}

# next_rc_number <base> <tags>: next N for base's -rc.N (1 when none yet)
next_rc_number() {
  local max=0 t
  while IFS= read -r t; do
    if [[ "$t" =~ $RC_RE && "v${BASH_REMATCH[1]}" == "$1" ]]; then
      ((10#${BASH_REMATCH[2]} > max)) && max=$((10#${BASH_REMATCH[2]}))
    fi
  done <<<"$2"
  echo $((max + 1))
}

# next_version <rc:0|1> <bump> <tags>: prints "<version>|<reason>"
next_version() {
  local rc="$1" bump="$2" tags="$3" last base reason t best=""
  last="$(last_final_tag "$tags")"
  if [[ -n "$last" ]]; then
    base="$(bump_version "$last" "$bump")"
    reason="$bump bump from $last"
  else
    # No final release yet: continue the highest release-candidate base if
    # there is one (v1.0.0-rc1 → v1.0.0), otherwise this is the first release.
    while IFS= read -r t; do
      [[ "$t" =~ $RC_RE ]] && best+="v${BASH_REMATCH[1]}"$'\n'
    done <<<"$tags"
    best="$(sort -V <<<"$best" | tail -n1)"
    if [[ -n "${best//$'\n'/}" ]]; then
      base="$best"
      reason="no final release yet, continuing release candidates of $base"
    else
      base="v0.1.0"
      reason="first release"
    fi
  fi
  if [[ "$rc" == "1" ]]; then
    echo "$base-rc.$(next_rc_number "$base" "$tags")|release candidate, $reason"
  else
    echo "$base|$reason"
  fi
}

main() {
  local rc=0 dry=0 override="" arg
  for arg in "$@"; do
    case "$arg" in
      --rc) rc=1 ;;
      --dry-run) dry=1 ;;
      -h | --help) sed -n '2,/^set -e/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
      -*) die "unknown flag $arg" ;;
      *) [[ -z "$override" ]] || die "only one version may be given"; override="$arg" ;;
    esac
  done
  if [[ -n "$override" ]]; then
    valid_version "$override" || die "version must look like v1.2.3 or v1.2.3-rc.1"
    [[ "$rc" == 0 ]] || die "--rc cannot be combined with an explicit version"
  fi

  # Repo state checks: fatal for a real release, warnings for --dry-run.
  problem() { if [[ "$dry" == 1 ]]; then echo "warning: $*" >&2; else die "$*"; fi; }
  git fetch --quiet --tags origin main
  [[ "$(git rev-parse --abbrev-ref HEAD)" == "main" ]] || problem "switch to main first"
  [[ -z "$(git status --porcelain)" ]] || problem "working tree is dirty"
  [[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] || problem "main is not in sync with origin/main"

  local tags last_final commits bump="patch" num oid labels title
  tags="$(git tag --list 'v*')"
  last_final="$(last_final_tag "$tags")"

  # List the merged PRs that would ship in this release (all of them when no
  # final tag exists yet) and derive the bump from their labels. PRs are
  # squash-merged, so mergeCommit.oid is the commit on main.
  if [[ -n "$last_final" ]]; then
    commits="$(git rev-list "$last_final..HEAD")"
    if [[ -z "$commits" ]]; then problem "no commits since $last_final"; fi
    echo "PRs merged since $last_final:"
  else
    commits="$(git rev-list HEAD)"
    echo "No previous final v* tag. All merged PRs:"
  fi

  # Fields are separated by the ASCII unit separator: tab is whitespace to
  # read, so an empty label list would make the title land in the labels field.
  while IFS=$'\x1f' read -r num oid labels title; do
    [[ -n "$oid" ]] || continue
    grep -qx "$oid" <<<"$commits" || continue
    echo "  #$num [$labels] $title"
    case ",$labels," in
      *,breaking,*) bump="major" ;;
      *,feature,* | *,enhancement,*) [[ "$bump" == "major" ]] || bump="minor" ;;
    esac
  done < <(gh pr list --state merged --base main --limit 200 \
    --json number,title,labels,mergeCommit \
    --jq '.[] | [(.number|tostring), (.mergeCommit.oid // ""), ([.labels[].name] | join(",")), .title] | join("\u001f")')

  local next reason
  if [[ -n "$override" ]]; then
    next="$override"
    reason="explicit version, computation skipped"
  else
    IFS='|' read -r next reason <<<"$(next_version "$rc" "$bump" "$tags")"
  fi
  echo
  echo "Proposed release: $next ($reason)"

  if [[ "$dry" == 1 ]]; then
    echo "Dry run: nothing tagged or pushed."
    exit 0
  fi

  printf 'Confirm [y], enter a different version (vX.Y.Z or vX.Y.Z-rc.N), or abort [N]: '
  read -r answer
  case "$answer" in
    y | Y | yes) ;;
    v*)
      valid_version "$answer" || die "version must look like v1.2.3 or v1.2.3-rc.1"
      next="$answer"
      ;;
    *) die "aborted" ;;
  esac

  grep -qxF "$next" <<<"$(git tag --list)" && die "tag $next already exists"

  git tag -s "$next" -m "Release $next"
  git push origin "$next"
  echo "Pushed $next — watch $(gh repo view --json url --jq .url)/actions"
}

# Allow sourcing for tests without running anything.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

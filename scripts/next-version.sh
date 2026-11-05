#!/usr/bin/env bash
# Prints GitHub Actions outputs (version=X.Y.Z, skip=true|false) for the next release.
#   - HEAD already tagged v*          -> skip
#   - Chart.yaml version > latest tag -> Chart.yaml version (intentional minor/major bump)
#   - otherwise                       -> latest tag with patch + 1
set -euo pipefail

chart_file="${1:-charts/github-app-factory/Chart.yaml}"
semver='^[0-9]+\.[0-9]+\.[0-9]+$'

chart_version="$(awk '/^version:/ {print $2; exit}' "$chart_file" | tr -d '"'"'")"
if [[ ! "$chart_version" =~ $semver ]]; then
  echo "Chart version '$chart_version' is not MAJOR.MINOR.PATCH" >&2
  exit 1
fi

if head_tag="$(git tag --points-at HEAD --list 'v*' | head -n1)" && [[ -n "$head_tag" ]]; then
  echo "HEAD is already released as $head_tag" >&2
  echo "skip=true"
  echo "version=${head_tag#v}"
  exit 0
fi

latest="$(git tag --list 'v*' | sed 's/^v//' | grep -E "$semver" | sort -V | tail -n1 || true)"

if [[ -z "$latest" ]]; then
  next="$chart_version"
elif [[ "$(printf '%s\n%s\n' "$latest" "$chart_version" | sort -V | tail -n1)" == "$chart_version" && "$chart_version" != "$latest" ]]; then
  next="$chart_version"
else
  IFS=. read -r major minor patch <<<"$latest"
  next="${major}.${minor}.$((patch + 1))"
fi

echo "skip=false"
echo "version=$next"

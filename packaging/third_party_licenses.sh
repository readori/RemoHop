#!/usr/bin/env bash
# Writes the license texts of every third-party module linked into remohop (and
# the quick-support program cmd/remohop-quick, which links the same set), for
# every release target, to stdout. Release archives ship its output as
# THIRD_PARTY_LICENSES.txt next to the binary, which the MIT, BSD, ISC and
# Apache licenses of those modules require.
#
# Usage: packaging/third_party_licenses.sh > THIRD_PARTY_LICENSES.txt
set -euo pipefail
cd "$(dirname "$0")/.."
cache=$(go env GOMODCACHE)
mods=$(for os in linux darwin windows; do
	GOOS=$os go list -deps -f '{{with .Module}}{{.Path}}@{{.Version}}{{end}}' ./cmd/remohop ./cmd/remohop-quick
done | grep '@v' | grep -v '^github.com/readori/RemoHop@' | sort -u)

echo "Third-party software in remohop"
echo "==========================="
echo
echo "remohop is licensed under the GNU Affero General Public License, version 3"
echo "(see LICENSE). It includes the following modules under their own licenses."
echo "It also includes the Go standard library and runtime, under the license of"
echo "the Go project, reproduced first below."
echo
echo "------------------------------------------------------------------------"
echo "Go standard library and runtime ($(go env GOVERSION))"
echo "------------------------------------------------------------------------"
cat "$(go env GOROOT)/LICENSE"
for m in $mods; do
	# The module cache escapes upper-case letters as !lower. (Written without GNU
	# sed's \L so that it runs on macOS too, where the signing workflow uses it.)
	dir="$cache/$(printf '%s' "$m" | sed 's/[A-Z]/!&/g' | tr '[:upper:]' '[:lower:]')"
	f=$(ls "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* 2>/dev/null | head -n 1 || true)
	echo
	echo "------------------------------------------------------------------------"
	echo "$m"
	echo "------------------------------------------------------------------------"
	if [ -n "$f" ]; then cat "$f"; else echo "(no license file found in the module; check upstream)"; fi
done

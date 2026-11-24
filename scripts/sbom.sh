#!/usr/bin/env bash
# CycloneDX 1.5 SBOM for remohop, built from the module information Go embeds in every
# binary (`go version -m`). No third-party tool, nothing to download. The component
# list is the union over all the binaries given (build tags change which modules a
# platform links), plus the Go toolchain that built them.
#
# Usage: scripts/sbom.sh VERSION BINARY... > remohop.cdx.json
#   e.g. scripts/sbom.sh v1.2.3 dist/remohop-linux-amd64 dist/remohop-windows-amd64.exe
#
# SOURCE_DATE_EPOCH (if set) fixes the timestamp, so the output is reproducible.
set -euo pipefail
VERSION="${1:?usage: sbom.sh VERSION BINARY...}"; shift
[[ $# -gt 0 ]] || { echo "sbom.sh: no binaries given" >&2; exit 2; }
command -v jq >/dev/null || { echo "sbom.sh: jq is required" >&2; exit 1; }

ts="$(date -u ${SOURCE_DATE_EPOCH:+-d "@$SOURCE_DATE_EPOCH"} +%Y-%m-%dT%H:%M:%SZ)"

# One TSV line per fact: "go<TAB>1.26.0" or "mod<TAB>path<TAB>version<TAB>h1".
# A `=>` line (a replace directive) supersedes the dep line before it.
facts="$(for b in "$@"; do go version -m "$b"; done | awk -F'\t' '
	function flush() { if (pend != "") print pend; pend = "" }
	/^[^\t]+: go[0-9]/ { flush(); split($0, a, ": go"); print "go\t" a[2]; next }
	$2 == "dep" { flush(); pend = "mod\t" $3 "\t" $4 "\t" $5; next }
	$2 == "=>"  { pend = "mod\t" $3 "\t" $4 "\t" $5; next }
	{ flush() }
	END { flush() }
' | sort -u)"

jq -Rn --arg version "$VERSION" --arg ts "$ts" '
	[inputs | split("\t")] as $f
	| ($f | map(select(.[0] == "go")) | .[0][1]) as $go
	| {
		bomFormat: "CycloneDX", specVersion: "1.5", version: 1,
		metadata: {
			timestamp: $ts,
			component: {type: "application", name: "remohop", version: $version,
				purl: ("pkg:golang/github.com/readori/RemoHop@" + $version),
				licenses: [{license: {id: "AGPL-3.0-only"}}]},
			tools: {components: [{type: "application", name: "scripts/sbom.sh (go version -m)"}]}
		},
		components: (
			[{type: "library", name: "stdlib", version: ("go" + $go), purl: ("pkg:golang/std@" + $go),
				licenses: [{license: {id: "BSD-3-Clause"}}]}]
			+ ($f | map(select(.[0] == "mod" and .[1] != "github.com/readori/RemoHop")) | unique_by(.[1])
				| map({type: "library", name: .[1], version: .[2], purl: ("pkg:golang/" + .[1] + "@" + .[2]),
					properties: (if (.[3] // "") != "" then [{name: "go:h1", value: .[3]}] else [] end)}))
		)
	}' <<<"$facts"

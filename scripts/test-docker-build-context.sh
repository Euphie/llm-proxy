#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
probe_dir=$(mktemp -d "$repo_root/.docker-context-probe.XXXXXX")
output_dir=$(mktemp -d "${TMPDIR:-/tmp}/llm-proxy-context-output.XXXXXX")

cleanup() {
	case "$probe_dir" in
		"$repo_root"/.docker-context-probe.*) rm -rf -- "$probe_dir" ;;
	esac
	case "$output_dir" in
		"${TMPDIR:-/tmp}"/llm-proxy-context-output.*) rm -rf -- "$output_dir" ;;
	esac
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$probe_dir/custom/runtime"
: >"$probe_dir/custom/runtime/llm-proxy.db"
: >"$probe_dir/custom/runtime/llm-proxy.db-wal"
: >"$probe_dir/custom/runtime/llm-proxy.db-shm"

DOCKER_BUILDKIT=1 docker build \
	--no-cache \
	--output "type=local,dest=$output_dir" \
	-f - \
	"$repo_root" <<'EOF'
FROM alpine:3.20 AS context-probe
WORKDIR /context
COPY . .
RUN leaked=0; \
	if test -e /context/config.yaml; then echo "legacy config reached build context"; leaked=1; fi; \
	if find /context -type f \( -name llm-proxy.db -o -name llm-proxy.db-wal -o -name llm-proxy.db-shm \) -print -quit | grep -q .; then \
		echo "runtime database reached build context"; leaked=1; \
	fi; \
	test "$leaked" -eq 0
RUN printf 'ok\n' >/context-probe-result

FROM scratch
COPY --from=context-probe /context-probe-result /
EOF

test "$(cat "$output_dir/context-probe-result")" = "ok"

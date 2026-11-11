#!/usr/bin/env bash
# Throwaway Postgres for the KUSO_TEST_PG_DSN-gated Go tests, safe to run
# from several git worktrees (and several invocations per worktree) at once.
#
#   hack/testdb.sh up                 start this worktree's session DB, print its DSN
#   hack/testdb.sh down               remove the session DB and any leftover run DBs
#   hack/testdb.sh status             show this worktree's containers
#   hack/testdb.sh all                go test -race ./... in every go.work module
#   hack/testdb.sh pkg <pkg>...       go test -race on the given packages
#   hack/testdb.sh changed [base]     go test the packages touched since base (origin/main)
#
# Env: TEST_PG_PORT (preferred host port), TEST_PG_CONTAINER (session
# container name), RUN (-run regexp), ARGS (extra go test flags).
#
# Each invocation gets its own database: on the session container when one
# is up (created and dropped around the run), otherwise on a fresh container
# removed on exit. Two runs never share a database, so the truncating tests
# cannot clobber each other. Within a run, go test -exec with
# server-go/cmd/pgtestexec gives every package's test binary its own database
# (<run db>_<pid>_<hex>), so packages run in parallel instead of under -p 1.
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
SUM=$(printf '%s' "$ROOT" | cksum | cut -d' ' -f1)
SESSION=${TEST_PG_CONTAINER:-kuso-test-pg-$SUM}
BASE_PORT=${TEST_PG_PORT:-$((20000 + SUM % 10000))}
IMAGE=postgres:16-alpine
LABEL=kuso.test-pg.worktree
LOGDIR=$ROOT/.testlogs

say() { echo "==> $*" >&2; }

port_in_use() {
	(exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

running() {
	[ "$(docker container inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]
}

host_port() {
	docker port "$1" 5432/tcp | head -1 | sed 's/.*://'
}

# start <name> <preferred port>: probes upward from the preferred port; a
# concurrent start that wins the same port makes docker run fail with
# "port is already allocated", which also moves us to the next one.
start() {
	local name=$1 port=$2 i
	for i in $(seq 0 49); do
		local p=$((port + i))
		port_in_use "$p" && continue
		if docker run -d --name "$name" --label "$LABEL=$ROOT" \
			-p "127.0.0.1:$p:5432" \
			-e POSTGRES_USER=kuso -e POSTGRES_PASSWORD=kuso -e POSTGRES_DB=kuso_test \
			"$IMAGE" >/dev/null 2>&1; then
			wait_ready "$name"
			return 0
		fi
		docker rm -fv "$name" >/dev/null 2>&1 || true
	done
	echo "no free host port in $port..$((port + 49))" >&2
	return 1
}

wait_ready() {
	local i
	for i in $(seq 1 60); do
		# 127.0.0.1, not the socket: the image's init phase runs a
		# socket-only server that it restarts before it is really ready.
		docker exec "$1" pg_isready -h 127.0.0.1 -U kuso -d kuso_test >/dev/null 2>&1 && return 0
		sleep 1
	done
	echo "postgres in $1 never became ready" >&2
	return 1
}

psql_in() {
	docker exec "$1" psql -v ON_ERROR_STOP=1 -qAt -U kuso -d kuso_test -c "$2"
}

cmd_up() {
	if running "$SESSION"; then
		say "session DB already up"
	else
		docker rm -fv "$SESSION" >/dev/null 2>&1 || true
		start "$SESSION" "$BASE_PORT"
	fi
	local p
	p=$(host_port "$SESSION")
	say "container $SESSION on 127.0.0.1:$p"
	echo "postgres://kuso:kuso@localhost:$p/kuso_test?sslmode=disable"
}

cmd_down() {
	local ids
	ids=$(docker ps -aq --filter "label=$LABEL=$ROOT")
	if [ -n "$ids" ]; then
		# shellcheck disable=SC2086
		docker rm -fv $ids >/dev/null
		say "removed $(echo "$ids" | wc -l | tr -d ' ') container(s)"
	else
		say "nothing to remove"
	fi
}

cmd_status() {
	docker ps -a --filter "label=$LABEL=$ROOT" --format '{{.Names}}\t{{.Status}}\t{{.Ports}}'
}

# with_db <cmd...>: runs cmd with KUSO_TEST_PG_DSN pointing at a database
# private to this invocation, logging to .testlogs/.
# server/db/ephemeral are globals: the EXIT trap reads them after with_db
# has returned.
with_db() {
	local port rc=0
	ephemeral=0
	if running "$SESSION"; then
		server=$SESSION
		db="kuso_test_$$_$RANDOM"
	else
		server="$SESSION-run-$$"
		ephemeral=1
		db=kuso_test
	fi
	cleanup() {
		if [ "$ephemeral" = 1 ]; then
			docker rm -fv "$server" >/dev/null 2>&1 || true
		else
			# pgtestexec drops its per-package databases itself; this also
			# catches the ones an interrupted run left behind.
			local d
			for d in $db $(psql_in "$server" "SELECT datname FROM pg_database WHERE datname LIKE '${db}\_%'" 2>/dev/null); do
				psql_in "$server" "DROP DATABASE IF EXISTS $d WITH (FORCE)" >/dev/null 2>&1 || true
			done
		fi
	}
	trap cleanup EXIT
	trap 'exit 130' INT TERM
	if [ "$ephemeral" = 1 ]; then
		start "$server" "$((BASE_PORT + 1))"
	else
		psql_in "$server" "CREATE DATABASE $db" >/dev/null
	fi
	port=$(host_port "$server")
	export KUSO_TEST_PG_DSN="postgres://kuso:kuso@localhost:$port/$db?sslmode=disable"
	mkdir -p "$LOGDIR"
	local log
	log="$LOGDIR/$(date +%Y%m%d-%H%M%S)-$$.log"
	say "postgres: container $server, 127.0.0.1:$port, database $db"
	say "log: $log"
	"$@" 2>&1 | tee "$log" || rc=$?
	say "postgres was: container $server, 127.0.0.1:$port, database $db ($([ "$ephemeral" = 1 ] && echo removed || echo dropped))"
	say "log: $log"
	return "$rc"
}

# build_exec: the go test -exec wrapper that gives each package's test
# binary its own database.
build_exec() {
	local dir=${TMPDIR:-/tmp}/kuso-pgtestexec-$SUM
	mkdir -p "$dir"
	(cd "$ROOT/server-go" && go build -o "$dir/pgtestexec" ./cmd/pgtestexec)
	PGTESTEXEC=$dir/pgtestexec
}

go_test_flags() {
	build_exec
	GOFLAGS_EXTRA=(-race -exec "$PGTESTEXEC")
	[ -n "${RUN:-}" ] && GOFLAGS_EXTRA+=(-run "$RUN")
	if [ -n "${ARGS:-}" ]; then
		# shellcheck disable=SC2206
		GOFLAGS_EXTRA+=($ARGS)
	fi
	return 0
}

# run_module <module dir> <pkg>...: one go test per module.
run_module() {
	local dir=$1
	shift
	echo "==> go test ${GOFLAGS_EXTRA[*]} $* ($dir)"
	(cd "$dir" && go test "${GOFLAGS_EXTRA[@]}" "$@")
}

# run_all: every go.work module in one go test, so compiles and packages of
# all modules share the parallelism (patterns are module paths, which
# workspace mode resolves from the repo root).
run_all() {
	# shellcheck disable=SC2046
	run_module "$ROOT" $(go list -m -f '{{.Path}}/...')
}

# module_of <path>: the go.work module a path (relative to the repo root or
# absolute) lives in, or empty.
module_of() {
	local p=$1 m best=""
	case "$p" in /*) ;; *) p="$ROOT/${p#./}" ;; esac
	for m in $(go list -m -f '{{.Dir}}'); do
		case "$p/" in "$m"/*) [ ${#m} -gt ${#best} ] && best=$m ;; esac
	done
	echo "$best"
}

# run_pkgs <pkg>...: a package is either module-relative (./internal/x/...,
# resolved against MOD, default server-go) or repo-relative (server-go/...).
run_pkgs() {
	local pkg mod rel order="" failed=0
	local -a mods=() rels=()
	for pkg in "$@"; do
		mod=$(module_of "$pkg")
		if [ -n "$mod" ] && [ "$mod" != "$ROOT" ]; then
			local abs=$pkg
			case "$abs" in /*) ;; *) abs="$ROOT/${abs#./}" ;; esac
			abs=${abs%/}
			if [ "$abs" = "$mod" ]; then
				rel=.
			else
				rel="./${abs#"$mod"/}"
			fi
		else
			mod="$ROOT/${MOD:-server-go}"
			rel=$pkg
		fi
		mods+=("$mod")
		rels+=("$rel")
		case " $order " in *" $mod "*) ;; *) order="$order $mod" ;; esac
	done
	for mod in $order; do
		local -a these=()
		local i
		for i in "${!mods[@]}"; do
			[ "${mods[$i]}" = "$mod" ] && these+=("${rels[$i]}")
		done
		run_module "$mod" "${these[@]}" || failed=1
	done
	return "$failed"
}

run_changed() {
	local base=${1:-origin/main} pkgs
	pkgs=$(python3 "$ROOT/hack/test-changed.py" "$base")
	if [ -z "$pkgs" ]; then
		say "no Go packages changed since $base"
		return 0
	fi
	say "packages changed since $base (plus in-module dependents):"
	echo "$pkgs" | sed 's/^/    /' >&2
	# shellcheck disable=SC2086
	run_pkgs $pkgs
}

case "${1:-}" in
up) cmd_up ;;
down) cmd_down ;;
status) cmd_status ;;
all) go_test_flags; with_db run_all ;;
pkg)
	shift
	[ $# -gt 0 ] || { echo "usage: $0 pkg <pkg>..." >&2; exit 2; }
	go_test_flags
	with_db run_pkgs "$@"
	;;
changed)
	shift
	go_test_flags
	with_db run_changed "$@"
	;;
*)
	sed -n '2,12p' "$0" >&2
	exit 2
	;;
esac

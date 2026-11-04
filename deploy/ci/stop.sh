# Sourced by the CI stage scripts.
#
# stop_pids PID...: send SIGTERM to the given child processes of this shell
# and wait until each has exited (SIGKILL after 15 s), so their files can be
# removed safely afterwards. Removing a work directory right after kill
# races with what the processes still write while they shut down: vm-agent
# appends its shutdown audit record with O_CREATE and recreates
# audit.jsonl under rm -rf, which then fails with "Directory not empty" and,
# inside an EXIT trap under set -e, fails the whole step (CI run 64).
stop_pids() {
	local p killer
	for p in "$@"; do kill "$p" 2>/dev/null || true; done
	for p in "$@"; do
		(sleep 15 && kill -9 "$p" 2>/dev/null) &
		killer=$!
		wait "$p" 2>/dev/null || true
		kill "$killer" 2>/dev/null || true
		wait "$killer" 2>/dev/null || true
	done
}

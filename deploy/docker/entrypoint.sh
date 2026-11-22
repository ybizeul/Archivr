#!/bin/sh
# Starts Archivr.
#
# Without LITESTREAM_REPLICA_URL the server runs from DATA_DIR (/data), as before.
#
# With LITESTREAM_REPLICA_URL the database lives on the container's local disk at
# ARCHIVR_LOCAL_DB (default /tmp/archivr.db). SQLite's WAL is unreliable on network
# volumes, and /data is often one. The database is restored from the replica on
# start, and Litestream replicates it while Archivr runs as its child process.
# Documents stay in DATA_DIR. Archivr takes no arguments.
set -eu

if [ -z "${LITESTREAM_REPLICA_URL:-}" ]; then
	exec /usr/local/bin/archivr
fi

data_dir="${DATA_DIR:-/data}"
DB_PATH="${ARCHIVR_LOCAL_DB:-/tmp/archivr.db}"
export DB_PATH

# Restores only when the local database is missing and a replica exists. A restart
# with the database still in place does nothing.
litestream restore -config /etc/litestream.yml -if-db-not-exists -if-replica-exists "$DB_PATH"

# No replica yet, but a database already sits on the data volume. Starting now would
# create an empty database beside the real documents, so stop and seed the replica first.
if [ ! -f "$DB_PATH" ] && [ -f "$data_dir/archivr.db" ]; then
	echo "entrypoint: no replica for $DB_PATH, but $data_dir/archivr.db exists." >&2
	echo "entrypoint: seed the replica from it before enabling LITESTREAM_REPLICA_URL." >&2
	exit 1
fi

exec litestream replicate -config /etc/litestream.yml -exec /usr/local/bin/archivr

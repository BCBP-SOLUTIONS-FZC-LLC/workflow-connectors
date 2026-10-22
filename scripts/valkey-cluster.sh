#!/bin/sh
# docker-compose.yml's valkey-cluster service (profile valkey-cluster): a
# Valkey Cluster of three masters, each with one replica, in one container,
# for test/integration/valkeystore's cluster tests (TEST_VALKEY_CLUSTER_ADDRS).
#
#   valkey-cluster.sh run      start the six nodes, create the cluster, then
#                              wait; SIGTERM / SIGINT shut every node down
#                              and exit 0 (this script is the container's
#                              PID 1, so `docker compose stop` is prompt)
#   valkey-cluster.sh health   exit 0 only when every node reports
#                              cluster_state:ok and knows all six nodes, no
#                              node is flagged failing, and three replicas
#                              have their replication link up
#
# VALKEY_CLUSTER_PORTS is the range of the six client ports, "first-last"
# (compose: WC_VALKEY_CLUSTER_PORTS, default 57001-57006). The nodes announce
# 127.0.0.1 and their own port, so the host reaches each one at the address
# the cluster gives in its slot map and redirects: host and container ports
# must be identical (compose maps the range onto itself). The cluster bus
# ports, 27001-27006, stay inside the container.
#
# `--cluster create` makes the first three ports masters and assigns one
# replica to each (a failover test may swap the roles later; tests look the
# roles up instead of assuming them). Every start wipes the node directories
# and creates a fresh cluster, so a restarted container — or one a test left
# mid-migration — comes back clean instead of failing `--cluster create` on a
# non-empty node.
set -eu

ports=${VALKEY_CLUSTER_PORTS:-57001-57006}
first=${ports%-*}
last=${ports#*-}
if [ "$((last - first))" -ne 5 ]; then
  echo "VALKEY_CLUSTER_PORTS must be a range of six ports (first-last), got $ports" >&2
  exit 2
fi

run() {
  stop() {
    for p in $(seq "$first" "$last"); do
      valkey-cli -p "$p" shutdown nosave >/dev/null 2>&1 || true
    done
    wait
    exit 0
  }
  trap stop TERM INT

  i=0
  for p in $(seq "$first" "$last"); do
    rm -rf "/data/$p"
    mkdir -p "/data/$p"
    valkey-server --port "$p" --dir "/data/$p" \
      --cluster-enabled yes --cluster-config-file nodes.conf --cluster-port "$((27001 + i))" \
      --cluster-announce-ip 127.0.0.1 \
      --appendonly yes --appendfsync always --maxmemory 128mb --maxmemory-policy noeviction &
    i=$((i + 1))
  done
  nodes=""
  for p in $(seq "$first" "$last"); do
    until valkey-cli -p "$p" ping >/dev/null 2>&1; do sleep 0.2; done
    nodes="$nodes 127.0.0.1:$p"
  done
  # shellcheck disable=SC2086 # one argument per node
  valkey-cli --cluster create $nodes --cluster-replicas 1 --cluster-yes
  # Interrupted by the trap on SIGTERM; returns on its own only if every
  # node exits.
  wait
}

health() {
  replicas=0
  for p in $(seq "$first" "$last"); do
    info=$(valkey-cli -p "$p" cluster info)
    echo "$info" | grep -q '^cluster_state:ok' || return 1
    echo "$info" | grep -q '^cluster_known_nodes:6' || return 1
    if valkey-cli -p "$p" role | head -n 1 | grep -q -e slave -e replica; then
      valkey-cli -p "$p" info replication | grep -q '^master_link_status:up' || return 1
      replicas=$((replicas + 1))
    fi
  done
  [ "$replicas" -eq 3 ] || return 1
  ! valkey-cli -p "$first" cluster nodes | grep -q -e fail -e handshake -e noaddr -e disconnected
}

case "${1:-run}" in
  run) run ;;
  health) health ;;
  *) echo "usage: $0 run|health" >&2; exit 2 ;;
esac

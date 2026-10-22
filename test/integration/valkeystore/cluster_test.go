//go:build integration

package valkeystore_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// clusterAddrs is TEST_VALKEY_CLUSTER_ADDRS, seed nodes of a real Valkey
// Cluster of three masters with one replica each (docker-compose.yml's
// valkey-cluster profile, which `make docker-up` and CI start with the rest
// of the stack). The seeds need not be masters: the tests look the roles up
// (a failover swaps them). Unset, the tests skip locally; with CI set they
// fail, like every other infrastructure test.
func clusterAddrs(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("TEST_VALKEY_CLUSTER_ADDRS")
	if v == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI is set but TEST_VALKEY_CLUSTER_ADDRS is not: the Valkey Cluster test cannot be skipped in CI")
		}
		t.Skip("TEST_VALKEY_CLUSTER_ADDRS not set (make docker-up starts the valkey-cluster profile; see docker-compose.yml)")
	}
	return strings.Split(v, ",")
}

// clusterNode is one node of the test cluster, from CLUSTER NODES.
type clusterNode struct {
	id, addr string
	master   bool
	masterID string        // a replica's master
	admin    *redis.Client // the default user, on this node alone
}

// clusterTopology reads the cluster's nodes from the first seed that answers,
// with an admin client per node (closed at the end of the test).
func clusterTopology(t *testing.T, ctx context.Context, addrs []string) []*clusterNode {
	t.Helper()
	nodes, err := readTopology(ctx, addrs)
	require.NoError(t, err)
	for _, n := range nodes {
		n.admin = redis.NewClient(&redis.Options{Addr: n.addr})
		t.Cleanup(func() { _ = n.admin.Close() })
	}
	return nodes
}

func readTopology(ctx context.Context, addrs []string) ([]*clusterNode, error) {
	var lastErr error
	for _, a := range addrs {
		c := redis.NewClient(&redis.Options{Addr: a})
		out, err := c.ClusterNodes(ctx).Result()
		_ = c.Close()
		if err != nil {
			lastErr = err
			continue
		}
		var nodes []*clusterNode
		for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 8 {
				return nil, fmt.Errorf("unexpected CLUSTER NODES line %q", line)
			}
			addr, _, _ := strings.Cut(f[1], "@")
			nodes = append(nodes, &clusterNode{
				id: f[0], addr: addr, master: strings.Contains(f[2], "master"), masterID: strings.Trim(f[3], "-"),
			})
		}
		return nodes, nil
	}
	return nil, lastErr
}

func mastersOf(nodes []*clusterNode) (m []*clusterNode) {
	for _, n := range nodes {
		if n.master {
			m = append(m, n)
		}
	}
	return m
}

func replicasOf(nodes []*clusterNode) (r []*clusterNode) {
	for _, n := range nodes {
		if !n.master {
			r = append(r, n)
		}
	}
	return r
}

// byAddr is the node at addr (go-redis's spelling of a node address).
func byAddr(t *testing.T, nodes []*clusterNode, addr string) *clusterNode {
	t.Helper()
	for _, n := range nodes {
		if n.addr == addr {
			return n
		}
	}
	t.Fatalf("no cluster node at %s", addr)
	return nil
}

// waitClusterStable waits until every node agrees the cluster is whole: three
// masters serving every slot, three replicas with their replication link up,
// no node flagged failing — as after a fresh start, or once a failover or a
// fail-back has settled.
func waitClusterStable(t *testing.T, ctx context.Context, addrs []string) {
	t.Helper()
	var why string
	stable := func() bool {
		nodes, err := readTopology(ctx, addrs)
		if err != nil {
			why = err.Error()
			return false
		}
		if len(nodes) != 6 || len(mastersOf(nodes)) != 3 {
			why = fmt.Sprintf("%d nodes, %d masters", len(nodes), len(mastersOf(nodes)))
			return false
		}
		for _, n := range nodes {
			c := redis.NewClient(&redis.Options{Addr: n.addr})
			info, err := c.ClusterInfo(ctx).Result()
			view, err2 := c.ClusterNodes(ctx).Result()
			repl, err3 := c.Info(ctx, "replication").Result()
			_ = c.Close()
			switch {
			case err != nil || err2 != nil || err3 != nil:
				why = fmt.Sprintf("%s: %v %v %v", n.addr, err, err2, err3)
				return false
			case !strings.Contains(info, "cluster_state:ok"), !strings.Contains(info, "cluster_slots_assigned:16384"):
				why = n.addr + ": cluster state not ok"
				return false
			case strings.Contains(view, "fail"), strings.Count(view, "master") != 3:
				why = n.addr + ": sees a failing node or not three masters"
				return false
			case !n.master && !strings.Contains(repl, "master_link_status:up"):
				why = n.addr + ": replication link down"
				return false
			}
		}
		return true
	}
	for deadline := time.Now().Add(45 * time.Second); !stable(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("the Valkey Cluster did not settle: %s", why)
		}
	}
}

// crc16 is the CRC-16/XMODEM Valkey Cluster hashes keys with.
func crc16(s string) uint16 {
	var crc uint16
	for i := range len(s) {
		crc ^= uint16(s[i]) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// slotOf is the hash slot of key (ref keys carry no hash tag).
func slotOf(key string) int { return int(crc16(key) % 16384) }

// idInSlot returns a new ref ID whose key hashes to slot.
func idInSlot(slot int) string {
	for {
		if id := docref.NewID(); slotOf(valkeystore.Key(id)) == slot {
			return id
		}
	}
}

// Put, Get and Delete keep working on a real Valkey Cluster while the ref's
// slot migrates to another master: ASK redirects (a new key, a key already
// moved) are followed with ASKING — the create's WAITAOF still on the
// writing connection — and once the migration completes, the client's stale
// slot map gets MOVED, reloads and reaches the new owner.
func TestCluster_SlotMigration_PutGetDelete(t *testing.T) {
	addrs := clusterAddrs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	waitClusterStable(t, ctx, addrs)

	client := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(ctx).Err())
	require.NoError(t, valkeystore.CheckDurability(ctx, client), "the profile's nodes are configured as durable stores")
	s, tn := valkeystore.New(client, valkeystore.Options{TTL: time.Hour}), tenant()

	// A ref written before the migration, on the slot's current owner.
	before := meta(tn)
	_, err := s.Put(ctx, before)
	require.NoError(t, err)
	key := valkeystore.Key(before.ID)
	slot := slotOf(key)

	// Admin clients, one per master, by node ID.
	nodes := map[string]*redis.Client{}
	nodeAddr := map[string]string{}
	for _, n := range mastersOf(clusterTopology(t, ctx, addrs)) {
		nodes[n.id], nodeAddr[n.id] = n.admin, n.addr
	}
	owner, err := client.MasterForKey(ctx, key)
	require.NoError(t, err)
	var srcID, dstID string
	for id, a := range nodeAddr {
		if a == owner.Options().Addr {
			srcID = id
		} else if dstID == "" {
			dstID = id
		}
	}
	require.NotEmpty(t, srcID, "the slot owner is one of the masters")
	require.NotEmpty(t, dstID)
	src, dst := nodes[srcID], nodes[dstID]

	// Start the migration: dst imports the slot, src migrates it.
	require.NoError(t, dst.Do(ctx, "CLUSTER", "SETSLOT", slot, "IMPORTING", srcID).Err())
	require.NoError(t, src.Do(ctx, "CLUSTER", "SETSLOT", slot, "MIGRATING", dstID).Err())
	finished := false
	t.Cleanup(func() {
		if !finished { // leave the cluster usable for the next run
			for _, c := range nodes {
				_ = c.Do(context.Background(), "CLUSTER", "SETSLOT", slot, "STABLE").Err()
			}
		}
	})

	// A new key in a MIGRATING slot is refused by src with ASK: the create
	// lands on dst.
	during := meta(tn)
	during.ID = idInSlot(slot)
	_, err = s.Put(ctx, during)
	require.NoError(t, err, "put during migration (ASK to the importing node)")
	var exists *redis.IntCmd
	_, err = dst.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.Do(ctx, "ASKING")
		exists = p.Exists(ctx, valkeystore.Key(during.ID))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists.Val(), "the new ref was created on the importing node")

	// The pre-existing ref is still served by src; the new one via ASK.
	for _, id := range []string{before.ID, during.ID} {
		_, found, err := s.Get(ctx, tn, id)
		require.NoError(t, err, "get %s during migration", id)
		assert.True(t, found, "get %s during migration", id)
	}

	// Move the pre-existing ref: src now answers ASK for it too.
	_, port, err := net.SplitHostPort(nodeAddr[dstID])
	require.NoError(t, err)
	require.NoError(t, src.Do(ctx, "MIGRATE", "127.0.0.1", port, "", 0, 5000, "KEYS", key).Err())
	_, found, err := s.Get(ctx, tn, before.ID)
	require.NoError(t, err, "get a migrated ref (ASK)")
	assert.True(t, found)

	// A delete during the migration follows the ASK too.
	extra := meta(tn)
	extra.ID = idInSlot(slot)
	_, err = s.Put(ctx, extra)
	require.NoError(t, err)
	require.NoError(t, s.Delete(ctx, tn, extra.ID), "delete during migration (ASK)")
	_, found, err = s.Get(ctx, tn, extra.ID)
	require.NoError(t, err)
	assert.False(t, found, "deleted")

	// Complete the migration on every node. The store's client still maps
	// the slot to src, which now answers MOVED.
	for _, id := range []string{dstID, srcID} {
		require.NoError(t, nodes[id].Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", dstID).Err())
	}
	for id, c := range nodes {
		if id != srcID && id != dstID {
			require.NoError(t, c.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", dstID).Err())
		}
	}
	finished = true
	for _, id := range []string{before.ID, during.ID} {
		_, found, err := s.Get(ctx, tn, id)
		require.NoError(t, err, "get %s after migration (MOVED)", id)
		assert.True(t, found, "get %s after migration", id)
	}
	after := meta(tn)
	after.ID = idInSlot(slot)
	_, err = s.Put(ctx, after)
	require.NoError(t, err, "put after migration")
	require.NoError(t, s.Delete(ctx, tn, after.ID), "delete after migration")
	owner, err = client.MasterForKey(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, nodeAddr[dstID], owner.Options().Addr, "the client's slot map now names the new owner")
}

// runbookClusterACL is the runbook's worker ACL plus the rules it says a
// Valkey Cluster also needs: "On Valkey Cluster add `+cluster|slots
// +command`".
func runbookClusterACL(t *testing.T) []string {
	t.Helper()
	doc, err := os.ReadFile("../../../docs/runbooks/document-refs.md")
	require.NoError(t, err)
	require.Contains(t, string(doc), "On Valkey Cluster add `+cluster|slots +command`", "the runbook's cluster rules changed: update this test")
	return append(runbookACL(t), "+cluster|slots", "+command")
}

// createWorkerUser creates a user with the runbook's cluster ACL on each of
// nodes (ACLs are per node) and deletes it again at the end of the test.
func createWorkerUser(t *testing.T, ctx context.Context, nodes []*clusterNode) (user, password string) {
	t.Helper()
	user, password = "docref-worker-"+uuid.NewString()[:8], uuid.NewString()
	args := []any{"ACL", "SETUSER", user}
	for _, rule := range runbookClusterACL(t) {
		args = append(args, strings.ReplaceAll(rule, "<password>", password))
	}
	for _, n := range nodes {
		t.Cleanup(func() { _ = n.admin.Do(context.Background(), "ACL", "DELUSER", user).Err() })
		require.NoError(t, n.admin.Do(ctx, args...).Err(), "the documented rules are valid ACL syntax on %s", n.addr)
	}
	return user, password
}

// The runbook's ACL for a cluster is complete: a user created with exactly
// those rules on every node (ACLs are per node) passes CheckDurability on
// every master and replica, and runs Put, Get and Delete — including the
// ASK path of a slot migration, which needs +asking.
func TestCluster_DocumentedACL_Suffices(t *testing.T) {
	addrs := clusterAddrs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	waitClusterStable(t, ctx, addrs)

	all := clusterTopology(t, ctx, addrs)
	user, password := createWorkerUser(t, ctx, all)
	nodes := map[string]*redis.Client{}
	nodeAddr := map[string]string{}
	for _, n := range mastersOf(all) {
		nodes[n.id], nodeAddr[n.id] = n.admin, n.addr
	}

	worker := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs, Username: user, Password: password})
	t.Cleanup(func() { _ = worker.Close() })
	require.NoError(t, worker.Ping(ctx).Err())
	require.NoError(t, valkeystore.CheckDurability(ctx, worker), "CheckDurability on every node as the worker user")

	s, tn := valkeystore.New(worker, valkeystore.Options{TTL: time.Hour}), tenant()
	ref, err := s.Put(ctx, meta(tn))
	require.NoError(t, err)
	_, found, err := s.Get(ctx, tn, ref.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, s.Delete(ctx, tn, ref.ID))

	// A new ref in a MIGRATING slot: Put, Get and Delete follow ASK
	// (ASKING) as the worker user.
	key := valkeystore.Key(ref.ID)
	slot := slotOf(key)
	owner, err := worker.MasterForKey(ctx, key)
	require.NoError(t, err)
	var srcID, dstID string
	for id, a := range nodeAddr {
		if a == owner.Options().Addr {
			srcID = id
		} else if dstID == "" {
			dstID = id
		}
	}
	require.NotEmpty(t, srcID)
	require.NotEmpty(t, dstID)
	require.NoError(t, nodes[dstID].Do(ctx, "CLUSTER", "SETSLOT", slot, "IMPORTING", srcID).Err())
	require.NoError(t, nodes[srcID].Do(ctx, "CLUSTER", "SETSLOT", slot, "MIGRATING", dstID).Err())
	t.Cleanup(func() { // back to src: the migrated key below is deleted first
		for _, c := range nodes {
			_ = c.Do(context.Background(), "CLUSTER", "SETSLOT", slot, "STABLE").Err()
		}
	})

	during := meta(tn)
	during.ID = idInSlot(slot)
	_, err = s.Put(ctx, during)
	require.NoError(t, err, "put during migration (ASK) as the worker user")
	var onDst *redis.IntCmd
	_, err = nodes[dstID].Pipelined(ctx, func(p redis.Pipeliner) error {
		p.Do(ctx, "ASKING")
		onDst = p.Exists(ctx, valkeystore.Key(during.ID))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), onDst.Val(), "the create followed ASK to the importing node")
	_, found, err = s.Get(ctx, tn, during.ID)
	require.NoError(t, err)
	assert.True(t, found)
	require.NoError(t, s.Delete(ctx, tn, during.ID), "delete during migration (ASK)")
	_, found, err = s.Get(ctx, tn, during.ID)
	require.NoError(t, err)
	assert.False(t, found)

	// The fence holds on a cluster too.
	assert.True(t, redis.IsPermissionError(worker.Get(ctx, "not-a-ref").Err()), "keys outside docref:* are denied")
}

// CheckDurability checks every master and every replica of a real cluster:
// the compose cluster's documented settings pass on all six nodes, and a
// replica that lacks the worker's ACL user (ACLs are per node, so a replica
// added or rebuilt without it is easy to miss) fails the check outright —
// WRONGPASS is not "CONFIG refused, verify in the console"
// (ErrConfigUnavailable), so a worker tolerating that cannot start with a
// replica it could never have checked.
func TestCluster_CheckDurability_EveryMasterAndReplica(t *testing.T) {
	addrs := clusterAddrs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	waitClusterStable(t, ctx, addrs)

	client := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { _ = client.Close() })
	var masters, replicas atomic.Int32
	require.NoError(t, client.ForEachMaster(ctx, func(context.Context, *redis.Client) error { masters.Add(1); return nil }))
	require.NoError(t, client.ForEachSlave(ctx, func(context.Context, *redis.Client) error { replicas.Add(1); return nil }))
	require.Equal(t, int32(3), masters.Load())
	require.Equal(t, int32(3), replicas.Load(), "the compose cluster has one replica per master")
	require.NoError(t, valkeystore.CheckDurability(ctx, client), "appendonly / appendfsync / noeviction on all six nodes")

	all := clusterTopology(t, ctx, addrs)
	missing := replicasOf(all)[0]
	var others []*clusterNode
	for _, n := range all {
		if n != missing {
			others = append(others, n)
		}
	}
	user, password := createWorkerUser(t, ctx, others)
	worker := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs, Username: user, Password: password})
	t.Cleanup(func() { _ = worker.Close() })

	err := valkeystore.CheckDurability(ctx, worker)
	require.Error(t, err)
	assert.NotErrorIs(t, err, valkeystore.ErrConfigUnavailable, "a replica that cannot be checked is a failure")
	var derr *valkeystore.DurabilityError
	assert.NotErrorAs(t, err, &derr, "not checked, not misconfigured")
	assert.Contains(t, err.Error(), "replica "+missing.addr+": ")
	assert.Contains(t, err.Error(), "WRONGPASS")
}

// A manual failover (CLUSTER FAILOVER on a replica, as an operator or a
// managed service does for maintenance) moves a ref's slot to the promoted
// replica: the store's next Get, Put and Delete reach the new master at once
// — the demoted one answers MOVED, the slot map is reloaded synchronously —
// instead of failing until go-redis's periodic reload (60 s). A ref written
// with WaitReplicas: 1 before the failover survives it, and once the demoted
// master has resynced as a replica, WaitReplicas: 1 writes succeed again.
func TestCluster_ManualFailover_RefsRouteToNewMaster(t *testing.T) {
	addrs := clusterAddrs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	waitClusterStable(t, ctx, addrs)

	client := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { _ = client.Close() })
	tn := tenant()
	replicated := valkeystore.New(client, valkeystore.Options{TTL: time.Hour, WaitReplicas: 1})
	s := valkeystore.New(client, valkeystore.Options{TTL: time.Hour})

	before, err := replicated.Put(ctx, meta(tn))
	require.NoError(t, err, "put confirmed by the master's and one replica's AOF")
	key := valkeystore.Key(before.ID)
	owner, err := client.MasterForKey(ctx, key)
	require.NoError(t, err)

	all := clusterTopology(t, ctx, addrs)
	oldMaster := byAddr(t, all, owner.Options().Addr)
	var promoted *clusterNode
	for _, n := range replicasOf(all) {
		if n.masterID == oldMaster.id {
			promoted = n
		}
	}
	require.NotNil(t, promoted, "the owner has a replica")

	require.NoError(t, promoted.admin.Do(ctx, "CLUSTER", "FAILOVER").Err())
	t.Cleanup(func() { // fail back, so the next test starts from the original roles
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = oldMaster.admin.Do(ctx, "CLUSTER", "FAILOVER").Err()
		require.Eventually(t, func() bool { return roleOf(ctx, oldMaster.admin) == "master" }, 30*time.Second, 100*time.Millisecond)
		waitClusterStable(t, ctx, addrs)
	})
	require.Eventually(t, func() bool {
		return roleOf(ctx, promoted.admin) == "master" && roleOf(ctx, oldMaster.admin) == "slave"
	}, 30*time.Second, 50*time.Millisecond, "the manual failover completed")

	// The store's slot map still names the demoted master.
	stale, err := client.MasterForKey(ctx, key)
	require.NoError(t, err)
	require.Equal(t, oldMaster.addr, stale.Options().Addr, "the client has not noticed the failover yet")

	start := time.Now()
	got, found, err := s.Get(ctx, tn, before.ID)
	require.NoError(t, err, "get after the failover")
	require.True(t, found, "the ref written with WaitReplicas: 1 survived the failover")
	assert.Equal(t, before.ObjectKey, got.ObjectKey)
	after := meta(tn)
	after.ID = idInSlot(slotOf(key))
	_, err = s.Put(ctx, after)
	require.NoError(t, err, "put after the failover")
	require.NoError(t, s.Delete(ctx, tn, after.ID), "delete after the failover")
	assert.Less(t, time.Since(start), 5*time.Second, "routed to the new master without waiting for a periodic reload")
	now, err := client.MasterForKey(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, promoted.addr, now.Options().Addr, "the client's slot map names the promoted replica")

	// The demoted master resyncs as the new master's replica: replica-
	// confirmed writes work again.
	require.Eventually(t, func() bool {
		ref := meta(tn)
		ref.ID = idInSlot(slotOf(key))
		_, err := replicated.Put(ctx, ref)
		return err == nil
	}, 30*time.Second, 200*time.Millisecond, "WaitReplicas: 1 after the demoted master resynced")
	require.NoError(t, s.Delete(ctx, tn, before.ID))
}

// roleOf is the node's ROLE ("master" or "slave"), "" when it cannot be read.
func roleOf(ctx context.Context, c *redis.Client) string {
	role, err := c.Do(ctx, "ROLE").Slice()
	if err != nil || len(role) == 0 {
		return ""
	}
	r, _ := role[0].(string)
	return r
}

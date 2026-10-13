//go:build integration

package valkeystore_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// clusterAddrs is TEST_VALKEY_CLUSTER_ADDRS, the masters of a real Valkey
// Cluster (docker-compose.yml's valkey-cluster profile, which `make
// docker-up` and CI start with the rest of the stack). Unset, the test skips
// locally; with CI set it fails, like every other infrastructure test.
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

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
	for _, a := range addrs {
		c := redis.NewClient(&redis.Options{Addr: a})
		t.Cleanup(func() { _ = c.Close() })
		id, err := c.Do(ctx, "CLUSTER", "MYID").Text()
		require.NoError(t, err)
		nodes[id], nodeAddr[id] = c, a
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
	require.NotEmpty(t, srcID, "the slot owner is one of TEST_VALKEY_CLUSTER_ADDRS")
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

// The runbook's ACL for a cluster is complete: a user created with exactly
// those rules on every node (ACLs are per node) passes CheckDurability on
// every master and replica, and runs Put, Get and Delete — including the
// ASK path of a slot migration, which needs +asking.
func TestCluster_DocumentedACL_Suffices(t *testing.T) {
	addrs := clusterAddrs(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	user, password := "docref-worker-"+uuid.NewString()[:8], uuid.NewString()
	args := []any{"ACL", "SETUSER", user}
	for _, rule := range runbookClusterACL(t) {
		args = append(args, strings.ReplaceAll(rule, "<password>", password))
	}
	nodes := map[string]*redis.Client{}
	nodeAddr := map[string]string{}
	for _, a := range addrs {
		admin := redis.NewClient(&redis.Options{Addr: a})
		t.Cleanup(func() {
			_ = admin.Do(context.Background(), "ACL", "DELUSER", user).Err()
			_ = admin.Close()
		})
		require.NoError(t, admin.Do(ctx, args...).Err(), "the documented rules are valid ACL syntax on %s", a)
		id, err := admin.Do(ctx, "CLUSTER", "MYID").Text()
		require.NoError(t, err)
		nodes[id], nodeAddr[id] = admin, a
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

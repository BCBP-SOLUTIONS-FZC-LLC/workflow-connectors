package valkeystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/redis/go-redis/v9"
)

// ErrConfigUnavailable means the server refused CONFIG GET (common on
// managed services that rename or disable CONFIG). Durability must then be
// verified in the provider's console; WAITAOF on each write still applies.
var ErrConfigUnavailable = errors.New("docref: Valkey configuration cannot be read")

// DurabilityError lists every setting that makes Valkey unfit to be the
// authoritative document-ref store.
type DurabilityError struct{ Problems []string }

func (e *DurabilityError) Error() string {
	return "docref: Valkey is not configured as a durable store: " + strings.Join(e.Problems, "; ")
}

// CheckDurability verifies the server is configured as the document-ref
// system of record: on every master and every replica of a cluster, or the
// one server. The worker calls it at startup and refuses to run on a
// *DurabilityError.
//
// Each master (or the one server) is first probed with a real WAITAOF — the
// command every Put depends on — which fails when the append-only file is
// off even where CONFIG is blocked. CONFIG GET then checks the settings; a
// server that refuses it returns ErrConfigUnavailable (verify the settings in
// the provider's console) only after the probe has passed.
//
// A cluster's replicas get the same CONFIG GET checks (without the probe): a
// replica is promoted on failover and must then be a durable store too. A
// standalone or Sentinel-managed client cannot enumerate the replicas, so
// their settings must be checked by the operator (see the runbook).
func CheckDurability(ctx context.Context, client redis.UniversalClient) error {
	switch c := client.(type) {
	case *redis.Client:
		return checkNode(ctx, c)
	case *redis.ClusterClient:
		var mu sync.Mutex
		var errs []error
		check := func(role string, fn func(context.Context, *redis.Client) error) func(context.Context, *redis.Client) error {
			return func(ctx context.Context, node *redis.Client) error {
				if err := fn(ctx, node); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("%s %s: %w", role, node.Options().Addr, err))
					mu.Unlock()
				}
				return nil
			}
		}
		errMasters := c.ForEachMaster(ctx, check("master", checkNode))
		errReplicas := c.ForEachSlave(ctx, check("replica", checkReplica))
		return errors.Join(append(errs, errMasters, errReplicas)...)
	default:
		return fmt.Errorf("docref: unsupported Valkey client %T: use *redis.Client (standalone or Sentinel) or *redis.ClusterClient", client)
	}
}

func checkNode(ctx context.Context, node *redis.Client) error {
	var probe []string
	acks, err := node.Do(ctx, "WAITAOF", 1, 0, 1000).Int64Slice()
	switch {
	case err != nil:
		probe = append(probe, fmt.Sprintf("WAITAOF refused (%v): every Put would fail", err))
	case len(acks) == 0 || acks[0] < 1:
		probe = append(probe, "WAITAOF did not confirm the local append-only file")
	}

	settings, err := readSettings(ctx, node)
	if err != nil {
		if len(probe) > 0 {
			return &DurabilityError{Problems: probe}
		}
		return err
	}
	err = evaluate(settings)
	if len(probe) == 0 {
		return err
	}
	var derr *DurabilityError
	if errors.As(err, &derr) {
		probe = append(probe, derr.Problems...)
	}
	return &DurabilityError{Problems: probe}
}

// checkReplica checks a replica's settings. WAITAOF is not probed: a replica
// has no writes of its own to confirm.
func checkReplica(ctx context.Context, node *redis.Client) error {
	settings, err := readSettings(ctx, node)
	if err != nil {
		return err
	}
	return evaluate(settings)
}

// readSettings reads the settings evaluate judges; a refused CONFIG GET is
// ErrConfigUnavailable.
func readSettings(ctx context.Context, node *redis.Client) (map[string]string, error) {
	settings := map[string]string{}
	for _, name := range []string{"appendonly", "appendfsync", "maxmemory-policy"} {
		values, err := node.ConfigGet(ctx, name).Result()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrConfigUnavailable, err)
		}
		settings[name] = values[name]
	}
	return settings, nil
}

// evaluate is CheckDurability's rule set, separated for testing.
func evaluate(s map[string]string) error {
	var problems []string
	if s["appendonly"] != "yes" {
		problems = append(problems, fmt.Sprintf("appendonly is %q, want \"yes\" (AOF persistence)", s["appendonly"]))
	}
	switch s["appendfsync"] {
	case "always", "everysec":
	default:
		problems = append(problems, fmt.Sprintf("appendfsync is %q, want \"always\" or \"everysec\"", s["appendfsync"]))
	}
	if s["maxmemory-policy"] != "noeviction" {
		problems = append(problems, fmt.Sprintf("maxmemory-policy is %q, want \"noeviction\" (ref keys carry a TTL, so volatile-* and allkeys-* policies can evict them early)", s["maxmemory-policy"]))
	}
	if len(problems) > 0 {
		return &DurabilityError{Problems: problems}
	}
	return nil
}

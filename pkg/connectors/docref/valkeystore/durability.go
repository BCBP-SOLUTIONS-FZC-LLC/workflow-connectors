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
// *DurabilityError (check errors.As first). The error matches
// ErrConfigUnavailable only when every failure is a refused CONFIG GET on a
// node whose WAITAOF probe passed: a *DurabilityError, or any other failure,
// on another node hides it.
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
		return joinNodeErrors(append(errs, errMasters, errReplicas))
	default:
		return fmt.Errorf("docref: unsupported Valkey client %T: use *redis.Client (standalone or Sentinel) or *redis.ClusterClient", client)
	}
}

// nodeErrors is a cluster's failures, one per node. It matches
// ErrConfigUnavailable only when every failure is ErrConfigUnavailable: a
// node that is not durable (a *DurabilityError) or could not be checked at
// all must not be mistaken for "verify in the console" by a worker that
// tolerates ErrConfigUnavailable.
type nodeErrors struct {
	errs    []error // every failure, for the message
	matches []error // what errors.Is / errors.As see
}

func (e *nodeErrors) Error() string   { return errors.Join(e.errs...).Error() }
func (e *nodeErrors) Unwrap() []error { return e.matches }

func joinNodeErrors(errs []error) error {
	var all, others []error
	for _, err := range errs {
		if err == nil {
			continue
		}
		all = append(all, err)
		if !errors.Is(err, ErrConfigUnavailable) {
			others = append(others, err)
		}
	}
	if len(others) == 0 {
		return errors.Join(all...) // nil, or only ErrConfigUnavailable
	}
	return &nodeErrors{errs: all, matches: others}
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

// readSettings reads the settings evaluate judges. Only a server reply
// refusing CONFIG GET itself — NOPERM (the user lacks +config|get), or an
// unknown command or subcommand (CONFIG disabled or renamed) — is
// ErrConfigUnavailable. Any other failure is returned as is: another server
// reply (WRONGPASS: the node lacks the worker's ACL user; LOADING, BUSY, a
// server at maxclients), the node unreachable, a timeout, the pool
// exhausted, the context ended. So a node that could not be checked is never
// mistaken for one whose settings must merely be checked in the console.
func readSettings(ctx context.Context, node *redis.Client) (map[string]string, error) {
	settings := map[string]string{}
	for _, name := range []string{"appendonly", "appendfsync", "maxmemory-policy"} {
		values, err := node.ConfigGet(ctx, name).Result()
		if configRefused(err) {
			return nil, fmt.Errorf("%w: %w", ErrConfigUnavailable, err)
		}
		if err != nil {
			return nil, fmt.Errorf("CONFIG GET %s: %w", name, err)
		}
		settings[name] = values[name]
	}
	return settings, nil
}

// configRefused reports a server reply refusing CONFIG GET as such: NOPERM,
// or "ERR unknown command" / "ERR unknown subcommand" (HasErrorPrefix strips
// the leading "ERR ").
func configRefused(err error) bool {
	return redis.IsPermissionError(err) ||
		redis.HasErrorPrefix(err, "unknown command") || redis.HasErrorPrefix(err, "unknown subcommand")
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

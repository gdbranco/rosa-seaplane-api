# Namespace and Cluster-Name Sharding

The seaplane operator partitions cache List/Watch streams across replicas. Rows
with a `seaplane.io/cluster` label are assigned using the account namespace and
that Cluster name; rows without a non-empty label retain per-namespace sharding.

```text
key = namespace + "/" + clusterName   when the label is non-empty
key = namespace                       otherwise
shard = abs(hashtext(key)::bigint) % replicaCount
```

The namespace scopes the name and ensures resources from different accounts do
not share an assignment key. New Cluster rows, NodePools, and Placements carry
the same name label, so they are colocated. The distinct
`seaplane.io/cluster-uid` label and ownerReference continue to carry machine
ownership and cleanup identity. Account-level resources and legacy rows without
the name label fall back to their namespace. A child created for a legacy
Cluster without the name label also omits it, keeping that child on the same
namespace shard. Reads through the direct client remain unsharded.

## How It Works

### Shard Assignment

The same parameterized PostgreSQL predicate is used for informer List and Watch
queries:

```sql
abs(hashtext(COALESCE(
    namespace || '/' || NULLIF(metadata->'labels'->>'seaplane.io/cluster', ''),
    namespace
))::bigint) % $mod = ANY($owned)
```

An absent or empty label falls back to the namespace. The operator configures
the label key via `pgruntime.ShardConfig`:

```go
pgruntime.ShardConfig{
    Mod:      replicaCount,
    Owned:    []int{ordinal},
    KeyLabel: v1alpha1.ClusterNameLabel,
    UnshardedGVKs: []schema.GroupVersionKind{
        v1alpha1.SchemeGroupVersion.WithKind("ManagementCluster"),
    },
}
```

- **Mod**: the modulus (total number of shards, equal to `replicaCount`)
- **Owned**: the shard indices this replica owns (typically just `[ordinal]`)
- **KeyLabel**: the optional label combined with the namespace; absent/empty
  values fall back to namespace-only sharding
- **UnshardedGVKs**: GVKs exempt from sharding (every replica sees them)

### Operator Replica Assignment

The operator runs as a **StatefulSet**. Each pod parses its ordinal from the
hostname (e.g., `seaplane-operator-2` -> 2) and owns that shard from
`REPLICA_COUNT`:

```
my_shard = ordinal
```

For four replicas, each labeled Cluster name and namespace fallback key hashes
to one of shards `0..3`. Different Clusters in the same account can be handled
by different replicas.

### ManagementCluster Visibility

ManagementCluster is cluster-scoped and declared in `UnshardedGVKs`. Every pod
sees all ManagementClusters through its standard manager client, so the
PlacementReconciler has the full registry.

### Cross-Cluster Decisions

Each operator pod's cache contains only its owned shard keys. Do not make
fleet-wide decisions—such as choosing the least-loaded ManagementCluster—from
counts in that shard-local cache. For scarce shared capacity, use an `Index`
claim or an optimistic update on a shared record. A direct read can see all rows,
but a list-then-act count is still not an atomic claim.

### Platform API and Reads

The platform API uses `pgruntime.NewClient()`, which is never sharded. The
operator's direct client reads likewise see all rows; only cache informer
List/Watch streams are partitioned.

## Configuration

| Variable        | Default | Description                                 |
| --------------- | ------- | ------------------------------------------- |
| `REPLICA_COUNT` | `1`     | Number of operator replicas (= shard count) |
| `POSTGRES_DSN`  | --      | PostgreSQL connection string (required)     |

```yaml
# Helm values (operator)
replicaCount: 4
```

The chart sets `REPLICA_COUNT` to match `replicaCount`.

## Scaling

To scale from 2 to 4 operator replicas:

1. Update `replicaCount: 4` in the operator Helm values.
2. The chart sets `REPLICA_COUNT=4` on every pod.
3. Each pod re-derives its shard on startup (rolling restart).

No per-pod configuration or manual shard assignment is needed.

### Constraints

- Changing `replicaCount` requires a rolling restart of the StatefulSet so each
  pod picks up the new modulus. During the rollout, some shard keys may
  temporarily be handled by two pods or none. Reconciler writes to PostgreSQL
  are idempotent, but external side effects such as DynamoDB desire writes may
  receive duplicate requests during the rollout; deterministic document IDs
  make those writes idempotent (see [DynamoDB strategy](dynamodb-strategy.md)).
- A shard belongs to one pod. While that pod is down or restarting, its rows
  are not reconciled; another pod does not automatically take over.
- The direct client and API remain unsharded and always see all rows.

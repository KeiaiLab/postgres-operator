# pg-router — deployable query router (RFC 0004 / ROADMAP G3)

The stateless PostgreSQL query router. Applications connect to its Service
instead of to a single shard; the router resolves the shard from the routing key
(vindex) and forwards the connection to that shard's backend.

This is the **first deployable slice** of the router (ROUTER-GAP-ANALYSIS step C):
single-shard routing with topology sourced from `ShardRange` CRDs.

## Build the image

Separate from the operator manager (so the router scales independently):

```bash
docker build -f Dockerfile.router -t ghcr.io/keiailab/pg-router:<tag> .
docker push ghcr.io/keiailab/pg-router:<tag>
```

## Deploy

Apply into the namespace of the `PostgresCluster` it fronts:

```bash
kustomize build config/router | kubectl apply -n <cluster-ns> -f -
```

Adjust env in `deployment.yaml`:

| env | meaning |
|---|---|
| `PGROUTER_TOPOLOGY` | `crd` (read ShardRange CRDs) or `static` (env table) |
| `PGROUTER_CLUSTER` / `PGROUTER_KEYSPACE` | which cluster + keyspace this router fronts |
| `PGROUTER_REFRESH` | ShardRange re-read interval (hot-reload) |
| `PGROUTER_BACKEND_TEMPLATE` | shard → backend DNS, with `{cluster}`/`{shard}`/`{namespace}` |

RBAC is least-privilege: `get/list/watch` on `shardranges` in the router's own
namespace only.

## Known limitations (first slice)

- Routes by the connection startup `database`/`user` parameter, not yet by parsed
  SQL (query-level routing is step E — message-aware proxy + `RouteKeyExtractor`).
- Backend template targets each shard's ordinal-0 pod; routing to the *current
  primary* per shard is follow-up work (needs a per-shard primary Service).
- Single-shard fast-path only; multi-shard scatter-gather is later (G5).

See [`docs/sharding/ROUTER-GAP-ANALYSIS.ko.md`](../../docs/sharding/ROUTER-GAP-ANALYSIS.ko.md).

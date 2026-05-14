- External PostgreSQL → PostgresCluster CR (`externalClusters.bootstrap.pg_basebackup`)
- StatefulSet → PostgresCluster CR
- 다른 operator (PGO / CNPG) → keiailab postgres-operator
- Sharding 도입 (G3+): non-sharded → ShardingMode=native

## External → PostgresCluster

```bash
kubectl apply -f - <<YAML
apiVersion: postgres.keiailab.com/v1alpha1
kind: PostgresCluster
metadata:
  name: from-external
spec:
  bootstrap:
    pg_basebackup:
      source: external-primary
  externalClusters:
  - name: external-primary
    connectionParameters:
      host: <external-host>
      port: "5432"
      user: replication
    password:
      secretRef:
        name: external-repl-password
        key: password
YAML
kubectl wait --for=condition=Ready postgrescluster/from-external --timeout=1800s
```

## Sharding 도입 (G3+, planned)

별 runbook (`sharding.md`) — G3 ShardRange CRD GA 후 작성.

## References

- ROADMAP.md G2 (externalClusters [~]) + G3 (sharding foundation)
- D.11.7 6-runbook 의 migration component

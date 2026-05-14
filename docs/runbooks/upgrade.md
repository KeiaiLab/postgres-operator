- N → N+1 minor (e.g. PG 18.1 → 18.2)
- N → N+2 major (e.g. PG 18 → 20)
- Patch / security release

## Pre-upgrade checks

- [ ] `kubectl get postgrescluster <name>` Ready=True
- [ ] `kubectl get backupjob` 최신 full ≤ 24h ago
- [ ] `kubectl get pdb` PDB allows rolling
- [ ] Maintenance window 공지 (Slack / status page)

## Upgrade steps

1. ImageCatalog 신버전 추가
   ```bash
   kubectl edit imagecatalog <catalog> # spec.images.<major> append
   ```
2. PostgresCluster spec 갱신
   ```bash
   kubectl patch postgrescluster <name> --type=merge \
       -p '{"spec":{"imageCatalogRef":{"major":"N+1"}}}'
   ```
3. Operator rolling upgrade — replica 부터 새 binary, primary 최후
4. Verify
   ```bash
   kubectl exec <primary> -- psql -c 'SELECT version();'
   ```

## Rollback

- ImageCatalog 의 이전 major 로 revert + StatefulSet rolling restart

## References

- ROADMAP.md G2 (Upgrade smoke + ImageCatalog)
- D.11.4 Upgrade matrix N→N+1/N→N+2/patches

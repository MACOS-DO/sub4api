# Gateway integration fixture

`mock_issuer.py` is a synthetic upstream for an isolated Docker network. It is not
part of deployment and does not forward requests to any real service. CONNECT
is restricted to the three official hostnames and terminates locally using a
fixture CA. Only synthetic credentials may be assigned to this proxy.

Mount `/fixtures/server.pem`, `/fixtures/server.key` and a writable `/captures`.
The Gateway container alone trusts the fixture CA through `CODEX_CA_CERTIFICATE`.
Capture output records protocol/identity/size evidence and deliberately excludes
Authorization. Do not put real cookies or credentials into fixture requests.

The reproducible repository tests are:

```sh
cd backend
go test -tags=unit ./internal/repository -run TestCodexGatewaySchedulerRedisRoundTrip
go test ./internal/pkg/codexgateway ./internal/service -run 'Test(Gateway|CodexGateway)'
CI=1 SUB2API_TEST_POSTGRES_IMAGE=postgres:18-alpine \
  go test -tags=integration ./internal/repository -run TestCodexGateway
```

The PG harness starts separate PostgreSQL/Redis containers and tests binding
atomicity, cross-instance locks and full-dump restore using the actual psql
client in the PostgreSQL container. The restore test retains both locks for the
whole transaction and verifies rollback on errors.

For one-off joint validation, the local test environment used ports 18721/18722
for two Sub4API instances, 39334 for Gateway, 39698 for PostgreSQL and 39336 for
Redis, all bound to localhost. These are test-only published ports; the deployment
overlay publishes no Gateway port. See the release validation record for results
and for official checks that are still unverified.


Automatic service-key deployment also has an isolated Compose regression:

```sh
python3 deploy/tests/docker-compose-codex-key-test.py          # merged configuration
python3 deploy/tests/docker-compose-codex-key-test.py --runtime
```

Run from the repository root with both local images already built. This test
starts two Sub4API containers using separate application data volumes and one
shared key volume, verifies first boot/recreate/authentication/fault isolation,
and removes only its uniquely named temporary project. The synthetic test
network has no Internet route and publishes no host ports. It does not use the
previous official credential database or make official requests. The test
Compose override uses `!reset`/`!override`, requiring Compose support for those tags (verified with Compose 5.1.3).

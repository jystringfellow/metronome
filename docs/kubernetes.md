# Running Metronome on Kubernetes

The chart at `deploy/helm/metronome` runs one `metronome serve` process with its configured intake,
publish and GitHub polling loops. It uses a Recreate deployment strategy: the old writer must stop
before a new one starts. PostgreSQL is the recommended hosted store and the chart default; SQLite
on a persistent volume is also supported. Neither the database server nor an identity provider is
installed by the chart.

Use a published image version or digest. The examples use `1.4.2` as an illustrative version;
replace it with a published version. By default the Service is private (ClusterIP), ingress is disabled,
and no service account token is mounted. The board and API have no built-in authentication.

## PostgreSQL (default)

Provision a PostgreSQL database and an existing Secret in the release namespace. Prefer your usual
secret store/controller. For a manual installation, put the PostgreSQL URL in a protected local file
`db-url` (without a trailing newline) and the GitHub token in `github-token`, and create the Secrets:

```sh
kubectl create secret generic metronome-db --from-file=url=./db-url
kubectl create secret generic metronome-github --from-file=token=./github-token
```

Keep credentials out of shell history, Git and Helm values; remove the local files securely when no
longer needed. Restrict Secret access with Kubernetes RBAC and use encryption at rest.

Save the following as `values.yaml`:

```yaml
image:
  tag: "1.4.2"
database:
  postgres:
    existingSecret: metronome-db
config:
  version: 1
  workstreams:
    - name: example
      intake:
        every: 10m
        command: [metronome, github, issues, --repo, acme/app, --owner, pat]
secretEnv:
  - name: GH_TOKEN
    secretName: metronome-github
    key: token
```

```sh
helm install metronome deploy/helm/metronome -f values.yaml
```

The URL is read by `--db-env METRONOME_DB`, from the existing Secret's `url` key by default.
No database PVC is created. Use a different database or schema for each release; an advisory lock
enforces a single writer. Use the provider's backup and restore facilities. To migrate an existing
SQLite event log, stop its writer and follow the `migrate-store` procedure in [operations](operations.md).

## SQLite

Alternatively, save these values as `values.yaml`:

```yaml
image:
  tag: "1.4.2"
database:
  type: sqlite
  sqlite:
    persistence:
      size: 1Gi
config:
  version: 1
```

```sh
helm install metronome deploy/helm/metronome -f values.yaml
```

The database lives at `/data/metronome.db`. Use block-backed ReadWriteOnce storage with local filesystem
semantics (not NFS or SMB). Set `database.sqlite.persistence.storageClassName` to choose a storage class,
or leave it empty to use the cluster default. A chart-created PVC is retained on uninstall; to reuse
it, set `database.sqlite.persistence.existingClaim` to its name (normally `metronome-data`). Do not
delete the PVC unless you intend to delete the event log.

Create an online backup and copy it off the volume:

```sh
backup="metronome-$(date -u +%Y%m%dT%H%M%SZ).db"
kubectl exec deploy/metronome -c metronome -- mkdir -p /data/backups
kubectl exec deploy/metronome -c metronome -- metronome backup --db /data/metronome.db --to "/data/backups/$backup"
kubectl cp -c metronome "metronome-data-placeholder:/data/backups/$backup" "./$backup"
```

Replace `metronome-data-placeholder` with the running pod name (find it using
`kubectl get pods -l app.kubernetes.io/instance=metronome`). Keep the backup on a filesystem that
supports hard links until the backup command completes. To restore, scale the Deployment to zero,
wait for the pod to terminate, mount the PVC in a maintenance pod, and follow the SQLite restore
procedure in [operations](operations.md). Remove the maintenance pod before starting the writer again.

## Check the instance

```sh
kubectl rollout status deployment/metronome
kubectl port-forward svc/metronome 7340:7340
```

From another terminal, request `http://localhost:7340/readyz` and open `http://localhost:7340/` for
the board. Startup and liveness probes use `/healthz`; readiness uses `/readyz` to wait for replay
and check store health. Probes directly address Metronome, not an authenticating proxy.

## Configuration and values

Place the [instance configuration](configuration.md) under `config` as a YAML mapping (JSON is also
valid YAML). The chart writes it as JSON to a read-only ConfigMap mount and restarts the pod on a
configuration change. The service validates the JSON. Supply credentials through existing Secrets;
never put them in the configuration or literal environment values. Restart the pod after changing
environment-based Secrets.

| Values | Purpose |
| --- | --- |
| `image.repository`, `image.tag`, `image.digest`, `image.pullPolicy`, `imagePullSecrets` | Image reference and pull settings; digest wins over tag. A tag or digest is required. |
| `database.type`, `database.postgres.existingSecret`, `database.postgres.key` | Store choice and existing PostgreSQL Secret reference. |
| `database.sqlite.persistence` | PVC size, storage class, access modes, annotations or existing claim. |
| `actor`, `config` | Default actor (`METRONOME_ACTOR`) and versioned instance configuration. |
| `secretEnv`, `envFromSecrets`, `extraEnv` | Individual secret key references, whole existing Secrets, and non-secret environment entries. |
| `resources`, `probes` | Container resources and startup, liveness, readiness settings. |
| `service`, `ingress`, `authProxy` | Service port/type, ingress hosts/TLS/class, and optional proxy sidecar. |
| `serviceAccount`, `podSecurityContext`, `securityContext` | Service account and security settings. |
| `nameOverride`, `fullnameOverride`, `podLabels`, `podAnnotations` | Naming and metadata. |
| `nodeSelector`, `tolerations`, `affinity`, `terminationGracePeriodSeconds` | Scheduling and termination settings. |
| `extraVolumes`, `extraVolumeMounts` | Additional mounts for operator-supplied tools. |

The pod runs as UID/GID 65532 with a read-only root filesystem, writable temporary directories at
`/tmp` and `/home/metronome`, and, with SQLite, the writable `/data` mount. PostgreSQL has no `/data`
volume. If custom tools need more writable storage, supply additional mounts. Use a derived image
as described in [container](container.md), set `image.repository` to its registry (for example
`registry.example.com/acme/metronome`), and pin its version or digest. The chart passes arguments to
the existing image entrypoint.

## Authenticating proxy and ingress

The optional sidecar is proxy-neutral. Supply a pinned proxy image that accepts the configured arguments
and can run with the pod's restricted security settings. It must listen on all pod interfaces and send
authenticated requests to `http://127.0.0.1:7340`. The Service forwards only to the proxy port when enabled.
For example, merge the following into your store and image values (adjust the illustrative proxy version,
identity provider settings and allowed users to your deployment):

```yaml
authProxy:
  enabled: true
  image:
    repository: quay.io/oauth2-proxy/oauth2-proxy
    tag: "v7.8.1"
  args:
    - --http-address=0.0.0.0:4180
    - --upstream=http://127.0.0.1:7340
    - --provider=oidc
    - --oidc-issuer-url=https://identity.example.com
    - --email-domain=example.com
    - --redirect-url=https://metronome.example.com/oauth2/callback
    - --reverse-proxy=true
  secretEnv:
    - name: OAUTH2_PROXY_CLIENT_ID
      secretName: metronome-proxy
      key: client-id
    - name: OAUTH2_PROXY_CLIENT_SECRET
      secretName: metronome-proxy
      key: client-secret
    - name: OAUTH2_PROXY_COOKIE_SECRET
      secretName: metronome-proxy
      key: cookie-secret
ingress:
  enabled: true
  hosts:
    - host: metronome.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - hosts: [metronome.example.com]
      secretName: metronome-tls
```

Provision the proxy credentials and TLS Secret separately. All board and API requests through the
Service must authenticate; the proxy does not provide per-action authorization. If an external
authenticating ingress controller protects the whole application instead, explicitly set
`ingress.allowWithoutProxy: true`. The chart otherwise refuses ingress without a proxy. Do not use a
public LoadBalancer or NodePort Service without authentication. Apply network controls to restrict
direct in-cluster access to the pod's unauthenticated port 7340; a proxy is not a network policy.
For private diagnostics with the proxy enabled, forward the Deployment's port 7340 locally rather
than the Service port. Only trusted operators should have permission to do this.

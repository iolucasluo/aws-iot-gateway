# Gateway Device Bootstrap Contract v1

This is the canonical public release contract for Anyka, Taixin, and future
device firmware that must contact `aws-iot-gateway` before it has trusted wall
clock time. It is separate from:

- Gateway server `.env`, which includes private runtime configuration;
- QR/AP/BLE commissioning payloads, which remain owned by the device transport
  contract; and
- Demo App HMAC configuration, which is App-specific and sensitive.

## Directory

```text
device-bootstrap-contract/
  contract.json
  gateway-ca.pem                         # required only for HTTPS Gateway
  time-bootstrap/<kid>.der               # one or two P-256 DER SPKIs
```

`contract.json` has `schema_version = 1`, `kind = "gateway-device-bootstrap"`,
an environment, a Gateway origin, the exact HTTP time-bootstrap URL, a keyring,
file SHA-256 values, and non-secret signer provenance.

## Canonical example

This is a staging-shaped example. The actual filename values are relative to
the Contract root; consumers must never resolve them outside that directory.

```json
{
  "schema_version": 1,
  "kind": "gateway-device-bootstrap",
  "environment": "staging",
  "gateway": {
    "base_url": "https://<api-id>.execute-api.<region>.amazonaws.com",
    "ca_pem_file": "gateway-ca.pem",
    "ca_pem_sha256": "<lowercase sha256>"
  },
  "time_bootstrap": {
    "url": "http://<public-time-alb>/api/v1/device-time/bootstrap",
    "keys": [{
      "kid": "time-staging-g1",
      "spki_der_file": "time-bootstrap/time-staging-g1.der",
      "sha256": "<lowercase sha256>"
    }]
  },
  "provenance": {
    "signer_backend": "aws-kms",
    "source_stack": "aws-iot-gateway-staging",
    "kms_key_arn": "arn:aws:kms:..."
  }
}
```

`gateway.ca_pem_file` and its digest are required exactly when `gateway.base_url`
uses HTTPS. They must be absent for a development HTTP origin. Every key is an
uncompressed P-256 SubjectPublicKeyInfo DER file; key count is one normally and
at most two during a planned rotation.

## Validation and ownership

| Rule | Gateway exporter | Consumer |
| --- | --- | --- |
| URL shape | emits only an HTTP(S) origin and the exact time path | rejects path/query/fragment/credentials or an unexpected time path |
| Environment | local signer → `development`; KMS exporter → `staging`/`production` | rejects cross-environment signer combinations and release HTTP |
| Schema + integrity | writes only canonical public fields and SHA-256 metadata atomically | rejects duplicate/unknown fields and validates all SHA-256 values before use |
| Filesystem isolation | creates a new output directory; never writes a private key | rejects absolute paths, `..`, symlink escape and non-regular artifacts |
| COSE trust | signs with the active `kid` | selects exactly matching keyring `kid`, then verifies ES256 + nonce + chip UID |

The exporter is the only component allowed to derive SPKI from its local signer
or fetch a KMS public key. Consumers never need AWS credentials and never read
the signer private key. Deployment workflow and copy locations are in
[deploy/device-bootstrap/README.md](../../deploy/device-bootstrap/README.md).

## Development and staging publication

Until production release governance is introduced, the shared development and
staging Contracts, once their stable environment inputs exist, are versioned
environment inputs in this repository:

```text
deploy/local/device-bootstrap-contract/
deploy/staging/device-bootstrap-contract/
```

The exporter scripts live under `deploy/device-bootstrap/scripts/` and require
an explicit, new absolute `--out` candidate directory. An authorized Gateway
owner validates the candidate and then manually replaces the matching tracked
environment directory in a reviewed Git change. This makes the public endpoint
and trust material reproducible across Taixin and Anyka builds while ensuring a
generator never silently overwrites an approved Contract. Do not put a personal
DHCP address, private signer material, or a local-only development experiment
in the shared `local` directory.

## Environment isolation

| Contract environment | Gateway origin | signer provenance | device-build rule |
| --- | --- | --- | --- |
| `development` | Explicit HTTP or HTTPS local origin | `local` | HTTP only with an explicit development build gate |
| `staging` | HTTPS | `aws-kms` | release-like, CA mandatory |
| `production` | HTTPS | `aws-kms` or an approved future managed signer | CA mandatory |

No release build may consume a `development` Contract. A local signer Contract
cannot claim staging or production. The device time endpoint always remains
the exact HTTP endpoint `/api/v1/device-time/bootstrap`; authenticity comes
from the COSE ES256 response and its keyring, not from TLS.

The fixed HTTP time endpoint is deliberately narrow: it only returns a signed,
short-lived time statement. It must never be generalized into an unauthenticated
Gateway API path. The normal Gateway origin remains HTTPS for staging and
production, with a pinned CA supplied by this Contract.

## Key lifecycle

The keyring has one active key normally and two keys only during a planned
rotation. The Gateway signs with one `kid`; device firmware selects the matching
SPKI from the COSE protected header. Rotation order is: distribute a Contract
containing current plus next key, release firmware, switch the Gateway signer,
then retire the old key only after supported device releases no longer need it.

## Consumer boundary

- Anyka writes the Gateway URL and time URL to immutable release configuration,
  installs `gateway-ca.pem` as `/etc/backend_tls_roots.pem`, and installs every
  keyring entry as `config/time-bootstrap/<kid>.der`.
- Taixin renders the Contract into an ignored generated C header and embeds the
  CA and keyring in the firmware image. Dell receives the Contract but no AWS
  authority.
- `aws-iot-demo-app` does not consume this Contract. It keeps its own Gateway
  origin plus HMAC configuration boundary.

The contract contains only public endpoints and public trust material. It does
not authorize a build host to use AWS or to sign time responses.

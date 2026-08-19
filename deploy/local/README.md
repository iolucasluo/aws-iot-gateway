# Shared local environment

`deploy/device-bootstrap/contracts/development/` is intentionally absent until a shared local Gateway
owner provides both of the following stable inputs:

1. a team-device-reachable hostname or reserved address for the running Gateway;
2. the owner-only P-256 signer configured in that Gateway's ignored local `.env`.

Do not turn a developer laptop's DHCP address into the shared Contract. Generate
the candidate with `deploy/device-bootstrap/scripts/render-local-contract.sh --out
<absolute-new-directory>`, review it, then manually add
`deploy/device-bootstrap/contracts/development/` in the same reviewed Git change. The resulting
directory is public and contains only the endpoint and public SPKI; the signer
PEM remains in the owner's ignored local configuration.

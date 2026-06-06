# Vault Encryption Keys — Per-Tenant Derivation & Rotation

> GDPR Art. 32 / Art. 25 (Tier-1 #2). How Eurobase encrypts tenant secrets at
> rest, why each tenant has a distinct key, and how keys rotate.

## What is encrypted

Every secret in a tenant's `vault_secrets` table (`internal/vault`) is sealed
with **AES-256-GCM** before it touches disk: API credentials, OAuth
`client_secret`s, and any value a customer stores via the Vault SDK/console.
Rows store the ciphertext (`secret`), the GCM `nonce`, and now a `key_version`.

## Key model

A single base64 32-byte master secret, `VAULT_ENCRYPTION_KEY`, is supplied via
the `eurobase-secrets` Kubernetes Secret. The master is **never used directly**
to seal new rows. Instead a `KeyProvider` (`internal/vault/keyprovider.go`)
derives a **distinct key per tenant**:

```
tenant_key = HKDF-SHA256(
    secret = VAULT_ENCRYPTION_KEY,
    salt   = <tenant schema name>,   // per-tenant separation
    info   = "eurobase-vault-v<N>",  // N = key version (rotation)
    len    = 32,
)
```

- **Per-tenant separation** — the salt is the tenant schema, so tenant A's key
  cannot open tenant B's ciphertext (proved in `keyprovider_test.go`).
- **Rotation** — the version is mixed into `info`, so bumping the version
  yields a fresh, independent key without affecting older rows.

### Versions

| `key_version` | Meaning |
|---|---|
| `0` | **Legacy.** Row was sealed with the shared master key verbatim, before per-tenant derivation existed. The provider returns the master key for v0 so these rows keep decrypting. Existing rows are backfilled to 0 by migration `000057`. |
| `1` | First HKDF-derived, per-tenant key. All **new** writes use the current version (starts at 1). |
| `≥2` | Reserved for future rotations. |

Decryption always dispatches on the row's stored `key_version`, so multiple
versions coexist safely. A `Set`/`Update` re-seals at the current version, so
rows migrate to per-tenant keys naturally as they're written.

## Rotation

`POST /platform/projects/{id}/vault/rekey` (admin-only) re-encrypts every
secret in the project's vault under the current key version, including legacy
v0 rows. It runs synchronously in one service-role transaction
(`VaultService.RekeySchema`) — vaults hold few secrets per tenant, so this is
cheap; audited as `vault.rekeyed`.

To rotate the whole platform onto a new version: bump the provider's current
version, deploy, then call rekey per project (or let writes migrate rows
lazily).

## Sovereignty & limits — honest scope

This is **app-layer per-tenant key derivation**, not "the platform cannot
decrypt your data" BYOK. The platform still holds the master secret and can
derive any tenant key. It delivers what GDPR/DSK call for in *per-tenant
cryptographic separation* and *key rotation*, but a German DSK reviewer asking
for **client-held keys** wants more.

The `KeyProvider` interface is the upgrade path. Because every ciphertext is
tagged with its `key_version`, a future provider can own a distinct version
range and coexist with HKDF rows — **no re-encryption of historic data
required**:

- **Scaleway Key Manager (KMS)** — envelope encryption with a managed,
  EU-sovereign master key (rotation + access audit handled by Scaleway).
- **Customer HYOK** — per-tenant key supplied/hosted by the customer (e.g.
  self-hosted HashiCorp Vault in the EU) so the platform genuinely cannot
  decrypt for that tenant.

Both are tracked for enterprise; neither changes the on-disk format.

## Operations

- **Master rotation** is a bigger operation than version rotation: re-wrapping
  every tenant key. Today, changing `VAULT_ENCRYPTION_KEY` breaks v0 rows and
  changes all derived keys — so do it only alongside a full rekey to a new
  version. Generate with `openssl rand -base64 32`.
- **Backups:** ciphertext is useless without the master secret; keep the
  `eurobase-secrets` Secret backed up separately from DB dumps.

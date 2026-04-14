# Eurobase Project: 

## API
- Base URL: https://newtek2-njyf.eurobase.app
- Project ID: 16a1a87c-6c7e-43b7-a9d5-03e6a46e9555

## Database
- Connection: see .env for DATABASE_URL
- All tenant-scoped queries must use RLS with set_tenant_id()
- System tables (users, refresh_tokens, storage_objects, email_tokens, vault_secrets) are managed by the platform

## Auth
- Custom auth built in Go (email/password, magic links, OAuth)
- Anon key for public client access
- Service key for server-side access only — never expose in client code

## Build & Deploy
- Backend builds and deploys via GitHub Actions (push to main triggers CI/CD)
- Do not run deploy scripts manually — just commit and push

## Sovereignty
- All infrastructure runs in EU (France) on Scaleway
- No US cloud services permitted (AWS, GCP, Azure, Cloudflare, Stripe, Vercel)

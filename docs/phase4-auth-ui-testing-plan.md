# Phase 4 — Manual Testing Plan (Built-in Auth Console UI)

Covers console UI changes for the built-in auth integration:
Auth guard, dynamic user email, apikey header pattern, Hanko cleanup, account page, 401 redirect.

## Prerequisites

- Gateway running (`make dev`) — do NOT set `DEV_MODE=true`, as that bypasses platform auth
- Console dev server running (`cd console && npm run dev`)
- At least one project provisioned
- A valid platform account (sign up via `/login` if needed)

---

## 1. Auth Guard — Redirect Unauthenticated Users

### 1.1 No Token → Redirect
- [ ] Clear `eurobase_token` and `eurobase_email` from localStorage (DevTools > Application > Local Storage)
- [ ] Navigate to `/projects` — should redirect to `/login`
- [ ] Navigate to `/p/{any-id}/database` — should redirect to `/login`
- [ ] Navigate to `/account` — should redirect to `/login`
- [ ] Navigate to `/onboarding` — should redirect to `/login`

### 1.2 Valid Token → No Redirect
- [ ] Sign in via `/login`
- [ ] Navigate to `/projects` — page loads normally, no redirect
- [ ] Navigate to `/account` — page loads normally

---

## 2. Dynamic User Email in Top Bar

### 2.1 Email Display
- [ ] Sign in with `test@example.com`
- [ ] Verify top bar shows `test@example.com` (not `dev@eurobase.app`)
- [ ] Verify avatar circle shows `T` (first letter of email, uppercased)

### 2.2 Different Account
- [ ] Sign out, sign in with a different email (e.g. `admin@eurobase.app`)
- [ ] Verify top bar updates to `admin@eurobase.app` with avatar `A`

### 2.3 Responsive
- [ ] Shrink browser to mobile width — email text hides (`hidden sm:block`), avatar still visible

---

## 3. API Explorer — apikey Header Pattern

### 3.1 Required Headers Section
- [ ] Navigate to a project's API tab (`/p/{id}/api`)
- [ ] Verify "Required Header" shows `apikey: <your-public-key>`
- [ ] Verify "Optional Header" shows `Authorization: Bearer <end-user-jwt>`
- [ ] Verify old `X-Project-Id` header is NOT shown
- [ ] Verify old `Authorization: Bearer <token>` is NOT shown as the required header

### 3.2 cURL Snippets
- [ ] Select a table, view the cURL tab
- [ ] Verify "List rows" uses `-H "apikey: YOUR_PUBLIC_KEY"` (no `Authorization` or `X-Project-Id`)
- [ ] Verify "List rows (as authenticated end-user)" shows both `apikey` and `Authorization: Bearer END_USER_JWT`
- [ ] Verify "Insert a row" uses `-H "apikey: YOUR_PUBLIC_KEY"`
- [ ] Verify auth signup/signin cURL examples are present at the bottom

### 3.3 SDK Snippet
- [ ] Switch to SDK tab — verify `apiKey: 'eb_pk_your_public_key'` is shown (unchanged, already correct)

### 3.4 End-User Auth Endpoints Section
- [ ] Verify new "End-User Auth Endpoints" card appears between table endpoints and schema
- [ ] Verify it lists: `POST /v1/auth/signup`, `POST /v1/auth/signin`, `POST /v1/auth/refresh`, `POST /v1/auth/signout`, `GET /v1/auth/user`
- [ ] Verify methods have correct color badges (POST = blue, GET = green)

---

## 4. Database — hanko_user_id Removed

### 4.1 Insert Form
- [ ] Navigate to Database > select `users` table
- [ ] Click "Insert Row" (or "Add User")
- [ ] Verify `hanko_user_id` does NOT appear in the form fields
- [ ] Verify `id`, `created_at`, `updated_at` still do NOT appear (still auto-generated)
- [ ] Verify other editable columns (e.g. `email`) DO appear

---

## 5. Account Page

### 5.1 Navigation
- [ ] Click "Account" in the sidebar — navigates to `/account`
- [ ] Verify page renders (not blank)

### 5.2 Content
- [ ] Verify "Account" heading and "Manage your Eurobase account." subtitle
- [ ] Verify "Email" section shows the logged-in user's email (matches top bar)
- [ ] Verify "Session" section has a red "Sign Out" button
- [ ] Verify "Change password and delete account coming soon." placeholder exists

### 5.3 Sign Out
- [ ] Click "Sign Out" on the account page
- [ ] Verify redirect to `/login`
- [ ] Verify `eurobase_token` and `eurobase_email` are cleared from localStorage
- [ ] Navigate to `/projects` — should redirect to `/login` (guard still works)

---

## 6. API 401 Redirect

### 6.1 Expired/Invalid Token
- [ ] Sign in normally
- [ ] Open DevTools > Application > Local Storage
- [ ] Replace `eurobase_token` value with `invalid_token_abc`
- [ ] Trigger any API call (e.g. navigate to `/projects`, which calls `listProjects`)
- [ ] Verify: redirects to `/login`
- [ ] Verify: `eurobase_token` and `eurobase_email` are cleared from localStorage

### 6.2 Token Removal Mid-Session
- [ ] Sign in normally, navigate to a project's Database tab
- [ ] Delete `eurobase_token` from localStorage entirely
- [ ] Click "Refresh" on the data grid (triggers an API call)
- [ ] Verify: redirects to `/login`

---

## 7. API Client Comment Cleanup

### 7.1 Code Review (not a runtime test)
- [ ] Open `console/src/lib/api.ts`
- [ ] Verify docstring says "built-in platform auth system" — no mention of Hanko

---

## 8. End-to-End Flow

### 8.1 Full Sign-Up → Use → Sign-Out
- [ ] Start with cleared localStorage
- [ ] Navigate to `/projects` — redirected to `/login`
- [ ] Sign up with a new email/password
- [ ] Verify redirect to `/projects` or `/onboarding`
- [ ] Top bar shows the new email
- [ ] Create a project (or use existing)
- [ ] Navigate to API tab — apikey headers shown correctly
- [ ] Navigate to Database tab — insert form has no `hanko_user_id`
- [ ] Navigate to Account — email displayed, click Sign Out
- [ ] Redirected to `/login`, cannot access `/projects` without signing in again

### 8.2 Sign-In After Sign-Out
- [ ] From `/login`, sign in with the same credentials
- [ ] Verify redirect to `/projects`, top bar shows correct email
- [ ] All pages accessible again

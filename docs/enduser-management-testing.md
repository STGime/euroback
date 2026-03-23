# End-User Management — Testing Guide

## Prerequisites

1. **Gateway running:**
   ```bash
   go run ./cmd/gateway/
   ```
2. **Console running:**
   ```bash
   cd console && npm run dev
   ```
3. **A project exists** — run the Phase 1 Postman collection first to create a tenant and get a `project_id`.
4. **Migration 000011 applied** — `banned_at` column must exist on tenant user tables.
5. **Clean state** — delete any leftover test users before running the Postman collection.

---

## Backend Testing (Postman)

### Import & configure

1. Open Postman and import `docs/eurobase-enduser-management.postman_collection.json`
2. Select the **Eurobase Local** environment
3. Ensure **`project_id`** is set in the environment (or collection variables) to your project's ID
4. Ensure **`platform_token`** is set in the environment — sign in and copy the token, or use the auth tests collection to get one

### Run the full suite

Click **"Run collection"** (Collection Runner) to execute all 27 requests in order. Every request has automated test scripts that assert status codes and response shapes.

The collection is organized into 5 folders:

| Folder | Requests | What it tests |
|--------|----------|---------------|
| CRUD & Validation | 1–8 | List, create with metadata, get single user, duplicate/short-pw/empty-email errors |
| Edit User | 9–12 | Update display name, metadata, email; duplicate email error; restore email |
| Suspend / Unsuspend | 13–16 | Suspend, verify via GET, double-suspend error, unsuspend |
| Password Reset | 17–18 | Reset password, short password validation |
| Search & Pagination | 19–23 | Search by email, by display name, no results, limit=1, offset=1 |
| Delete & Not Found | 24–27 | Delete user, delete non-existent, cleanup, final empty check |

All 55 assertions should pass. The collection cleans up after itself.

---

## Frontend Testing (Browser)

Open the console at `http://localhost:5173` and navigate to a project.

### 1. Tab visibility

- [ ] **"Users"** tab appears between "Storage" and "API" in the project nav
- [ ] Clicking it navigates to `/p/{id}/users`

### 2. Empty state

- [ ] With no end-users, the page shows a people icon, "No users yet" heading, and a subtitle mentioning self-signup + manual invite
- [ ] An "Add User" CTA button is shown in the empty state
- [ ] The header also has an "Add User" button (always visible)

### 3. Loading state

- [ ] Throttle network in DevTools (Slow 3G), refresh the Users page
- [ ] 3 skeleton rows with avatar circles and text placeholders appear while loading

### 4. Create user — basic

1. [ ] Click **"Add User"** — modal opens with Email, Password, and Metadata fields
2. [ ] Button says "Create User" and is **disabled** when fields are empty
3. [ ] Type an email, enter a 5-character password — button stays disabled
4. [ ] Enter 8+ character password — button becomes enabled
5. [ ] Leave metadata empty, click **"Create User"**
6. [ ] Modal closes, table appears with the new user
7. [ ] User count badge appears next to the "Users" heading

### 5. Create user — with metadata

1. [ ] Click **"Add User"** again
2. [ ] Enter a different email and password
3. [ ] In the Metadata field, type: `{"role": "admin", "company": "Acme"}`
4. [ ] Click **"Create User"**
5. [ ] User appears in the table

### 6. Create user — validation errors

- [ ] Open "Add User", enter an email that already exists — red error banner shows "email already exists"
- [ ] Enter invalid JSON in metadata (e.g. `{broken`) — error banner shows "Invalid JSON in metadata field"
- [ ] Close modal, re-open — error banner is cleared

### 7. Table rendering

- [ ] Columns: **Email** (with colored avatar initial), **Display Name**, **Status**, **Last Sign In**, **Created**, **Actions**
- [ ] Email column shows first letter of email as avatar
- [ ] Display Name shows "—" for users without one
- [ ] Status shows green "Active" badge for normal users
- [ ] Last Sign In shows "Never" for users who haven't signed in
- [ ] Created shows a formatted date like "23 Mar 2026"
- [ ] Each row has 4 action icons: edit (pencil), reset password (key), suspend (ban circle), delete (trash)

### 8. User detail view (expandable row)

1. [ ] Click on a user's email — the row expands to show a detail panel
2. [ ] **Left side**: User Details with ID (monospace UUID), Email, Display Name, Status, Last Sign In, Created
3. [ ] **Right side**: Metadata section — shows "No metadata set." if empty, or a formatted JSON block if metadata exists
4. [ ] Click the same email again — detail panel collapses
5. [ ] Click a different user — first panel closes, second opens

### 9. Edit user

1. [ ] Click the **pencil icon** on a user row — Edit modal opens
2. [ ] Fields are pre-filled: Email, Display Name, Metadata (as formatted JSON)
3. [ ] Change the Display Name to "Alice Wonderland", click **"Save Changes"**
4. [ ] Modal closes, table updates — Display Name column now shows "Alice Wonderland"
5. [ ] Open edit again, change email to a different value, save — email updates in table
6. [ ] Open edit, change email to another user's email — red error: "email already taken"
7. [ ] Open edit, update Metadata JSON (e.g. add `"verified": true`), save
8. [ ] Expand the user detail row — metadata shows the updated value
9. [ ] Open edit, enter invalid JSON in metadata — error: "Invalid JSON in metadata field"
10. [ ] Open edit, change nothing, click "Save Changes" — modal closes without error (no-op)

### 10. Suspend / unsuspend user

1. [ ] Click the **ban icon** (circle with line) on an active user
2. [ ] Status column changes from green "Active" to red "Suspended"
3. [ ] Avatar circle changes from brand color to red
4. [ ] The ban icon changes to an **unlock icon** (green tint)
5. [ ] Expand the user detail — Status shows "Suspended since [date/time]"
6. [ ] Click the **unlock icon** — status reverts to green "Active"
7. [ ] Avatar circle returns to brand color

### 11. Reset password

1. [ ] Click the **key icon** on a user row — Reset Password modal opens
2. [ ] Modal shows the user's email and a warning that all sessions will be revoked
3. [ ] New Password field is empty, button says "Reset Password" and is disabled
4. [ ] Enter a 5-character password — button stays disabled
5. [ ] Enter 8+ character password — button becomes enabled
6. [ ] Click **"Reset Password"** — modal closes (success, no visible change in table)
7. [ ] Click Cancel on another reset attempt — modal closes without action

### 12. Delete user

1. [ ] Click the **trash icon** on a user row — delete confirmation modal appears
2. [ ] Modal shows warning icon, "Delete User" heading, user's email in bold, and message about permanent removal + session revocation
3. [ ] Click **"Cancel"** — modal closes, user still in table
4. [ ] Click trash again, click **"Delete"**
5. [ ] User disappears from the table
6. [ ] User count badge updates

### 13. Delete last user returns to empty state

- [ ] Delete all users one by one
- [ ] After the last delete, the empty state ("No users yet") reappears

### 14. Search

1. [ ] Create at least 2 users (e.g. alice@example.com and bob@example.com, give alice display name "Alice W")
2. [ ] Type "alice" in the search bar — only alice appears, bob is filtered out
3. [ ] Clear search — both users appear again
4. [ ] Type "Alice W" — alice appears (matched by display name)
5. [ ] Type "nonexistent" — empty state shows: No users matching "nonexistent"
6. [ ] Clear search — all users reappear
7. [ ] Verify search is **debounced** — typing quickly only triggers one request (watch Network tab)

### 15. Pagination

> Requires 51+ users to test naturally. Alternatively, temporarily change `pageSize` to 1 in the Svelte file to test with fewer users.

1. [ ] With more users than `pageSize`, the pagination bar appears below the table
2. [ ] Shows "Showing 1–50 of N" text
3. [ ] **"Previous"** button is disabled on page 1
4. [ ] Click **"Next"** — table shows the next page of users, "Previous" becomes enabled
5. [ ] Click **"Previous"** — returns to page 1
6. [ ] Searching resets to page 1

### 16. Error handling

- [ ] Stop the gateway, refresh the Users page — error banner appears with a connection error message
- [ ] Start the gateway, refresh — users load normally

---

## Checklist summary

| Area | Tests |
|------|-------|
| Tab & navigation | 2 |
| Empty state | 3 |
| Loading state | 2 |
| Create user | 9 |
| Table rendering | 7 |
| Detail view | 5 |
| Edit user | 10 |
| Suspend / unsuspend | 7 |
| Reset password | 7 |
| Delete user | 6 |
| Search | 7 |
| Pagination | 6 |
| Error handling | 2 |
| **Total** | **73** |

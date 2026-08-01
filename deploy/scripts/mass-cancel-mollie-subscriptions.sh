#!/usr/bin/env bash
# Emergency mass-cancel of every LIVE Mollie subscription tied to
# a Eurobase project. Written for the PR 8 rollback path — if
# something goes wrong on launch day and we flip BILLING_ENABLED
# back to false, our webhooks silently 200 (see PR 4), so any
# subscription Mollie has on file will keep charging our users
# unless we explicitly cancel it.
#
# Usage:
#
#   MOLLIE_API_KEY=live_xxx DATABASE_URL=postgres://... \
#     ./deploy/scripts/mass-cancel-mollie-subscriptions.sh
#
# The script is idempotent — Mollie returns 200 with a
# canceled-state resource on already-canceled subscriptions.
# Runs at ~1 req/sec so we don't trip Mollie's rate limit (10
# req/sec at their default, but conservative on shared IPs).
#
# By design this does NOT touch the local DB. Local downgrade
# happens via the PR 5 sweep once BILLING_ENABLED comes back on
# OR via manual SQL if we're staying rolled back. Reasoning:
# separating the "stop billing" action from the "flip local
# state" action is safer in a firefight — one is reversible
# (re-charge tomorrow) and one is not (users lose Pro).

set -euo pipefail

: "${MOLLIE_API_KEY:?MOLLIE_API_KEY must be set (live_… or test_…)}"
: "${DATABASE_URL:?DATABASE_URL must be set}"

MOLLIE_HOST="${MOLLIE_HOST:-https://api.mollie.com}"

echo "Fetching live subscriptions from Eurobase DB..."
# Query returns (mollie_customer_id, mollie_subscription_id) for
# every subscription in a live state. Anything else is either
# already-canceled or never-activated (no Mollie sub exists).
subs=$(psql "$DATABASE_URL" -t -A -F, -c "
    SELECT mollie_customer_id, mollie_subscription_id
      FROM public.subscriptions
     WHERE status IN ('active', 'past_due', 'incomplete')
       AND mollie_customer_id IS NOT NULL
       AND mollie_subscription_id IS NOT NULL
       AND mollie_subscription_id != ''
")

if [ -z "$subs" ]; then
    echo "No live subscriptions found — nothing to do."
    exit 0
fi

count=$(echo "$subs" | wc -l | tr -d ' ')
echo "Found $count subscription(s) to cancel."
echo "Continue? [y/N]"
read -r confirm
if [ "$confirm" != "y" ]; then
    echo "Aborted."
    exit 1
fi

canceled=0
failed=0

while IFS=, read -r customer_id subscription_id; do
    [ -z "$customer_id" ] && continue
    [ -z "$subscription_id" ] && continue

    echo -n "Cancelling $subscription_id (customer $customer_id)... "

    http_code=$(curl -s -o /tmp/mollie-cancel-out.json -w "%{http_code}" \
        -X DELETE \
        "$MOLLIE_HOST/v2/customers/$customer_id/subscriptions/$subscription_id" \
        -H "Authorization: Bearer $MOLLIE_API_KEY" \
        -H "Accept: application/json")

    if [ "$http_code" = "200" ] || [ "$http_code" = "204" ]; then
        canceled=$((canceled + 1))
        echo "OK"
    elif [ "$http_code" = "404" ]; then
        # Already gone at Mollie — that's a success for our
        # purposes.
        canceled=$((canceled + 1))
        echo "already canceled"
    else
        failed=$((failed + 1))
        echo "FAILED (HTTP $http_code)"
        cat /tmp/mollie-cancel-out.json
        echo
    fi

    # Conservative rate-limit — Mollie's default is 10 req/s
    # but shared IPs may see stricter policies. 1 req/s keeps
    # us safe across the widest audience.
    sleep 1
done <<< "$subs"

echo
echo "Done. Canceled: $canceled. Failed: $failed."
if [ "$failed" -gt 0 ]; then
    echo "Failures need manual cleanup — check the Mollie dashboard:"
    echo "  https://my.mollie.com/dashboard/customers"
    exit 2
fi

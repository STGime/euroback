package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/eurobase/euroback/internal/billing"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// transferBillingHooks adapts the billing service to the project ownership
// transfer handlers (#846), which don't import billing. Both are nil when
// billing isn't wired.
func transferBillingHooks(svc *billing.Service) (tenant.BillingHandover, tenant.CheckTransferable) {
	if svc == nil {
		return nil, nil
	}
	handover := func(ctx context.Context, tx pgx.Tx, projectID string) (bool, *time.Time, func(context.Context), error) {
		res, finish, err := svc.HandOverInTx(ctx, tx, projectID)
		if err != nil {
			return false, nil, nil, err
		}
		return res.HandedOver, res.PaidUntil, func(ctx context.Context) { finish(ctx) }, nil
	}
	check := func(ctx context.Context, projectID string) error {
		err := svc.CheckTransferable(ctx, projectID)
		if errors.Is(err, billing.ErrPaymentOverdue) {
			return tenant.ErrTransferPaymentOverdue
		}
		return err
	}
	return handover, check
}

package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/eurobase/euroback/internal/billing"
	"github.com/eurobase/euroback/internal/tenant"
)

// transferBillingHooks adapts the billing service to the project ownership
// transfer handlers (#846), which don't import billing. Both are nil when
// billing isn't wired.
func transferBillingHooks(svc *billing.Service) (tenant.BillingHandover, tenant.CheckTransferable) {
	if svc == nil {
		return nil, nil
	}
	handover := func(ctx context.Context, projectID string) (bool, *time.Time, error) {
		res, err := svc.HandOverOnTransfer(ctx, projectID)
		if res == nil {
			return false, nil, err
		}
		return res.HandedOver, res.PaidUntil, err
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

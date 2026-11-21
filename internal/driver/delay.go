package driver

import (
	"context"
	"sync"
	"time"
)

// DefaultRequestIntervalMS is the default minimum interval between platform API
// requests for one account, in milliseconds.
//
// Why it exists: walking a share directory file by file and transferring files
// one by one is the main trigger for platform risk control. A gap between
// requests is the cheapest and most effective mitigation. Quark and Guangya are
// both reverse-engineered Web APIs with no published rate limits, so this uses
// the conservative value shared by similar projects (CASX / quark-auto-save).
//
// Drivers pass their own baseline interval to WaitRequestInterval; a value <= 0
// falls back to this default, so drivers without an explicit interval still get
// basic throttling instead of running completely unthrottled.
const DefaultRequestIntervalMS = 300

// DelayController serializes API requests per account: two requests for the same
// accountID are never issued less than the configured interval apart.
type DelayController struct {
	mu       sync.Mutex
	accounts map[int64]*accountDelay
}

type accountDelay struct {
	mu   sync.Mutex
	last time.Time
}

func NewDelayController() *DelayController {
	return &DelayController{accounts: make(map[int64]*accountDelay)}
}

// RequestIntervalGate is injected into driver instances; drivers call Wait before
// issuing a platform API request.
type RequestIntervalGate interface {
	Wait(ctx context.Context, interval time.Duration) error
}

type accountGate struct {
	dc        *DelayController
	accountID int64
}

func (g accountGate) Wait(ctx context.Context, interval time.Duration) error {
	return g.dc.wait(ctx, g.accountID, interval)
}

// Gate returns an interval gate bound to the given account, for Manager to inject.
func (dc *DelayController) Gate(accountID int64) RequestIntervalGate {
	return accountGate{dc: dc, accountID: accountID}
}

// DropAccount clears the per-account interval state to avoid unbounded map growth.
func (dc *DelayController) DropAccount(accountID int64) {
	dc.mu.Lock()
	delete(dc.accounts, accountID)
	dc.mu.Unlock()
}

func (dc *DelayController) account(accountID int64) *accountDelay {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if ad, ok := dc.accounts[accountID]; ok {
		return ad
	}
	ad := &accountDelay{}
	dc.accounts[accountID] = ad
	return ad
}

func (dc *DelayController) wait(ctx context.Context, accountID int64, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}

	ad := dc.account(accountID)
	ad.mu.Lock()
	defer ad.mu.Unlock()

	if !ad.last.IsZero() {
		if wait := interval - time.Since(ad.last); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
	}
	ad.last = time.Now()
	return nil
}

type extraDelayKey struct{}

// WithExtraAPIDelay adds a task-level extra API interval (ms) onto ctx, which is
// summed with the driver's own operation_delay and applied by the same gate.
func WithExtraAPIDelay(ctx context.Context, ms int) context.Context {
	if ms <= 0 {
		return ctx
	}
	return context.WithValue(ctx, extraDelayKey{}, ms)
}

// ExtraAPIDelayMS reads the task-level extra interval from ctx.
func ExtraAPIDelayMS(ctx context.Context) int {
	v, _ := ctx.Value(extraDelayKey{}).(int)
	return v
}

// WaitRequestInterval waits on the per-account gate for baseMS + extra delay.
//
// baseMS == 0 falls back to DefaultRequestIntervalMS: a driver that declares no
// interval of its own is not thereby declaring "no throttling" (Quark previously
// used requestInterval=0, which meant zero throttling). A negative value is the
// explicit way for a driver to opt out of throttling.
func WaitRequestInterval(ctx context.Context, gate RequestIntervalGate, baseMS int) error {
	if gate == nil {
		return nil
	}
	if baseMS < 0 {
		return nil
	}
	if baseMS == 0 {
		baseMS = DefaultRequestIntervalMS
	}
	ms := baseMS + ExtraAPIDelayMS(ctx)
	if ms <= 0 {
		return nil
	}
	return gate.Wait(ctx, time.Duration(ms)*time.Millisecond)
}
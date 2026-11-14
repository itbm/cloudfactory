package actions

import (
	"context"
	"errors"
	"fmt"
)

// ApplyDiskQuotas sets the disk limit of each website of this server, when the server has
// user quotas.
//
// A website gets its limit when the agent makes it, and when its limits change. This function
// is for the websites that were there before the quotas were on: the installation script
// turns on the quotas of a server of an earlier release, and starts the agent again. Without
// this function, such a website would have no limit until a change of its limits, and the
// panel would show a limit that the server does not apply.
func (d *Deps) ApplyDiskQuotas(ctx context.Context) error {
	if d.QuotaMount == "" {
		return nil
	}
	sites, err := d.State.All()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sites {
		if s.Website == nil {
			continue
		}
		if err := d.applyDiskQuota(ctx, s.UnixUser, s.Website.GetLimits()); err != nil {
			errs = append(errs, fmt.Errorf("the disk limit of %s: %w", s.UnixUser, err))
		}
	}
	return errors.Join(errs...)
}

package obs

import (
	"context"

	"github.com/cplieger/go/measure/v2"
)

func (o *Obs) Gather(ctx context.Context) ([]measure.Family, error) { return o.reg.Gather(ctx) }

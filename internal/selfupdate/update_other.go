//go:build !linux && !darwin

package selfupdate

import (
	"context"
	"fmt"
	"io"
)

func Run(_ context.Context, _ string, _ io.Writer) error {
	return fmt.Errorf("servctl update 仅支持 Linux")
}

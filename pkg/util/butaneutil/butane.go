package butaneutil

import (
	"fmt"
	"log/slog"

	butane "github.com/coreos/butane/config"
	butanecommon "github.com/coreos/butane/config/common"
)

func Translate(but string) ([]byte, error) {
	options := butanecommon.TranslateBytesOptions{}
	options.NoResourceAutoCompression = true
	options.Pretty = true

	ign, report, err := butane.TranslateBytes([]byte(but), options)
	if err != nil {
		return nil, fmt.Errorf("translating config: %w", err)
	}
	if len(report.Entries) > 0 {
		slog.Error("butane", "report", report.String())

		return nil, fmt.Errorf("butane produced warnings and strict mode is enabled") //nolint:goerr113
	}

	return ign, nil
}

package hhfab

import "context"

func Build(ctx context.Context, workDir, cacheDir string, hMode HydrateMode) error {
	_, err := load(ctx, workDir, cacheDir, true, hMode)
	if err != nil {
		return err
	}

	panic("not implemented")

	return nil
}

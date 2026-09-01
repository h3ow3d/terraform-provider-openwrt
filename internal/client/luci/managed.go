package luci

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

func (c *rpcClient) ApplyManagedBlocks(ctx context.Context, blocks []ManagedBlock) error {
	return c.applyManaged(ctx, blocks, false)
}

func (c *rpcClient) DeleteManagedBlocks(ctx context.Context, blocks []ManagedBlock) error {
	return c.applyManaged(ctx, blocks, true)
}

func (c *rpcClient) applyManaged(ctx context.Context, blocks []ManagedBlock, remove bool) error {
	if len(blocks) == 0 {
		return nil
	}

	original := map[string]string{}
	updated := map[string]string{}
	changedPackages := map[string]bool{}
	restartServices := map[string]bool{}

	for _, b := range blocks {
		if b.Package == "" {
			return fmt.Errorf("managed block package cannot be empty")
		}
		if b.Key == "" {
			return fmt.Errorf("managed block key cannot be empty")
		}

		if _, ok := original[b.Package]; !ok {
			content, err := c.readConfigFile(ctx, b.Package)
			if err != nil {
				return fmt.Errorf("reading package %s: %w", b.Package, err)
			}
			original[b.Package] = content
			updated[b.Package] = content
		}

		before := updated[b.Package]
		if remove {
			updated[b.Package] = removeManagedBlock(before, b.Key)
		} else {
			updated[b.Package] = upsertManagedBlock(before, b.Key, b.Block)
		}

		if updated[b.Package] != before {
			changedPackages[b.Package] = true
			if b.Service != "" {
				restartServices[b.Service] = true
			}
		}
	}

	if len(changedPackages) == 0 {
		return nil
	}

	packages := mapKeys(changedPackages)
	slices.Sort(packages)

	for _, pkg := range packages {
		if err := c.writeConfigFile(ctx, pkg, updated[pkg]); err != nil {
			c.rollbackPackages(ctx, packages, original)
			return fmt.Errorf("writing package %s: %w", pkg, err)
		}
	}

	for _, pkg := range packages {
		if err := c.commitPackage(ctx, pkg); err != nil {
			c.rollbackPackages(ctx, packages, original)
			return fmt.Errorf("committing package %s: %w", pkg, err)
		}
	}

	services := mapKeys(restartServices)
	slices.Sort(services)
	for _, service := range services {
		if err := c.restartService(ctx, service); err != nil {
			c.rollbackPackages(ctx, packages, original)
			return fmt.Errorf("restarting service %s: %w", service, err)
		}
	}

	return nil
}

func (c *rpcClient) rollbackPackages(ctx context.Context, packages []string, original map[string]string) error {
	var rollbackErrs []error
	for _, pkg := range packages {
		content, ok := original[pkg]
		if !ok {
			continue
		}
		if err := c.rollbackFile(ctx, pkg, content); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback failed for %s: %w", pkg, err))
		}
	}
	return errors.Join(rollbackErrs...)
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

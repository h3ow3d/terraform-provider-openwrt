package provider

import (
	"context"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/luci"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/modernubus"
)

type clientBundle struct {
	legacy luci.Client
	modern *modernubus.Client
}

func newClientBundle(legacy luci.Client, modern *modernubus.Client) clientBundle {
	return clientBundle{
		legacy: legacy,
		modern: modern,
	}
}

func (b clientBundle) ModernUBUS() *modernubus.Client {
	return b.modern
}

func (b clientBundle) ApplyNetwork(ctx context.Context, req luci.ApplyNetworkRequest) error {
	return b.legacy.ApplyNetwork(ctx, req)
}

func (b clientBundle) DeleteNetwork(ctx context.Context, name string) error {
	return b.legacy.DeleteNetwork(ctx, name)
}

func (b clientBundle) UpsertDHCPHost(ctx context.Context, req luci.UpsertDHCPHostRequest) error {
	return b.legacy.UpsertDHCPHost(ctx, req)
}

func (b clientBundle) DeleteDHCPHost(ctx context.Context, id string) error {
	return b.legacy.DeleteDHCPHost(ctx, id)
}

func (b clientBundle) ApplyManagedBlocks(ctx context.Context, blocks []luci.ManagedBlock) error {
	return b.legacy.ApplyManagedBlocks(ctx, blocks)
}

func (b clientBundle) DeleteManagedBlocks(ctx context.Context, blocks []luci.ManagedBlock) error {
	return b.legacy.DeleteManagedBlocks(ctx, blocks)
}

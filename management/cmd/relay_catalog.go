package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	nbconfig "github.com/netbirdio/netbird/management/internals/server/config"
	"github.com/netbirdio/netbird/management/server/relaypolicy"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
)

func newRelayCatalogCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay-catalog",
		Short: "Preview and publish the operator-approved relay catalog",
	}
	for _, publish := range []bool{false, true} {
		name := "preview"
		if publish {
			name = "publish"
		}
		var expected uint64
		action := &cobra.Command{
			Use:  name,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return withAdminConfig(cmd, false, func(ctx context.Context, cfg *nbconfig.Config, datadir string) (resultErr error) {
					s, err := openAdminStore(ctx, cfg, datadir)
					if err != nil {
						return err
					}
					defer func() {
						if err := s.Close(ctx); err != nil {
							resultErr = errors.Join(resultErr, fmt.Errorf("close relay catalog store: %w", err))
						}
					}()
					repository, ok := s.(store.RelayPolicyStore)
					if !ok {
						return fmt.Errorf("store does not support relay catalog publication")
					}
					candidate, err := configuredRelayCatalog(cfg.Relay)
					if err != nil {
						return err
					}
					if !cmd.Flags().Changed("expected-generation") {
						current, err := repository.GetRelayCatalog(ctx)
						if err != nil {
							return err
						}
						expected = current.Generation
					}
					if publish {
						catalog, err := repository.PublishRelayCatalog(ctx, candidate, expected)
						if err != nil {
							return err
						}
						return json.NewEncoder(cmd.OutOrStdout()).Encode(catalog)
					}
					impact, err := repository.PreviewRelayCatalog(ctx, candidate, expected)
					if err != nil {
						return err
					}
					return json.NewEncoder(cmd.OutOrStdout()).Encode(impact)
				})
			},
		}
		action.Flags().Uint64Var(&expected, "expected-generation", 0, "Catalog generation from the impact preview")
		if publish {
			if err := action.MarkFlagRequired("expected-generation"); err != nil {
				panic(err)
			}
		}
		cmd.AddCommand(action)
	}
	return cmd
}

func configuredRelayCatalog(relay *nbconfig.Relay) (types.RelayCatalog, error) {
	if relay == nil {
		return types.RelayCatalog{}, nil
	}
	return relaypolicy.BootstrapCatalog(relay.Addresses, relay.Catalog)
}

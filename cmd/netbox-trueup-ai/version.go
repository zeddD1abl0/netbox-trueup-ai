package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/version"
)

func newVersionCommand() *cobra.Command {
	var out output
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := version.String()
			if out == outputJSON {
				return writeJSON(cmd.OutOrStdout(), struct {
					Version string `json:"version"`
				}{v})
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "netbox-trueup-ai", v); err != nil {
				return fmt.Errorf("write output: %w", err)
			}
			return nil
		},
	}
	addOutputFlag(cmd, &out)
	return cmd
}

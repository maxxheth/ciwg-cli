package dnsbackup

import (
	"context"
	"fmt"
	"strings"

	dnsbackup "ciwg-cli/internal/dnsbackup"

	"github.com/spf13/cobra"
)

func runExport(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}
	zone := args[0]
	token, err := requireToken(mustGetStringFlag(cmd, "token"))
	if err != nil {
		return err
	}
	client, err := dnsbackup.NewClient(token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), mustGetDurationFlag(cmd, "timeout"))
	defer cancel()

	snapshot, err := client.Export(ctx, zone)
	if err != nil {
		return err
	}

	if meta, err := parseMetadata(mustGetStringSliceFlag(cmd, "metadata")); err != nil {
		return err
	} else if len(meta) > 0 {
		snapshot.Metadata = meta
	}

	format := strings.ToLower(mustGetStringFlag(cmd, "format"))
	if format == "" {
		format = "json"
	}
	pretty := mustGetBoolFlag(cmd, "pretty")
	output := mustGetStringFlag(cmd, "output")
	if output == "" {
		payload, err := dnsbackup.EncodeSnapshot(snapshot, format, pretty)
		if err != nil {
			return err
		}
		if _, err := cmd.OutOrStdout().Write(payload); err != nil {
			return err
		}
		if len(payload) == 0 || payload[len(payload)-1] != '\n' {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	}

	if err := dnsbackup.SaveSnapshot(snapshot, output, format, pretty); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Snapshot saved to %s\n", output)
	return nil
}

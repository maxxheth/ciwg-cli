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
	token, err := requireToken(mustGetStringFlag(cmd, "token"))
	if err != nil {
		return err
	}
	client, err := dnsbackup.NewClient(token)
	if err != nil {
		return err
	}
	zones, err := resolveZones(cmd, args, client)
	if err != nil {
		return err
	}

	meta, err := parseMetadata(mustGetStringSliceFlag(cmd, "metadata"))
	if err != nil {
		return err
	}

	format := strings.ToLower(mustGetStringFlag(cmd, "format"))
	if format == "" {
		format = "json"
	}
	pretty := mustGetBoolFlag(cmd, "pretty")
	outputBase := mustGetStringFlag(cmd, "output")
	multi := len(zones) > 1
	timeout := mustGetDurationFlag(cmd, "timeout")
	defaultPattern := fmt.Sprintf("%%s.%s", formatExtension(format))

	for _, target := range zones {
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		snapshot, err := client.Export(ctx, target.ZoneName)
		cancel()
		if err != nil {
			return err
		}
		if len(meta) > 0 {
			snapshot.Metadata = meta
		}

		path, toStdout, err := resolveZoneFilePath(outputBase, target.ZoneName, defaultPattern, "--output", multi, true)
		if err != nil {
			return err
		}

		if toStdout {
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
			continue
		}

		if err := ensureParentDir(path); err != nil {
			return fmt.Errorf("prepare output path for %s: %w", target.ZoneName, err)
		}
		if err := dnsbackup.SaveSnapshot(snapshot, path, format, pretty); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "[%s] Snapshot saved to %s\n", target.ZoneName, path)
	}

	return nil
}

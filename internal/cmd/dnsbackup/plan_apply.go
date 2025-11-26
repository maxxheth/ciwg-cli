package dnsbackup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dnsbackup "ciwg-cli/internal/dnsbackup"

	"github.com/spf13/cobra"
)

func runPlan(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}
	snapshotPath := mustGetStringFlag(cmd, "snapshot")
	if snapshotPath == "" {
		return errors.New("--snapshot is required")
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
	if len(zones) == 0 {
		return errors.New("no zones resolved")
	}

	snapshotFormat := mustGetStringFlag(cmd, "snapshot-format")
	snapshotPattern := fmt.Sprintf("%%s.%s", formatExtension(snapshotFormat))
	deleteMissing := mustGetBoolFlag(cmd, "delete-missing")
	planFormat := strings.ToLower(mustGetStringFlag(cmd, "format"))
	if planFormat == "" {
		planFormat = "json"
	}
	pretty := mustGetBoolFlag(cmd, "pretty")
	printPlan := mustGetBoolFlag(cmd, "print-plan")
	outputBase := mustGetStringFlag(cmd, "output")
	multi := len(zones) > 1
	timeout := mustGetDurationFlag(cmd, "timeout")
	for _, target := range zones {
		resolvedSnapshot, _, err := resolveZoneFilePath(snapshotPath, target.ZoneName, snapshotPattern, "--snapshot", multi, false)
		if err != nil {
			return err
		}
		snapshot, err := dnsbackup.LoadSnapshot(resolvedSnapshot, snapshotFormat)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		plan, err := client.Plan(ctx, target.ZoneName, snapshot, dnsbackup.PlanOptions{
			DeleteExtraneous: deleteMissing,
		})
		cancel()
		if err != nil {
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", target.ZoneName, summarizePlan(plan))
		if len(plan.Changes) == 0 {
			continue
		}

		if printPlan {
			fmt.Fprintf(cmd.OutOrStdout(), "-- %s plan --\n", target.ZoneName)
			if err := streamPlan(cmd, plan, planFormat, pretty); err != nil {
				return err
			}
		}

		if outputBase != "" {
			defaultPattern := fmt.Sprintf("%%s.%s", formatExtension(planFormat))
			planPath, _, err := resolveZoneFilePath(outputBase, target.ZoneName, defaultPattern, "--output", multi, true)
			if err != nil {
				return err
			}
			if err := ensureParentDir(planPath); err != nil {
				return fmt.Errorf("prepare plan output for %s: %w", target.ZoneName, err)
			}
			if err := dnsbackup.SavePlan(plan, planPath, planFormat, pretty); err != nil {
				return fmt.Errorf("write plan for %s: %w", target.ZoneName, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "[%s] Plan saved to %s\n", target.ZoneName, planPath)
		}
	}

	return nil
}

func runApply(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}
	path := mustGetStringFlag(cmd, "snapshot")
	if path == "" {
		return errors.New("--snapshot is required")
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
	if len(zones) == 0 {
		return errors.New("no zones resolved")
	}

	snapshotFormat := mustGetStringFlag(cmd, "snapshot-format")
	snapshotPattern := fmt.Sprintf("%%s.%s", formatExtension(snapshotFormat))
	deleteMissing := mustGetBoolFlag(cmd, "delete-missing")
	planFormat := strings.ToLower(mustGetStringFlag(cmd, "plan-format"))
	if planFormat == "" {
		planFormat = "json"
	}
	planPretty := mustGetBoolFlag(cmd, "plan-pretty")
	printPlan := mustGetBoolFlag(cmd, "print-plan")
	planOutputBase := mustGetStringFlag(cmd, "plan-output")
	multi := len(zones) > 1
	timeout := mustGetDurationFlag(cmd, "timeout")
	dryRun := mustGetBoolFlag(cmd, "dry-run")
	autoApprove := mustGetBoolFlag(cmd, "yes")

	for _, target := range zones {
		resolvedSnapshot, _, err := resolveZoneFilePath(path, target.ZoneName, snapshotPattern, "--snapshot", multi, false)
		if err != nil {
			return err
		}
		snapshot, err := dnsbackup.LoadSnapshot(resolvedSnapshot, snapshotFormat)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		plan, err := client.Plan(ctx, target.ZoneName, snapshot, dnsbackup.PlanOptions{
			DeleteExtraneous: deleteMissing,
		})
		if err != nil {
			cancel()
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", target.ZoneName, summarizePlan(plan))
		if planOutputBase != "" {
			defaultPattern := fmt.Sprintf("%%s.%s", formatExtension(planFormat))
			planPath, _, err := resolveZoneFilePath(planOutputBase, target.ZoneName, defaultPattern, "--plan-output", multi, true)
			if err != nil {
				cancel()
				return err
			}
			if err := ensureParentDir(planPath); err != nil {
				cancel()
				return fmt.Errorf("prepare plan output for %s: %w", target.ZoneName, err)
			}
			if err := dnsbackup.SavePlan(plan, planPath, planFormat, planPretty); err != nil {
				cancel()
				return fmt.Errorf("write plan for %s: %w", target.ZoneName, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "[%s] Plan saved to %s\n", target.ZoneName, planPath)
		}

		if len(plan.Changes) == 0 {
			cancel()
			continue
		}

		if printPlan {
			fmt.Fprintf(cmd.OutOrStdout(), "-- %s plan --\n", target.ZoneName)
			if err := streamPlan(cmd, plan, planFormat, planPretty); err != nil {
				cancel()
				return err
			}
		}

		if dryRun {
			fmt.Fprintf(cmd.ErrOrStderr(), "[%s] Dry run enabled; no changes applied\n", target.ZoneName)
			cancel()
			continue
		}

		if !autoApprove {
			cancel()
			return fmt.Errorf("refusing to apply changes to %s without --yes; rerun with --dry-run to preview", target.ZoneName)
		}

		for _, change := range plan.Changes {
			fmt.Fprintf(cmd.ErrOrStderr(), "[%s] Applying %s\n", target.ZoneName, describeChange(change))
		}

		if err := client.Apply(ctx, plan, dnsbackup.ApplyOptions{}); err != nil {
			cancel()
			return err
		}
		cancel()
		fmt.Fprintf(cmd.ErrOrStderr(), "[%s] DNS records updated successfully\n", target.ZoneName)
	}

	return nil
}

func streamPlan(cmd *cobra.Command, plan *dnsbackup.Plan, format string, pretty bool) error {
	payload, err := dnsbackup.EncodePlan(plan, format, pretty)
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

func buildClient(cmd *cobra.Command) (*dnsbackup.Client, context.Context, context.CancelFunc, error) {
	token, err := requireToken(mustGetStringFlag(cmd, "token"))
	if err != nil {
		return nil, nil, nil, err
	}
	client, err := dnsbackup.NewClient(token)
	if err != nil {
		return nil, nil, nil, err
	}
	timeout := mustGetDurationFlag(cmd, "timeout")
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	return client, ctx, cancel, nil
}

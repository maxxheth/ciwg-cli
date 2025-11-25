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
	zone := args[0]
	snapshotPath := mustGetStringFlag(cmd, "snapshot")
	if snapshotPath == "" {
		return errors.New("--snapshot is required")
	}

	snapshot, err := dnsbackup.LoadSnapshot(snapshotPath, mustGetStringFlag(cmd, "snapshot-format"))
	if err != nil {
		return err
	}

	client, ctx, cancel, err := buildClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	plan, err := client.Plan(ctx, zone, snapshot, dnsbackup.PlanOptions{
		DeleteExtraneous: mustGetBoolFlag(cmd, "delete-missing"),
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), summarizePlan(plan))
	if len(plan.Changes) == 0 {
		return nil
	}

	format := strings.ToLower(mustGetStringFlag(cmd, "format"))
	if format == "" {
		format = "json"
	}
	pretty := mustGetBoolFlag(cmd, "pretty")

	if mustGetBoolFlag(cmd, "print-plan") {
		if err := streamPlan(cmd, plan, format, pretty); err != nil {
			return err
		}
	}

	if output := mustGetStringFlag(cmd, "output"); output != "" {
		if err := dnsbackup.SavePlan(plan, output, format, pretty); err != nil {
			return fmt.Errorf("write plan: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Plan saved to %s\n", output)
	}

	return nil
}

func runApply(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}
	zone := args[0]
	path := mustGetStringFlag(cmd, "snapshot")
	if path == "" {
		return errors.New("--snapshot is required")
	}

	snapshot, err := dnsbackup.LoadSnapshot(path, mustGetStringFlag(cmd, "snapshot-format"))
	if err != nil {
		return err
	}

	client, ctx, cancel, err := buildClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	plan, err := client.Plan(ctx, zone, snapshot, dnsbackup.PlanOptions{
		DeleteExtraneous: mustGetBoolFlag(cmd, "delete-missing"),
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), summarizePlan(plan))
	if output := mustGetStringFlag(cmd, "plan-output"); output != "" {
		format := strings.ToLower(mustGetStringFlag(cmd, "plan-format"))
		if format == "" {
			format = "json"
		}
		if err := dnsbackup.SavePlan(plan, output, format, mustGetBoolFlag(cmd, "plan-pretty")); err != nil {
			return fmt.Errorf("write plan: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Plan saved to %s\n", output)
	}

	if len(plan.Changes) == 0 {
		return nil
	}

	planFormat := strings.ToLower(mustGetStringFlag(cmd, "plan-format"))
	if planFormat == "" {
		planFormat = "json"
	}
	planPretty := mustGetBoolFlag(cmd, "plan-pretty")
	if mustGetBoolFlag(cmd, "print-plan") {
		if err := streamPlan(cmd, plan, planFormat, planPretty); err != nil {
			return err
		}
	}

	dryRun := mustGetBoolFlag(cmd, "dry-run")
	if dryRun {
		fmt.Fprintln(cmd.ErrOrStderr(), "Dry run enabled; no changes applied")
		return nil
	}

	if !mustGetBoolFlag(cmd, "yes") {
		return errors.New("refusing to apply without --yes; rerun with --dry-run to preview")
	}

	for _, change := range plan.Changes {
		fmt.Fprintf(cmd.ErrOrStderr(), "Applying %s\n", describeChange(change))
	}

	if err := client.Apply(ctx, plan, dnsbackup.ApplyOptions{}); err != nil {
		return err
	}

	fmt.Fprintln(cmd.ErrOrStderr(), "DNS records updated successfully")
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

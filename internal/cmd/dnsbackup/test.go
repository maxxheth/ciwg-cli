package dnsbackup

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func runTest(cmd *cobra.Command, _ []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}
	client, ctx, cancel, err := buildClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	resp, err := client.VerifyToken(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Token %s status: %s\n", resp.ID, resp.Status)
	if !resp.NotBefore.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "Valid from: %s\n", resp.NotBefore.Format(time.RFC3339))
	}
	if !resp.ExpiresOn.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "Expires on: %s\n", resp.ExpiresOn.Format(time.RFC3339))
	}

	return nil
}

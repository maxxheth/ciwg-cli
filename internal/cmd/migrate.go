package cmd

import (
	"fmt"
	"os"

	"ciwg-cli/internal/utils"

	"github.com/spf13/cobra"
)

var (
	zipPath string
	domain  string
	url     string
	dbName  string
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate a WordPress site from a zip file",
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		if domain == "" && !dryRun {
			fmt.Fprintln(os.Stderr, "Error: domain is required when not in dry-run mode.")
			os.Exit(1)
		}

		wpPath, sqlPath, err := utils.InspectZipFile(zipPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting zip file: %v\n", err)
			os.Exit(1)
		}

		projectPath := "./" + domain

		if err := utils.SpinUpSite(domain, url, dbName, zipPath, wpPath, sqlPath, projectPath, dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "Error spinning up site: %v\n", err)
			os.Exit(1)
		}

		if dryRun {
			fmt.Println("\nDry run complete! No changes were made.")
		} else {
			fmt.Println("Migration complete!")
		}
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().StringVarP(&zipPath, "zip", "z", "", "Path to the zip file")
	migrateCmd.Flags().StringVarP(&domain, "domain", "d", "", "Domain for the new site")
	migrateCmd.Flags().StringVarP(&url, "url", "u", "", "URL for the new site")
	migrateCmd.Flags().StringVarP(&dbName, "dbname", "n", "", "Database name")
	migrateCmd.Flags().Bool("dry-run", false, "Perform a dry run without making any changes")

	migrateCmd.MarkFlagRequired("zip")
}

package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"ciwg-cli/internal/utils"

	"github.com/spf13/cobra"
)

var fetchDomainsCmd = &cobra.Command{
	Use:   "fetch-domains",
	Short: "Fetch domain info from CIWG API",
	Long:  `Fetch and display domain info from https://digital-ocean.ciwebgroup.com/domains.json using CIWG_DOMAIN_TOKEN`,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonFlag, _ := cmd.Flags().GetBool("json")
		if jsonFlag {
			body, err := utils.FetchDomainsRawJSON()
			if err != nil {
				return fmt.Errorf("failed to fetch domains: %w", err)
			}
			os.Stdout.Write(body)
			return nil
		}
		domains, err := utils.FetchDomainsFromAPI()
		if err != nil {
			return fmt.Errorf("failed to fetch domains: %w", err)
		}
		if len(domains) == 0 {
			fmt.Println("No domains found.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "DOMAIN\tWEBSITE\tSERVER\tIP")
		fmt.Fprintln(w, "------\t-------\t------\t--")
		for _, d := range domains {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Domain, d.Website, d.Server, d.IP)
		}
		w.Flush()
		fmt.Printf("\nTotal: %d domains\n", len(domains))
		return nil
	},
}

func init() {
	fetchDomainsCmd.Flags().Bool("json", false, "Output raw JSON response")
	rootCmd.AddCommand(fetchDomainsCmd)
}

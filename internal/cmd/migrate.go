package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv" // added
	"strings"
	"time"

	"ciwg-cli/internal/auth"

	"github.com/joho/godotenv"
	"github.com/maniartech/gotime"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type migratePlanEntry struct {
	From       string `json:"from" yaml:"from"`
	To         string `json:"to" yaml:"to"`
	DelayUntil string `json:"delayUntil,omitempty" yaml:"delayUntil,omitempty"`
}

type migratePlan map[string]migratePlanEntry

// splitHostPath splits an input like "user@host:/path" or "host:/path" or "host" or "local:/custom"
// into hostPart (e.g. "user@host" or "host" or "local") and pathPart (e.g. "/path" or "").
func splitHostPath(input string) (hostPart, pathPart string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	// Find first ':' that indicates start of a path (avoid confusing with user@)
	// For typical inputs user@host:/path or host:/path this works.
	// IPv6 literal with colon is uncommon in these plans and not handled here.
	if idx := strings.Index(input, ":"); idx != -1 {
		return input[:idx], input[idx+1:]
	}
	return input, ""
}

// joinPathWithDomain returns a path that points to the domain directory.
// If base is empty defaultBase ("/var/opt") is used. If the provided base already
// ends with the domain name we return it as-is.
func joinPathWithDomain(base, domain string) string {
	defaultBase := "/var/opt"
	if base == "" {
		return filepath.Join(defaultBase, domain)
	}
	// If base starts with "~" expand (simple)
	if strings.HasPrefix(base, "~") {
		base = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(base, "~"))
	}
	cleanBase := filepath.Clean(base)
	if filepath.Base(cleanBase) == domain {
		return cleanBase
	}
	return filepath.Join(cleanBase, domain)
}

func newMigrateCmd() *cobra.Command {
	var (
		planFile             string
		sitesGlob            string
		targetServer         string
		dryRunFlag           bool
		archiveDir           string
		archiveWithTimestamp bool
		compressArchive      bool
		archiveCompression   string
		deleteAfter          bool
		forceDelete          bool
		globalDelay          string

		cfEmail string
		cfKey   string

		// new flag: activate the migrated site on the target
		activateMigrated bool

		// import SQL on the target after migration
		importSQL bool

		// local relay flags
		localRelay     bool
		localRelayPath string

		// new flag: base destination path
		baseDestPath string
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate one or more sites between servers and update DNS",
		Long: `Migrate sites between servers (local or remote), update Cloudflare DNS, and optionally archive/delete the source.

Credentials (Cloudflare) should be supplied via a .env file or flags:
  CLOUDFLARE_EMAIL and CLOUDFLARE_API_KEY (preferred via .env) or --cf-email / --cf-key.

Plan files (JSON/YAML) map domains to from/to hosts and optional delayUntil values.
If --plan is not provided, use --sites and --target to migrate matching directories under /var/opt.`,
		Example: `
# Simple: migrate a single domain from local to target (dry run)
ciwg-cli migrate --sites example.com --target wp18.ciwgserver.com --dry-run

# Plan file (JSON) example (plan.json):
{
  "example.com": {
    "from": "local",
    "to": "wp18.ciwgserver.com",
    "delayUntil": "in 2h"
  },
  "blog.example.net": {
    "from": "user@old.server.com",
    "to": "root@new.server.com",
    "delayUntil": "2025-09-01T10:00:00Z"
  }
}

# Plan file (YAML) example (plan.yaml):
example.com:
  from: local
  to: wp18.ciwgserver.com
  delayUntil: "in 2h"
blog.example.net:
  from: user@old.server.com
  to: root@new.server.com

# Use a plan file (dry run)
ciwg-cli migrate --plan ./plan.json --dry-run

# Archive then delete after successful migration (prompted)
ciwg-cli migrate --plan ./plan.yaml --archive-dir /var/backups/migrated --archive-with-timestamp --compress-archive --archive-compression xz --delete

# Force delete without prompt
ciwg-cli migrate --plan ./plan.yaml --force-delete

# Apply a global fuzzy delay to all plan entries that lack delayUntil
ciwg-cli migrate --plan ./plan.json --set-global-delay "in 30m"

# Pass Cloudflare credentials via flags (overrides .env)
ciwg-cli migrate --plan ./plan.json --cf-email admin@example.com --cf-key your_api_key_here

# SSH options (applies to rsync/ssh connections)
ciwg-cli migrate --plan ./plan.json -u root -p 2222 -k ~/.ssh/id_ed25519

# Use a custom base destination path on the target server
ciwg-cli migrate --sites example.com --target wp18.ciwgserver.com --base-dest-path /home/sites

Notes:
 - 'from' and 'to' may be 'local' or a remote host optionally prefixed with user@ (e.g. user@host.example.com).
 - When migrating from a remote host, the tool stages files to /tmp/ciwg_migrate/<domain> before shipping to the target.
 - Use --dry-run to verify actions before making changes.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = godotenv.Load()

			if cfEmail == "" {
				cfEmail = os.Getenv("CLOUDFLARE_EMAIL")
			}
			if cfKey == "" {
				cfKey = os.Getenv("CLOUDFLARE_API_KEY")
			}

			if planFile == "" && (sitesGlob == "" || targetServer == "") {
				return errors.New("provide --plan or both --sites and --target")
			}

			// Build execution plan
			plan, err := buildPlan(planFile, sitesGlob, targetServer)
			if err != nil {
				return err
			}
			// Apply global delay if set
			if globalDelay != "" {
				for k, v := range plan {
					if v.DelayUntil == "" {
						v.DelayUntil = globalDelay
						plan[k] = v
					}
				}
			}

			// SSH params to build rsync -e "ssh ..." consistently with flags
			user, port, keyPath := getSSHParams(cmd)
			sshRsyncArg := buildRsyncSSHArg(user, port, keyPath)

			for domain, entry := range plan {
				// Delay scheduling
				if entry.DelayUntil != "" {
					when, err := parseFuzzyTime(entry.DelayUntil)
					if err == nil && time.Now().Before(when) {
						fmt.Fprintf(os.Stderr, "[INFO] Skipping %s: scheduled for %s\n", domain, when)
						continue
					}
				}

				sourceRaw := strings.TrimSpace(entry.From)
				targetRaw := strings.TrimSpace(entry.To)
				if targetRaw == "" {
					targetRaw = targetServer
				}
				if sourceRaw == "" {
					sourceRaw = "local"
				}

				// Split host and optional path for both source and target.
				srcHostPart, srcPathPart := splitHostPath(sourceRaw)
				tgtHostPart, tgtPathPart := splitHostPath(targetRaw)

				// Build sourcePath and targetPath that point to the domain directory.
				sourcePath := joinPathWithDomain(srcPathPart, domain)
				targetPath := tgtPathPart
				// If no explicit target path provided in the plan, check the flag, then use default.
				if targetPath == "" {
					if baseDestPath != "" {
						targetPath = baseDestPath
					} else {
						targetPath = "/var/opt"
					}
				}

				fmt.Fprintf(os.Stderr, "[INFO] Migrating %s: %s -> %s\n", domain, sourceRaw, targetRaw)
				if dryRunFlag {
					newIP := lookupIPForHost(hostOnly(tgtHostPart))
					fmt.Fprintf(os.Stderr, "[DRY RUN] Would dump DB for %s\n", domain)

					// Be explicit about relay vs direct copy
					if localRelay {
						relayDir := filepath.Join(localRelayPath, domain)
						if isLocal(srcHostPart) {
							fmt.Fprintf(os.Stderr, "[DRY RUN] Would rsync local %s/ to relay %s (ssh: <local>)\n", sourcePath, relayDir)
						} else {
							fmt.Fprintf(os.Stderr, "[DRY RUN] Would rsync %s:%s/ to relay %s (ssh: %s)\n", srcHostPart, sourcePath, relayDir, sshRsyncArg)
						}
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would rsync relay %s to %s:%s (ssh: %s)\n", relayDir, tgtHostPart, targetPath, sshRsyncArg)
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would remove relay directory %s after successful transfer\n", relayDir)
					} else {
						// one-hop
						srcSpec := fmt.Sprintf("%s:%s", srcHostPart, sourcePath)
						destSpec := fmt.Sprintf("%s:%s", tgtHostPart, targetPath)
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would rsync %s to %s (ssh: %s)\n", srcSpec, destSpec, sshRsyncArg)
					}

					if cfEmail != "" && cfKey != "" && newIP != "" {
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would update Cloudflare A record for %s -> %s\n", domain, newIP)
					}
					if activateMigrated {
						siteDir := filepath.Join(targetPath, domain)
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would SSH to %s and run: cd %s && docker compose up -d\n", tgtHostPart, siteDir)
						wpMgrDir := filepath.Join(targetPath, "wordpress-manager")
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would SSH to %s and run: cd %s && docker compose down && docker compose up -d\n", tgtHostPart, wpMgrDir)
					}
					if importSQL {
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would import latest SQL file for %s on target %s\n", domain, tgtHostPart)
					}
					if archiveDir != "" {
						fmt.Fprintf(os.Stderr, "[DRY RUN] Would archive source directory of %s to %s (ts:%v, compress:%v:%s)\n", domain, archiveDir, archiveWithTimestamp, compressArchive, archiveCompression)
					}
					if deleteAfter {
						if forceDelete {
							fmt.Fprintf(os.Stderr, "[DRY RUN] Would force-delete source directory for %s\n", domain)
						} else {
							fmt.Fprintf(os.Stderr, "[DRY RUN] Would prompt to delete source directory for %s\n", domain)
						}
					}
					continue
				}

				// Resolve source connection & existence
				var sourceIsLocal bool
				var sourceClient *auth.SSHClient

				if !isLocal(srcHostPart) {
					var err error
					sourceClient, err = createSSHClient(cmd, srcHostPart)
					if err != nil {
						return fmt.Errorf("connect to source %s: %w", srcHostPart, err)
					}
					defer sourceClient.Close()

					// Verify directory exists remotely
					if _, _, err := sourceClient.ExecuteCommand(fmt.Sprintf("test -d %q", sourcePath)); err != nil {
						fmt.Fprintf(os.Stderr, "[WARN] %s: not found on source %s\n", sourcePath, srcHostPart)
						continue
					}
				} else {
					sourceIsLocal = true
					if st, err := os.Stat(sourcePath); err != nil || !st.IsDir() {
						fmt.Fprintf(os.Stderr, "[WARN] %s: not found locally\n", sourcePath)
						continue
					}
				}

				// Dump database at source using existing helpers (wp db export -> *.sql in wp-content)
				if sourceIsLocal {
					if err := dumpDBLocal(sourcePath); err != nil {
						fmt.Fprintf(os.Stderr, "[WARN] DB dump failed locally for %s: %v\n", domain, err)
					}
				} else {
					if err := dumpDBRemote(sourceClient, sourcePath); err != nil {
						fmt.Fprintf(os.Stderr, "[WARN] DB dump failed on %s for %s: %v\n", srcHostPart, domain, err)
					}
				}

				// Stage directory for transfer if source is remote (rsync remote->local), else use local path directly
				localStage := sourcePath
				needsCleanup := false

				if localRelay {
					// Always stage to relay path when enabled
					localStage = filepath.Join(localRelayPath, domain)
					if err := os.MkdirAll(localStage, 0755); err != nil {
						return fmt.Errorf("create relay staging: %w", err)
					}
					if sourceIsLocal {
						if err := runRsync(sourcePath+"/", localStage, ""); err != nil {
							return fmt.Errorf("rsync local -> relay failed: %w", err)
						}
					} else {
						srcSpec := fmt.Sprintf("%s:%s/", srcHostPart, sourcePath)
						if err := runRsync(srcSpec, localStage, sshRsyncArg); err != nil {
							return fmt.Errorf("rsync remote -> relay failed: %w", err)
						}
					}
					needsCleanup = true
				} else {
					// previous behavior: only stage when source is remote
					if !sourceIsLocal {
						localStage = filepath.Join("/tmp", "ciwg_migrate", domain)
						if err := os.MkdirAll(localStage, 0755); err != nil {
							return fmt.Errorf("create staging: %w", err)
						}
						srcSpec := fmt.Sprintf("%s:%s/", srcHostPart, sourcePath)
						if err := runRsync(srcSpec, localStage, sshRsyncArg); err != nil {
							return fmt.Errorf("rsync from source %s failed: %w", srcHostPart, err)
						}
						needsCleanup = true
					}
				}

				// Ensure target SSH connectivity (pass only host part to createSSHClient)
				targetConn := tgtHostPart
				if targetConn == "" {
					return fmt.Errorf("invalid target host for %q", targetRaw)
				}
				targetClient, err := createSSHClient(cmd, targetConn)
				if err != nil {
					return fmt.Errorf("connect to target %s: %w", targetConn, err)
				}
				defer targetClient.Close()

				// Rsync localStage to target:path
				// destSpec uses tgtHostPart (may include user@)
				destSpec := fmt.Sprintf("%s:%s", tgtHostPart, targetPath)

				// CRITICAL: Ensure the source for the final hop does NOT have a trailing slash.
				// This prevents rsync from wiping the target directory.
				// We are copying the directory *itself* into the target path.
				finalStageSource := strings.TrimSuffix(localStage, "/")

				if err := runRsync(finalStageSource, destSpec, sshRsyncArg); err != nil {
					return fmt.Errorf("rsync to target %s failed: %w", tgtHostPart, err)
				}

				// Cleanup staging/relay after successful second hop
				if needsCleanup {
					if err := os.RemoveAll(localStage); err != nil {
						fmt.Fprintf(os.Stderr, "[WARN] failed to remove staging dir %s: %v\n", localStage, err)
					} else {
						fmt.Fprintf(os.Stderr, "[INFO] removed staging dir %s\n", localStage)
					}
				}

				// Cloudflare DNS update
				if cfEmail != "" && cfKey != "" {
					newIP := lookupIPForHost(hostOnly(tgtHostPart))
					if newIP != "" {
						if err := cfUpdateARecord(domain, newIP, cfEmail, cfKey); err != nil {
							fmt.Fprintf(os.Stderr, "[WARN] DNS update failed for %s: %v\n", domain, err)
						} else {
							fmt.Fprintf(os.Stderr, "[INFO] DNS updated for %s -> %s\n", domain, newIP)
						}
					} else {
						fmt.Fprintf(os.Stderr, "[WARN] Could not resolve IP for %s; skipping DNS update\n", tgtHostPart)
					}
				} else {
					fmt.Fprintf(os.Stderr, "[INFO] Cloudflare credentials missing; skipping DNS update for %s\n", domain)
				}

				// Activate migrated site on target if requested
				if activateMigrated {
					siteDir := filepath.Join(targetPath, domain)
					wpMgrDir := filepath.Join(targetPath, "wordpress-manager")

					// If target is local, run commands locally; otherwise run over targetClient SSH
					if isLocal(tgtHostPart) {
						// local: run docker compose up -d in site dir
						cmdRun := exec.Command("docker", "compose", "up", "-d")
						cmdRun.Dir = siteDir
						cmdRun.Stdout = os.Stdout
						cmdRun.Stderr = os.Stderr
						if err := cmdRun.Run(); err != nil {
							fmt.Fprintf(os.Stderr, "[WARN] local activation failed for %s: %v\n", siteDir, err)
						} else {
							fmt.Fprintf(os.Stderr, "[INFO] Activated site locally: %s\n", siteDir)
						}

						// local: restart wordpress-manager
						cmdDown := exec.Command("docker", "compose", "down")
						cmdDown.Dir = wpMgrDir
						cmdDown.Stdout = os.Stdout
						cmdDown.Stderr = os.Stderr
						_ = cmdDown.Run() // ignore error for down; we'll attempt up after
						cmdUp := exec.Command("docker", "compose", "up", "-d")
						cmdUp.Dir = wpMgrDir
						cmdUp.Stdout = os.Stdout
						cmdUp.Stderr = os.Stderr
						if err := cmdUp.Run(); err != nil {
							fmt.Fprintf(os.Stderr, "[WARN] local wordpress-manager restart failed for %s: %v\n", wpMgrDir, err)
						} else {
							fmt.Fprintf(os.Stderr, "[INFO] Restarted wordpress-manager locally: %s\n", wpMgrDir)
						}
					} else {
						// remote: run via targetClient (we created targetClient earlier)
						// ensure targetClient is available
						if targetClient == nil {
							// attempt to create connection if missing
							tc, err := createSSHClient(cmd, tgtHostPart)
							if err != nil {
								fmt.Fprintf(os.Stderr, "[WARN] could not connect to target %s to activate site: %v\n", tgtHostPart, err)
							} else {
								targetClient = tc
								defer targetClient.Close()
							}
						}
						if targetClient != nil {
							// run docker compose up -d in site dir
							cmd1 := fmt.Sprintf("cd %q && docker compose up -d", siteDir)
							if out, stderr, err := targetClient.ExecuteCommand(cmd1); err != nil {
								fmt.Fprintf(os.Stderr, "[WARN] remote activation failed for %s on %s: %v (stderr: %s, out: %s)\n", siteDir, tgtHostPart, err, stderr, out)
							} else {
								fmt.Fprintf(os.Stderr, "[INFO] Activated site on %s: %s\n", tgtHostPart, siteDir)
							}

							// restart wordpress-manager
							cmd2 := fmt.Sprintf("cd %q && docker compose down && docker compose up -d", wpMgrDir)
							if out, stderr, err := targetClient.ExecuteCommand(cmd2); err != nil {
								fmt.Fprintf(os.Stderr, "[WARN] remote wordpress-manager restart failed on %s: %v (stderr: %s, out: %s)\n", tgtHostPart, err, stderr, out)
							} else {
								fmt.Fprintf(os.Stderr, "[INFO] Restarted wordpress-manager on %s: %s\n", tgtHostPart, wpMgrDir)
							}
						}
					}
				}

				// Attempt DB import on target (remote or local)
				// Run only when not in dry-run (the dry-run message is printed above)
				if importSQL && !dryRunFlag {
					sqlNameCmd := fmt.Sprintf("ls -1t %s/wp-content/*.sql 2>/dev/null | head -n1", filepath.Join(targetPath, domain))
					if isLocal(tgtHostPart) {
						// local import
						sqlFile, _ := runLocalCommand(sqlNameCmd)
						sqlFile = strings.TrimSpace(sqlFile)
						if sqlFile != "" {
							// Find WP container name
							container, _ := getContainerNameLocal(filepath.Join(targetPath, domain, "docker-compose.yml"))
							cmd := exec.Command("docker", "exec", container, "sh", "-c", fmt.Sprintf("cd /var/www/html/wp-content && wp db import %q --allow-root", sqlFile))
							cmd.Stdout = os.Stdout
							cmd.Stderr = os.Stderr
							_ = cmd.Run()
						}
					} else {
						// remote import via SSH client
						sqlFileCmdRemote := fmt.Sprintf("ls -1t %s/wp-content/*.sql 2>/dev/null | head -n1", filepath.Join(targetPath, domain))
						sqlFile, _, _ := targetClient.ExecuteCommand(sqlFileCmdRemote)
						sqlFile = strings.TrimSpace(sqlFile)
						if sqlFile != "" {
							// determine container name remotely
							container, _ := getContainerName(targetClient, filepath.Join(targetPath, domain, "docker-compose.yml"))
							importCmd := fmt.Sprintf("docker exec %s sh -c 'cd /var/www/html/wp-content && wp db import %q --allow-root'", container, sqlFile)
							if out, stderr, err := targetClient.ExecuteCommand(importCmd); err != nil {
								fmt.Fprintf(os.Stderr, "[WARN] DB import failed: %v stderr:%s out:%s\n", err, stderr, out)
							} else {
								fmt.Fprintf(os.Stderr, "[INFO] DB import completed: %s\n", sqlFile)
							}
						}
					}
				}

				// Optional archive of source
				if archiveDir != "" {
					if sourceIsLocal {
						if err := archiveSiteLocal(sourcePath, archiveDir, archiveWithTimestamp, compressArchive, archiveCompression); err != nil {
							fmt.Fprintf(os.Stderr, "[WARN] Archive(local) failed for %s: %v\n", domain, err)
						}
					} else {
						if err := archiveSiteRemote(sourceClient, sourcePath, archiveDir, archiveWithTimestamp, compressArchive, archiveCompression); err != nil {
							fmt.Fprintf(os.Stderr, "[WARN] Archive(remote) failed for %s: %v\n", domain, err)
						}
					}
				}

				// Optional delete of source
				if deleteAfter {
					ok := forceDelete
					if !forceDelete {
						ok = promptConfirm(fmt.Sprintf("Delete source directory %s on %s? [y/N]: ", sourcePath, srcHostPart))
					}
					if ok {
						if sourceIsLocal {
							if err := os.RemoveAll(sourcePath); err != nil {
								fmt.Fprintf(os.Stderr, "[WARN] Delete(local) failed for %s: %v\n", sourcePath, err)
							}
						} else {
							if _, stderr, err := sourceClient.ExecuteCommand(fmt.Sprintf("rm -rf %q", sourcePath)); err != nil {
								fmt.Fprintf(os.Stderr, "[WARN] Delete(remote) failed for %s: %v (stderr: %s)\n", sourcePath, err, stderr)
							}
						}
					}
				}

				// Cleanup staging
				if needsCleanup {
					_ = os.RemoveAll(localStage)
				}

				fmt.Fprintf(os.Stderr, "[INFO] Migration finished for %s. Next on %s: cd %s && docker compose up -d\n", domain, hostOnly(tgtHostPart), filepath.Join(targetPath, domain))
			}

			return nil
		},
	}

	// Inputs
	cmd.Flags().StringVar(&planFile, "plan", "", "Path to JSON/YAML plan file")
	cmd.Flags().StringVar(&sitesGlob, "sites", "", "Glob for sites under /var/opt (e.g., [a-c]*.com)")
	cmd.Flags().StringVar(&targetServer, "target", "", "Target server hostname or IP (if --plan not provided)")

	// Behavior
	cmd.Flags().BoolVar(&dryRunFlag, "dry-run", false, "Show actions without executing")
	cmd.Flags().StringVar(&archiveDir, "archive-dir", "", "Move the source directory to this directory after migration")
	cmd.Flags().BoolVar(&archiveWithTimestamp, "archive-with-timestamp", false, "Append timestamp to archived directory name")
	cmd.Flags().BoolVar(&compressArchive, "compress-archive", false, "Create a compressed tar archive in the archive dir")
	cmd.Flags().StringVar(&archiveCompression, "archive-compression-type", "xz", "Compression type (xz, gzip/gz)")
	cmd.Flags().BoolVar(&deleteAfter, "delete", false, "Prompt to delete the source after migration")
	cmd.Flags().BoolVar(&forceDelete, "force-delete", false, "Delete source without prompt")
	cmd.Flags().StringVar(&globalDelay, "set-global-delay", "", "Apply fuzzy timestamp delay to all entries missing delayUntil")

	// Cloudflare override (env is default)
	cmd.Flags().StringVar(&cfEmail, "cf-email", "", "Cloudflare email (default from CLOUDFLARE_EMAIL)")
	cmd.Flags().StringVar(&cfKey, "cf-key", "", "Cloudflare API key (default from CLOUDFLARE_API_KEY)")

	// SSH flags (align with other commands)
	cmd.Flags().StringP("user", "u", "", "SSH username (default: current user or 'root')")
	cmd.Flags().StringP("port", "p", "22", "SSH port")
	cmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	cmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	cmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
	cmd.Flags().CountVarP(&verboseCount, "verbose", "v", "Set verbosity level")

	// activation flag registration
	cmd.Flags().BoolVar(&activateMigrated, "activate-migrated-site", false, "After migration and DNS update, activate the migrated site on the target (runs docker compose commands)")

	// import SQL flag
	cmd.Flags().BoolVar(&importSQL, "import-sql", false, "After migration, detect latest exported .sql in wp-content and import it into WordPress on the target")

	// local-relay flags
	cmd.Flags().BoolVar(&localRelay, "local-relay", false, "Stage the site locally first (rsync -> local-relay-path) then rsync from relay to target")
	cmd.Flags().StringVar(&localRelayPath, "local-relay-path", "/tmp/ciwg_migrate", "Path for local relay staging (used when --local-relay is set)")

	// base destination path flag
	cmd.Flags().StringVar(&baseDestPath, "base-dest-path", "/var/opt", "Base destination path on the target server (defaults to /var/opt)")

	return cmd
}

func init() {
	rootCmd.AddCommand(newMigrateCmd())
}

// Helpers

func buildPlan(planPath, sitesGlob, target string) (migratePlan, error) {
	if planPath != "" {
		return parsePlan(planPath)
	}
	plan := make(migratePlan)
	// Expand domains under /var/opt according to glob
	pattern := filepath.Join("/var/opt", sitesGlob)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob error: %w", err)
	}
	if len(matches) == 0 {
		// treat as direct domain
		plan[sitesGlob] = migratePlanEntry{From: "local", To: target}
		return plan, nil
	}
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			plan[filepath.Base(m)] = migratePlanEntry{From: "local", To: target}
		}
	}
	return plan, nil
}

func parsePlan(path string) (migratePlan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	plan := make(migratePlan)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if err := json.Unmarshal(b, &plan); err != nil {
			return nil, err
		}
	default:
		if err := yaml.Unmarshal(b, &plan); err != nil {
			// try JSON fallback
			if err2 := json.Unmarshal(b, &plan); err2 != nil {
				return nil, err
			}
		}
	}
	return plan, nil
}

func parseFuzzyTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		panic(fmt.Errorf("unable to parse time: empty string"))
	}
	now := time.Now()

	// 1) Try gotime (fuzzy parsing relative to now)
	if t, err := gotime.Parse(s, time.Now().Format(time.RFC3339)); err == nil && !t.IsZero() {
		return t.UTC(), nil
	}

	lower := strings.ToLower(s)

	// 2) Try duration styles: "in 15m", "+2h", "-30m", "15m", "now+15m"
	if strings.HasPrefix(lower, "in ") {
		if d, err := time.ParseDuration(strings.TrimSpace(lower[3:])); err == nil {
			return now.Add(d).UTC(), nil
		}
	}
	if strings.HasPrefix(lower, "now+") {
		if d, err := time.ParseDuration(strings.TrimPrefix(lower, "now+")); err == nil {
			return now.Add(d).UTC(), nil
		}
	}
	if strings.HasPrefix(lower, "now-") {
		if d, err := time.ParseDuration(strings.TrimPrefix(lower, "now-")); err == nil {
			return now.Add(-d).UTC(), nil
		}
	}
	if strings.HasPrefix(lower, "+") || strings.HasPrefix(lower, "-") {
		if d, err := time.ParseDuration(lower); err == nil {
			return now.Add(d).UTC(), nil
		}
	}
	if d, err := time.ParseDuration(lower); err == nil {
		return now.Add(d).UTC(), nil
	}

	// 3) Try epoch seconds/milliseconds
	if isAllDigits(s) {
		// ms (>=13 digits)
		if len(s) >= 13 {
			if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
				return time.Unix(0, ms*int64(time.Millisecond)).UTC(), nil
			}
		}
		// seconds (>=10 digits)
		if len(s) >= 10 {
			if sec, err := strconv.ParseInt(s[:10], 10, 64); err == nil {
				return time.Unix(sec, 0).UTC(), nil
			}
		}
	}

	// 4) Try a set of common time layouts
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		time.RFC850,
		time.ANSIC,
		time.UnixDate,
		time.RubyDate,
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"02 Jan 2006 15:04:05 MST",
		"02 Jan 2006 15:04 MST",
		"02 Jan 2006",
		"Jan 2 2006 15:04:05",
		"Jan 2 2006 3:04PM",
		"Jan 2 2006",
		"01/02/2006 15:04:05",
		"01/02/2006 15:04",
		"01/02/2006",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC(), nil
		}
	}

	// Last resort
	panic(fmt.Errorf("unable to parse time %q with gotime, duration, epoch, or known layouts", s))
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func isLocal(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "" || h == "local" || h == "localhost" || h == "127.0.0.1"
}

func hostOnly(target string) string {
	// strip optional user@
	if at := strings.LastIndex(target, "@"); at != -1 {
		return target[at+1:]
	}
	return target
}

func getSSHParams(cmd *cobra.Command) (user, port, key string) {
	user, _ = cmd.Flags().GetString("user")
	if user == "" {
		user = getCurrentUser() // from internal/cmd/ssh.go (defaults to "root")
	}
	port, _ = cmd.Flags().GetString("port")
	key, _ = cmd.Flags().GetString("key")
	return
}

func buildRsyncSSHArg(user, port, key string) string {
	var b strings.Builder
	b.WriteString("ssh")
	if user != "" {
		b.WriteString(" -l ")
		b.WriteString(user)
	}
	if port != "" && port != "22" {
		b.WriteString(" -p ")
		b.WriteString(port)
	}
	if key != "" {
		b.WriteString(" -i ")
		b.WriteString(key)
	}
	return b.String()
}

func runRsync(src, dst, sshCmd string) error {
	args := []string{"-azv", "--delete"}

	// Skip archives and backups
	excludePatterns := []string{
		"*.tar", "*.tar.gz", "*.tgz", "*.tar.xz", "*.txz", "*.tar.bz2", "*.tbz2",
		"*.zip",
		"*backup*", "*Backup*", "*BACKUP*",
	}
	for _, pat := range excludePatterns {
		args = append(args, "--exclude", pat)
	}

	if sshCmd != "" {
		args = append(args, "-e", sshCmd)
	}

	// DO NOT automatically add a trailing slash. The caller must decide.
	// This was the source of the destructive bug.
	// if fi, err := os.Stat(src); err == nil && fi.IsDir() && !strings.HasSuffix(src, "/") && !strings.Contains(src, ":") {
	// 	src = src + "/"
	// }

	args = append(args, src, dst)
	c := exec.Command("rsync", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// DB dump helpers using existing code paths from domains.go

func dumpDBLocal(sitePath string) error {
	compose := filepath.Join(sitePath, "docker-compose.yml")
	if _, err := os.Stat(compose); err != nil {
		return fmt.Errorf("compose not found: %s", compose)
	}
	container, err := getContainerNameLocal(compose)
	if err != nil {
		return err
	}
	// This writes *.sql in wp-content mapped to host volume
	return exportDatabaseLocal(container)
}

func dumpDBRemote(client *auth.SSHClient, sitePath string) error {
	compose := filepath.Join(sitePath, "docker-compose.yml")
	// read remote compose to find container
	cmd := fmt.Sprintf("cat %q", compose)
	stdout, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("read remote compose failed: %v (stderr: %s)", err, stderr)
	}
	var tmpFile string
	tmpFile, err = writeTemp(stdout)
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	container, err := getContainerName(client, compose) // uses remote parsing too
	if err != nil {
		return err
	}
	return exportDatabase(client, container)
}

func writeTemp(data string) (string, error) {
	f, err := os.CreateTemp("", "compose-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// Cloudflare

func cfUpdateARecord(domain, ip, email, apiKey string) error {
	zoneID, err := getCloudflareZoneID(domain, email, apiKey)
	if err != nil || zoneID == "" {
		return fmt.Errorf("cloudflare zone id for %s: %w", domain, err)
	}
	recID, err := cfGetARecordID(zoneID, domain, email, apiKey)
	if err != nil || recID == "" {
		return fmt.Errorf("cloudflare A record id for %s: %w", domain, err)
	}
	return cfPutARecord(zoneID, recID, domain, ip, email, apiKey)
}

func cfGetARecordID(zoneID, domain, email, apiKey string) (string, error) {
	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A&name=%s", zoneID, domain)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Auth-Email", email)
	req.Header.Set("X-Auth-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var r struct {
		Success bool `json:"success"`
		Result  []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	if !r.Success || len(r.Result) == 0 {
		return "", fmt.Errorf("no A record for %s", domain)
	}
	return r.Result[0].ID, nil
}

func cfPutARecord(zoneID, recID, domain, ip, email, apiKey string) error {
	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", zoneID, recID)
	payload := fmt.Sprintf(`{"type":"A","name":"%s","content":"%s","ttl":120,"proxied":true}`, domain, ip)
	req, err := http.NewRequest("PUT", apiURL, bytes.NewBufferString(payload))
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Email", email)
	req.Header.Set("X-Auth-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return err
	}
	if !r.Success {
		return fmt.Errorf("cloudflare update failed: %s", string(body))
	}
	return nil
}

func lookupIPForHost(host string) string {
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		// fallback to dig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, "sh", "-c", "dig +short "+host).Output()
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 {
			return strings.TrimSpace(lines[0])
		}
		return ""
	}
	return ips[0]
}

// Archiving

func archiveSiteLocal(sitePath, archiveDir string, withTS, compress bool, compression string) error {
	base := filepath.Base(sitePath)
	name := base
	if withTS {
		name = fmt.Sprintf("%s-%s", base, time.Now().Format("20060102-150405"))
	}
	dst := filepath.Join(archiveDir, name)

	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return err
	}
	// Move
	if err := os.Rename(sitePath, dst); err != nil {
		// fallback: copy via rsync then delete
		if err := runRsync(sitePath+"/", dst, ""); err != nil {
			return err
		}
		if err := os.RemoveAll(sitePath); err != nil {
			return err
		}
	}
	if compress {
		return localTar(archiveDir, name, compression)
	}
	return nil
}

func archiveSiteRemote(client *auth.SSHClient, sitePath, archiveDir string, withTS, compress bool, compression string) error {
	base := filepath.Base(sitePath)
	name := base
	if withTS {
		name = fmt.Sprintf("%s-%s", base, time.Now().Format("20060102-150405"))
	}
	dst := filepath.Join(archiveDir, name)

	// mkdir + mv
	cmd := fmt.Sprintf("mkdir -p %q && mv %q %q", archiveDir, sitePath, dst)
	if _, stderr, err := client.ExecuteCommand(cmd); err != nil {
		return fmt.Errorf("remote move failed: %v (stderr: %s)", err, stderr)
	}
	if compress {
		return remoteTar(client, archiveDir, name, compression)
	}
	return nil
}

func localTar(parent, name, compression string) error {
	archive := name + ".tar." + normalizeCompExt(compression)
	var c *exec.Cmd
	switch normalizeComp(compression) {
	case "xz":
		c = exec.Command("tar", "cJf", filepath.Join(parent, archive), "-C", parent, name)
	case "gz":
		c = exec.Command("tar", "czf", filepath.Join(parent, archive), "-C", parent, name)
	default:
		return fmt.Errorf("unsupported compression: %s", compression)
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func remoteTar(client *auth.SSHClient, parent, name, compression string) error {
	archive := name + ".tar." + normalizeCompExt(compression)
	var tarFlags string
	switch normalizeComp(compression) {
	case "xz":
		tarFlags = "cJf"
	case "gz":
		tarFlags = "czf"
	default:
		return fmt.Errorf("unsupported compression: %s", compression)
	}
	cmd := fmt.Sprintf("tar -%s %q -C %q %q", tarFlags, filepath.Join(parent, archive), parent, name)
	if _, stderr, err := client.ExecuteCommand(cmd); err != nil {
		return fmt.Errorf("remote tar failed: %v (stderr: %s)", err, stderr)
	}
	return nil
}

func normalizeComp(s string) string {
	s = strings.ToLower(s)
	switch s {
	case "gz", "gzip":
		return "gz"
	case "xz":
		return "xz"
	default:
		return s
	}
}
func normalizeCompExt(s string) string {
	switch normalizeComp(s) {
	case "gz":
		return "gz"
	case "xz":
		return "xz"
	default:
		return s
	}
}

func promptConfirm(msg string) bool {
	fmt.Fprint(os.Stderr, msg)
	// read from stdin actually
	rin := bufio.NewReader(os.Stdin)
	resp, _ := rin.ReadString('\n')
	resp = strings.ToLower(strings.TrimSpace(resp))
	return resp == "y" || resp == "yes"
}

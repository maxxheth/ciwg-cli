package cmd

import (
	"bufio"
	"crypto/rand"     // added
	"encoding/base64" // added
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv" // added
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	purgeEmailPattern   string
	purgeEmailListFile  string
	purgeExclude        string
	purgeExcludeEmails  string
	purgeInclude        string // new
	purgeIncludeEmails  string // new
	purgeIDs            string // NEW: comma-separated user IDs to target
	purgeUsernames      string // NEW: comma-separated usernames to target (case-insensitive)
	purgeDisplayNames   string // NEW: comma-separated display names to target (case-insensitive exact match)
	purgeDryRun         bool
	purgeLargeOutputs   bool   // new
	purgeDelete         bool   // NEW: actually delete matched users
	purgeSkipConfirm    bool   // NEW: skip interactive confirmation
	purgeSetRole        string // NEW: set role (or level) for matched (source) users after reassignment
	purgeUpdatePassword string // NEW: update (or randomize) password for matched source users
)

var purgeUsersCmd = &cobra.Command{
	Use:   "purge-users [server] [--email-pattern=PATTERN | --email-list=FILE] [flags] -- <wp user create args>",
	Short: "Reassign posts to a new user and delete old users across containers / servers",
	Long: `Creates (or finds) a target user in each WordPress container, reassigns posts
from matching users (by email pattern or list), backs up the DB, then deletes old users.
Supports local or remote servers (via --server-range and SSH flags).
You may specify a single target server as the first positional argument (before --). Example:
  purge-users wp3.example.com --email-pattern='@old.com' -- newowner newowner@new.com --role=editor
If no server is given, --server-range is used (default: local).`,
	RunE:                  runPurgeUsers,
	DisableFlagsInUseLine: true,
}

func init() {
	rootCmd.AddCommand(purgeUsersCmd)

	// Selection flags
	purgeUsersCmd.Flags().StringVar(&purgeEmailPattern, "email-pattern", "", "Email pattern to match (substr match, e.g. '@old-domain.com')")
	purgeUsersCmd.Flags().StringVar(&purgeEmailListFile, "email-list", "", "File with one entry per line: email, user ID, username, or display name")
	purgeUsersCmd.Flags().StringVar(&purgeExclude, "exclude", "", "Comma-separated container names to exclude")
	purgeUsersCmd.Flags().StringVar(&purgeExcludeEmails, "exclude-email", "", "Comma-separated emails to exclude from deletion")
	purgeUsersCmd.Flags().StringVar(&purgeInclude, "include", "", "Comma-separated container names to include (if set, only these are processed)")
	purgeUsersCmd.Flags().StringVar(&purgeIncludeEmails, "include-email", "", "Comma-separated emails to include (if set, only these are considered)")
	purgeUsersCmd.Flags().BoolVar(&purgeDryRun, "dry-run", false, "Show actions without making changes")
	purgeUsersCmd.Flags().BoolVar(&purgeLargeOutputs, "large-outputs", false, "Optimize for large outputs (stream locally, use pipes)")
	purgeUsersCmd.Flags().BoolVar(&purgeDelete, "delete", false, "Delete matched users after reassignment (requires confirmation)")
	purgeUsersCmd.Flags().BoolVar(&purgeSkipConfirm, "skip-confirmation", false, "Skip confirmation prompt when --delete is used")

	purgeUsersCmd.Flags().StringVar(&purgeIDs, "ids", "", "Comma-separated user IDs to target")
	purgeUsersCmd.Flags().StringVar(&purgeUsernames, "usernames", "", "Comma-separated usernames to target (case-insensitive)")
	purgeUsersCmd.Flags().StringVar(&purgeDisplayNames, "display-names", "", "Comma-separated display names to target (case-insensitive exact match)")

	// Reuse server/SSH flags style from extract-users
	purgeUsersCmd.Flags().StringVarP(&serverRange, "server-range", "s", "local", "Server range (e.g., 'local', 'wp%d.ciwgserver.com:1-14')")
	purgeUsersCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	purgeUsersCmd.Flags().StringP("port", "p", "22", "SSH port")
	purgeUsersCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	purgeUsersCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	purgeUsersCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")

	purgeUsersCmd.Flags().StringVar(&purgeSetRole, "set-role", "", "Set a WordPress role (name or level number) for matched source users after post reassignment (before optional deletion)")
	purgeUsersCmd.Flags().StringVar(&purgeUpdatePassword, "update-password", "", "Update password for matched source users. If a value is provided it is used; if flag present with no value a random base64 password is generated per user.")
	// Allow --update-password with no explicit value
	purgeUsersCmd.Flags().Lookup("update-password").NoOptDefVal = "__AUTO__"
}

func runPurgeUsers(cmd *cobra.Command, args []string) error {
	// Interpret optional single server positional argument.
	// Heuristic: If there are at least 3 args and the third contains '@' (email),
	// treat first as server, second as login, third as email (remaining as create flags).
	// This avoids ambiguity with normal (login email) two-arg case.
	var serverOverride string
	if !cmd.Flags().Lookup("server-range").Changed {
		if len(args) >= 3 &&
			strings.Contains(args[2], "@") &&
			!strings.Contains(args[0], "@") &&
			!strings.Contains(args[1], "@") {
			serverOverride = args[0]
			args = args[1:]
		}
	}

	// Validation of selection method (now broader)
	if purgeEmailPattern == "" &&
		purgeEmailListFile == "" &&
		purgeIDs == "" &&
		purgeUsernames == "" &&
		purgeDisplayNames == "" {
		return errors.New("must provide at least one selector: --email-pattern | --email-list | --ids | --usernames | --display-names")
	}
	if purgeEmailPattern != "" && purgeEmailListFile != "" {
		return errors.New("cannot use both --email-pattern and --email-list")
	}
	// Remaining args are wp user create args.
	if len(args) < 2 {
		return errors.New("must provide wp user create arguments after '--' (at least: <login> <email>)")
	}

	createArgs := args

	// Build server list
	var servers []string
	if serverOverride != "" {
		servers = []string{serverOverride}
	} else {
		pattern, start, end, err := parseServerRange(serverRange)
		if err != nil {
			return fmt.Errorf("parse server range: %w", err)
		}
		if serverRange == "local" {
			servers = []string{"local"}
		} else {
			for i := start; i <= end; i++ {
				servers = append(servers, fmt.Sprintf(pattern, i))
			}
		}
	}

	fmt.Printf("Starting purge across %d server(s). Dry-run=%v\n", len(servers), purgeDryRun)

	// Build selection sets
	var excludeContainers = csvToSet(purgeExclude)
	var excludeEmails = csvToLowerSet(purgeExcludeEmails)
	var includeContainers = csvToSet(purgeInclude)
	var includeEmails = csvToLowerSet(purgeIncludeEmails)

	idSet := csvToIntSet(purgeIDs)
	usernameSet := csvToLowerSet(purgeUsernames)
	displayNameSet := csvToLowerSet(purgeDisplayNames)

	// Preload email list file (now: emails, IDs, usernames, or display names)
	emailList := map[string]struct{}{}
	if purgeEmailListFile != "" {
		f, err := os.Open(purgeEmailListFile)
		if err != nil {
			return fmt.Errorf("open selector list: %w", err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			raw := strings.TrimSpace(sc.Text())
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}

			// Heuristics:
			// 1. Pure integer -> user ID
			// 2. Contains '@' -> email
			// 3. Else -> treat as username and display name candidate
			if id, errConv := strconv.Atoi(raw); errConv == nil {
				idSet[id] = struct{}{}
				continue
			}
			lc := strings.ToLower(raw)
			if strings.Contains(raw, "@") {
				emailList[lc] = struct{}{}
				continue
			}
			// Username (login)
			usernameSet[lc] = struct{}{}
			// Display name (case-insensitive exact match)
			displayNameSet[lc] = struct{}{}
		}
		if err := sc.Err(); err != nil {
			return fmt.Errorf("read selector list: %w", err)
		}
	}

	// If after loading list we still have no selectors (defensive)
	if purgeEmailPattern == "" &&
		len(emailList) == 0 &&
		len(idSet) == 0 &&
		len(usernameSet) == 0 &&
		len(displayNameSet) == 0 {
		return errors.New("no valid selectors found (emails / ids / usernames / display names)")
	}

	for si, server := range servers {
		fmt.Printf("[%d/%d] Server: %s\n", si+1, len(servers), server)
		containers, err := listWPContainers(cmd, server)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: list containers: %v\n", err)
			continue
		}
		if len(containers) == 0 {
			fmt.Println("  No containers found.")
			continue
		}
		// Filter excludes
		filtered := make([]string, 0, len(containers))
		for _, c := range containers {
			if len(includeContainers) > 0 {
				if _, ok := includeContainers[c]; !ok {
					continue
				}
			}
			if _, skip := excludeContainers[c]; skip {
				continue
			}
			filtered = append(filtered, c)
		}
		if len(filtered) == 0 {
			fmt.Println("  All containers excluded.")
			continue
		}
		for _, container := range filtered {
			if err := processContainer(
				cmd, server, container, createArgs,
				emailList, excludeEmails, includeEmails,
				idSet, usernameSet, displayNameSet,
			); err != nil {
				fmt.Fprintf(os.Stderr, "  Container %s error: %v\n", container, err)
			}
		}
	}

	fmt.Println("Purge operation complete.")
	return nil
}

func processContainer(
	cmd *cobra.Command,
	server, container string,
	createArgs []string,
	emailList map[string]struct{},
	excludeEmails map[string]struct{},
	includeEmails map[string]struct{},
	idSet map[int]struct{},
	usernameSet map[string]struct{},
	displayNameSet map[string]struct{},
) error {
	fmt.Printf("  > Container: %s\n", container)

	// 1. Find users
	users, err := findTargetUsers(cmd, server, container, emailList, idSet, usernameSet, displayNameSet)
	if err != nil {
		return fmt.Errorf("find users: %w", err)
	}
	if len(users) == 0 {
		fmt.Println("    No matching users.")
		return nil
	}

	// Apply include-email filter first (narrowing)
	if len(includeEmails) > 0 {
		tmp := users[:0]
		for _, u := range users {
			if _, ok := includeEmails[strings.ToLower(u.Email)]; ok {
				tmp = append(tmp, u)
			}
		}
		users = tmp
		if len(users) == 0 {
			fmt.Println("    No users after include-email filtering.")
			return nil
		}
	}

	// Exclude emails
	filtered := users[:0]
	for _, u := range users {
		if _, skip := excludeEmails[strings.ToLower(u.Email)]; skip {
			fmt.Printf("    Excluding user %d (%s)\n", u.ID, u.Email)
			continue
		}
		filtered = append(filtered, u)
	}
	users = filtered
	if len(users) == 0 {
		fmt.Println("    No users after exclusion.")
		return nil
	}

	fmt.Printf("    Users to process: %d\n", len(users))

	// 2. Create (or get) target user
	newUserID, err := ensureTargetUser(cmd, server, container, createArgs)
	if err != nil {
		return fmt.Errorf("create target user: %w", err)
	}
	if newUserID == "" {
		return errors.New("could not determine target user ID")
	}

	// Build list excluding target user
	var sourceUsers []simpleUser
	for _, u := range users {
		if strconv.Itoa(u.ID) == newUserID {
			continue
		}
		sourceUsers = append(sourceUsers, u)
	}

	if len(sourceUsers) == 0 {
		fmt.Println("    No non-target users to process.")
		return nil
	}

	// 3. If NOT deleting, perform standalone reassignment (legacy behavior preserved)
	if !purgeDelete {
		for _, u := range sourceUsers {
			if err := reassignPosts(cmd, server, container, strconv.Itoa(u.ID), newUserID); err != nil {
				fmt.Fprintf(os.Stderr, "    Reassign posts (%d): %v\n", u.ID, err)
			}
		}
		// Optional role adjustment only makes sense if users remain
		if purgeSetRole != "" {
			roleSlug, level, err := normalizeRole(purgeSetRole)
			if err != nil {
				fmt.Fprintf(os.Stderr, "    Role normalization error: %v\n", err)
			} else {
				fmt.Printf("    Setting role '%s'%s for matched users (excluding target).\n", roleSlug, level)
				for _, u := range sourceUsers {
					if err := setUserRole(cmd, server, container, strconv.Itoa(u.ID), roleSlug); err != nil {
						fmt.Fprintf(os.Stderr, "      Set role user %d: %v\n", u.ID, err)
					}
				}
			}
		}
	}

	// 3b. Password updates (applies whether deleting or not)
	if f := cmd.Flags().Lookup("update-password"); f != nil && f.Changed {
		fmt.Println("    Updating passwords for matched source users...")
		for _, u := range sourceUsers {
			pass := purgeUpdatePassword
			if pass == "" || pass == "__AUTO__" {
				randPass, err := randomBase64Password()
				if err != nil {
					fmt.Fprintf(os.Stderr, "      Generate password user %d: %v\n", u.ID, err)
					continue
				}
				pass = randPass
			}
			if err := updateUserPassword(cmd, server, container, strconv.Itoa(u.ID), pass); err != nil {
				fmt.Fprintf(os.Stderr, "      Update password user %d: %v\n", u.ID, err)
				continue
			}
		}
	}

	// 4. Backup DB (now before any delete-with-reassign operations)
	if err := backupDatabase(cmd, server, container); err != nil {
		fmt.Fprintf(os.Stderr, "    DB backup warning: %v\n", err)
	}

	// 5. Deletion (with reassignment inline)
	if !purgeDelete {
		fmt.Printf("    Deletion skipped (use --delete to remove %d user(s)).\n", len(sourceUsers))
		return nil
	}

	if purgeDryRun {
		fmt.Printf("    [DRY RUN] Would delete %d user(s) with reassignment to %s: ", len(sourceUsers), newUserID)
		var ids []string
		for _, u := range sourceUsers {
			ids = append(ids, strconv.Itoa(u.ID))
		}
		fmt.Println(strings.Join(ids, ", "))
		return nil
	}

	if !purgeSkipConfirm {
		var ids []string
		for _, u := range sourceUsers {
			ids = append(ids, strconv.Itoa(u.ID))
		}
		fmt.Printf("    About to DELETE %d user(s) in container %s (reassign -> %s): %s\n",
			len(sourceUsers), container, newUserID, strings.Join(ids, ", "))
		fmt.Print("    Confirm delete? Type 'yes' to proceed: ")
		reader := bufio.NewReader(os.Stdin)
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "yes" {
			fmt.Println("    Deletion aborted by user.")
			return nil
		}
	}

	for _, u := range sourceUsers {
		if err := deleteUserWithReassign(cmd, server, container, strconv.Itoa(u.ID), newUserID); err != nil {
			fmt.Fprintf(os.Stderr, "    Delete/Reassign user %d: %v\n", u.ID, err)
		}
	}

	return nil
}

// Expanded simpleUser with login & display name
type simpleUser struct {
	ID          int    `json:"ID"`
	Login       string `json:"user_login"`
	Email       string `json:"user_email"`
	DisplayName string `json:"display_name"`
	UserName    string `json:"user_name"`
}

// Unified finder: pulls all needed fields once, filters in Go
func findTargetUsers(
	cmd *cobra.Command,
	server, container string,
	emailList map[string]struct{},
	idSet map[int]struct{},
	usernameSet map[string]struct{},
	displayNameSet map[string]struct{},
) ([]simpleUser, error) {

	wpCmd := []string{"user", "list", "--fields=ID,user_login,user_email,display_name", "--format=json"}
	fmt.Printf("    Enumerating users: wp %s\n", strings.Join(wpCmd, " "))
	out, stderr, err := runWP(cmd, server, container, wpCmd)
	if err != nil {
		fmt.Printf("    WP user list error: %v (stderr: %s)\n", err, strings.TrimSpace(stderr))
		return nil, err
	}

	var all []simpleUser
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}

	pattern := strings.ToLower(purgeEmailPattern)
	hasPattern := pattern != ""
	hasEmailList := len(emailList) > 0
	hasIDs := len(idSet) > 0
	hasUsernames := len(usernameSet) > 0
	hasDisplayNames := len(displayNameSet) > 0

	anySelector := hasPattern || hasEmailList || hasIDs || hasUsernames || hasDisplayNames

	var matched []simpleUser
	for _, u := range all {
		if u.ID == 0 {
			continue
		}
		fmt.Printf("    Checking user %v\n", u)
		emailL := strings.ToLower(u.Email)
		loginL := strings.ToLower(u.Login)
		displayL := strings.ToLower(u.DisplayName)
		usernameL := strings.ToLower(u.UserName)

		fmt.Printf("    User %d: email=%s, login=%s, display=%s, username=%s\n", u.ID, emailL, loginL, displayL, usernameL)

		fmt.Printf("Pattern: %s\n", pattern)

		match := false
		if hasPattern {
			// Substring match across email, login, display name
			if strings.Contains(emailL, pattern) ||
				strings.Contains(loginL, pattern) ||
				strings.Contains(displayL, pattern) {
				match = true
			}
		}
		if !match && hasEmailList {
			if _, ok := emailList[emailL]; ok {
				match = true
			}
		}
		if !match && hasIDs {
			if _, ok := idSet[u.ID]; ok {
				match = true
			}
		}
		if !match && hasUsernames {
			if _, ok := usernameSet[loginL]; ok {
				match = true
			}
		}
		if !match && hasDisplayNames {
			if _, ok := displayNameSet[displayL]; ok {
				match = true
			}
		}

		if anySelector && !match {
			continue
		}
		matched = append(matched, u)
	}

	// Deduplicate by ID (should already be unique)
	seen := map[int]struct{}{}
	outUsers := make([]simpleUser, 0, len(matched))
	for _, u := range matched {
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		outUsers = append(outUsers, u)
	}

	fmt.Printf("    Matched %d user(s) in container %s\n", len(outUsers), container)
	return outUsers, nil
}

func ensureTargetUser(cmd *cobra.Command, server, container string, createArgs []string) (string, error) {
	fmt.Println("    Ensuring target user...")
	if purgeDryRun {
		fmt.Printf("      [DRY RUN] wp user create %s\n", strings.Join(createArgs, " "))
		return "99999", nil
	}

	// Attempt create
	wpCmd := append([]string{"user", "create"}, createArgs...)
	out, errOut, err := runWP(cmd, server, container, wpCmd)
	if err == nil {
		// Extract ID
		id := extractFirstNumber(out)
		if id == "" {
			// Maybe output in stderr
			id = extractFirstNumber(errOut)
		}
		if id != "" {
			fmt.Printf("      Created user ID %s\n", id)
			return id, nil
		}
	}

	fmt.Printf("      Failed to create user: %s %s\n", out, errOut)

	// If already exists, lookup by login (first arg)

	if strings.Contains(out+errOut, "already registered") || err != nil {
		login := createArgs[0]
		getCmd := []string{"user", "get", login, "--field=ID"}
		idOut, _, gErr := runWP(cmd, server, container, getCmd)

		fmt.Printf("      Attempting to find existing user by login '%s'...\n", login)
		fmt.Printf("      WP Command: wp %s\n", strings.Join(getCmd, " "))
		if gErr == nil {
			fmt.Printf("      Found existing user by login '%s': %s\n", login, idOut)
			id := strings.TrimSpace(idOut)
			if id != "" {
				fmt.Printf("      Found existing user ID %s\n", id)
				return id, nil
			}
		}
		fmt.Printf("      Failed to find existing user by login '%s': %v\n", login, gErr)
	}

	return "", fmt.Errorf("failed to create or find target user: %s %s", out, errOut)
}

func reassignPosts(cmd *cobra.Command, server, container, oldID, newID string) error {
	fmt.Printf("    Reassigning posts from %s -> %s\n", oldID, newID)
	postListCmd := []string{"post", "list", "--author=" + oldID, "--format=ids"}
	out, _, err := runWP(cmd, server, container, postListCmd)
	if err != nil {
		return err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		fmt.Println("      No posts.")
		return nil
	}
	if purgeDryRun {
		fmt.Printf("      [DRY RUN] wp post update %s --post_author=%s\n", out, newID)
		return nil
	}
	updateCmd := append([]string{"post", "update"}, strings.Split(out, " ")...)
	updateCmd = append(updateCmd, "--post_author="+newID)
	_, _, err = runWP(cmd, server, container, updateCmd)
	if err != nil {
		return err
	}
	fmt.Println("      Posts reassigned.")
	return nil
}

func backupDatabase(cmd *cobra.Command, server, container string) error {
	ts := time.Now().Format("2006-01-02-150405")
	filename := fmt.Sprintf("db_backup_%s_%s_%s.sql", sanitizeFilePart(server), container, ts)
	if purgeDryRun {
		fmt.Printf("    [DRY RUN] wp db export - > %s\n", filename)
		return nil
	}

	if purgeLargeOutputs && server == "local" {
		// Stream directly to file to avoid keeping entire dump in memory.
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return fmt.Errorf("open backup file: %w", err)
		}
		defer f.Close()

		base := []string{"docker", "exec", container, "wp", "--allow-root", "--skip-plugins", "--skip-themes", "--path=/var/www/html", "db", "export", "-"}
		shellCmd := shellJoin(base)
		c := exec.Command("sh", "-c", shellCmd)
		c.Stdout = f
		var stderr strings.Builder
		c.Stderr = &stderr
		fmt.Println("    Streaming DB export (large-outputs enabled)...")
		if err := c.Run(); err != nil {
			os.Remove(filename)
			return fmt.Errorf("db export stream failed: %v (stderr: %s)", err, stderr.String())
		}
		fmt.Printf("    DB backup: %s (streamed)\n", filename)
		return nil
	}

	// Fallback (remote or not large)
	dbCmd := []string{"db", "export", "-"}
	out, errStr, err := runWP(cmd, server, container, dbCmd)
	if err != nil {
		return fmt.Errorf("db export: %v (stderr: %s)", err, errStr)
	}
	if err := os.WriteFile(filename, []byte(out), 0600); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	fmt.Printf("    DB backup: %s\n", filename)
	return nil
}

// func deleteUser(cmd *cobra.Command, server, container, userID string) error {
// 	if purgeDryRun {
// 		fmt.Printf("    [DRY RUN] wp user delete %s --yes\n", userID)
// 		return nil
// 	}
// 	delCmd := []string{"user", "delete", userID, "--yes"}
// 	_, _, err := runWP(cmd, server, container, delCmd)
// 	if err == nil {
// 		fmt.Printf("    Deleted user %s\n", userID)
// 	}
// 	return err
// }

// NEW: delete user while reassigning their content in one step
func deleteUserWithReassign(cmd *cobra.Command, server, container, userID, newUserID string) error {
	if purgeDryRun {
		fmt.Printf("    [DRY RUN] wp user delete %s --reassign=%s --yes\n", userID, newUserID)
		return nil
	}
	delCmd := []string{"user", "delete", userID, "--reassign=" + newUserID, "--yes"}
	_, stderr, err := runWP(cmd, server, container, delCmd)
	if err != nil {
		return fmt.Errorf("wp user delete %s --reassign=%s failed (stderr: %s): %w", userID, newUserID, strings.TrimSpace(stderr), err)
	}
	fmt.Printf("    Deleted user %s (reassigned to %s)\n", userID, newUserID)
	return nil
}

// NEW: update a user's password
func updateUserPassword(cmd *cobra.Command, server, container, userID, password string) error {
    if purgeDryRun {
        fmt.Printf("      [DRY RUN] wp user reset-password %s --password=<redacted> --skip-email\n", userID)
        return nil
    }
    // Use reset-password to avoid triggering password change notifications.
    wpCmd := []string{"user", "reset-password", userID, "--password=" + password, "--skip-email"}
    _, stderr, err := runWP(cmd, server, container, wpCmd)
    if err != nil {
        return fmt.Errorf("wp user reset-password %s failed (stderr: %s): %w", userID, strings.TrimSpace(stderr), err)
    }
    fmt.Printf("      Password reset for user %s\n", userID)
    return nil
}

// NEW: generate random base64 password
func randomBase64Password() (string, error) {
	b := make([]byte, 32) // 32 bytes -> 43 chars (raw, no padding)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

// runWP executes a wp command inside a container (local or remote).
func runWP(cmd *cobra.Command, server, container string, wpArgs []string) (stdout string, stderr string, err error) {
	base := []string{"docker", "exec", container, "wp", "--allow-root", "--skip-plugins", "--skip-themes", "--path=/var/www/html"}
	full := append(base, wpArgs...)
	shellCmd := shellJoin(full)

	if server == "local" {
		// If large outputs requested, use pipes & readAll for consistency / potential future streaming logic.
		if purgeLargeOutputs {
			c := exec.Command("sh", "-c", shellCmd)
			stdoutPipe, _ := c.StdoutPipe()
			stderrPipe, _ := c.StderrPipe()
			if err := c.Start(); err != nil {
				return "", "", err
			}
			outStr := readAll(stdoutPipe)
			errStr := readAll(stderrPipe)
			waitErr := c.Wait()
			return outStr, errStr, waitErr
		}
		c := exec.Command("sh", "-c", shellCmd)
		outB, err := c.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return string(outB), string(ee.Stderr), err
			}
			return string(outB), "", err
		}
		return string(outB), "", nil
	}

	// Remote: current SSH helper returns full buffered strings (no streaming).
	client, err := createSSHClient(cmd, server)
	if err != nil {
		return "", "", err
	}
	defer client.Close()
	stdout, stderr, err = client.ExecuteCommand(shellCmd)
	return stdout, stderr, err
}

func listWPContainers(cmd *cobra.Command, server string) ([]string, error) {
	var out string
	var err error
	if server == "local" {
		c := exec.Command("docker", "ps", "--format", "{{.Names}}")
		b, e := c.Output()
		out, err = string(b), e
	} else {
		client, e := createSSHClient(cmd, server)
		if e != nil {
			return nil, e
		}
		defer client.Close()
		var stderr string
		out, stderr, err = client.ExecuteCommand("docker ps --format '{{.Names}}'")
		if err != nil {
			return nil, fmt.Errorf("remote docker ps: %v (stderr: %s)", err, stderr)
		}
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	var res []string
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, "wp_") {
			res = append(res, name)
		}
	}
	return res, nil
}

// Helpers

func csvToSet(s string) map[string]struct{} {
	m := map[string]struct{}{}
	if strings.TrimSpace(s) == "" {
		return m
	}
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			m[p] = struct{}{}
		}
	}
	return m
}

// Convert comma-separated string of ints to set
func csvToIntSet(s string) map[int]struct{} {
	m := map[int]struct{}{}
	if strings.TrimSpace(s) == "" {
		return m
	}
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if id, err := strconv.Atoi(p); err == nil {
			m[id] = struct{}{}
		}
	}
	return m
}

// Lowercasing string set
func csvToLowerSet(s string) map[string]struct{} {
	m := map[string]struct{}{}
	if strings.TrimSpace(s) == "" {
		return m
	}
	for _, part := range strings.Split(s, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		if p != "" {
			m[p] = struct{}{}
		}
	}
	return m
}

var reNumber = regexp.MustCompile(`\b\d+\b`)

func extractFirstNumber(s string) string {
	return reNumber.FindString(s)
}

func sanitizeFilePart(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

func shellJoin(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			out = append(out, "''")
			continue
		}
		if strings.ContainsAny(p, " \t\n\"'\\$`!&*()[]{}<>|;") {
			out = append(out, "'"+strings.ReplaceAll(p, "'", "'\"'\"'")+"'")
		} else {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// func escapeArg(s string) string {
// 	if s == "" {
// 		return "''"
// 	}
// 	if strings.ContainsAny(s, " \t\n\"'\\$`") {
// 		return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
// 	}
// 	return s
// }

// (Optional) If large outputs needed (not currently), stream helper:
func readAll(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}

// setUserRole assigns a role slug to a user.
func setUserRole(cmd *cobra.Command, server, container, userID, role string) error {
	if purgeDryRun {
		fmt.Printf("      [DRY RUN] wp user set-role %s %s\n", userID, role)
		return nil
	}
	wpCmd := []string{"user", "set-role", userID, role}
	_, stderr, err := runWP(cmd, server, container, wpCmd)
	if err != nil {
		return fmt.Errorf("wp user set-role failed (stderr: %s): %w", strings.TrimSpace(stderr), err)
	}
	fmt.Printf("      Role set for user %s -> %s\n", userID, role)
	return nil
}

// normalizeRole converts user input (role name or numeric level) to a role slug.
// Returns: roleSlug, levelInfo, error
func normalizeRole(input string) (string, string, error) {
	in := strings.ToLower(strings.TrimSpace(input))
	if in == "" {
		return "", "", errors.New("empty role")
	}
	// Direct known slugs / names
	roleMap := map[string]string{
		"admin":         "administrator",
		"administrator": "administrator",
		"editor":        "editor",
		"author":        "author",
		"contributor":   "contributor",
		"subscriber":    "subscriber",
	}
	if slug, ok := roleMap[in]; ok {
		return slug, "", nil
	}
	// Try numeric (treat as user level)
	if lvl, err := strconv.Atoi(in); err == nil {
		// Map common levels to closest standard role
		type levelRole struct {
			min  int
			max  int
			slug string
		}
		// Simple mapping based on typical WP capabilities layout
		var levelMapping = []levelRole{
			{8, 10, "administrator"},
			{7, 7, "editor"},
			{2, 6, "author"},
			{1, 1, "contributor"},
			{0, 0, "subscriber"},
		}
		for _, lr := range levelMapping {
			if lvl >= lr.min && lvl <= lr.max {
				return lr.slug, fmt.Sprintf(" (from level %d)", lvl), nil
			}
		}
		// Fallback
		if lvl >= 10 {
			return "administrator", fmt.Sprintf(" (from level %d)", lvl), nil
		}
		if lvl <= 0 {
			return "subscriber", fmt.Sprintf(" (from level %d)", lvl), nil
		}
		return "author", fmt.Sprintf(" (from level %d)", lvl), nil
	}
	return "", "", fmt.Errorf("unknown role or level: %s", input)
}

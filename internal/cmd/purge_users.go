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
	purgeScrambleEmail  string // NEW: scramble or replace user email addresses
	purgeReassignToUser string // NEW: existing user selector for reassignment (ID, login, email, or display name)
	purgeCreateUser     bool   // NEW: whether to create the target user (requires create args)
)

var purgeUsersCmd = &cobra.Command{
	Use:   "purge-users [server] [selectors] [flags] -- <wp user create args>",
	Short: "Reassign posts to a target user (optionally create) and delete/modify old users",
	Long: `Find users by selectors (email pattern/list, ids, usernames, display names).
Optionally:
  * Provide '--reassign-to-user=<selector>' to reuse an existing user.
  * Provide wp user create args after '--' (e.g. '-- newlogin new@site.com --role=author') to create the target user.
If both are omitted the command fails when a target user is required.
Examples:
  purge-users --email-pattern='@old.com' --reassign-to-user=admin
  purge-users wp3.example.com --email-list=users.txt -- newowner newowner@new.com --role=editor`,
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

	purgeUsersCmd.Flags().StringVar(&purgeScrambleEmail, "scramble-email-addrs", "", "Scramble or replace source user email addresses. Forms: inject-hash-before (ihb), inject-hash-after (iha), combine with '|', or replace-with=<email>")
	purgeUsersCmd.Flags().StringVar(&purgeReassignToUser, "reassign-to-user", "", "Existing user (ID, login, email, or display name) to receive reassigned posts")
	purgeUsersCmd.Flags().BoolVar(&purgeCreateUser, "create-user", false, "Create the target user (provide wp user create args after '--')")
}

func runPurgeUsers(cmd *cobra.Command, args []string) error {
	// Validation of selection method (now broader)
	if purgeEmailPattern == "" &&
		purgeEmailListFile == "" &&
		purgeIDs == "" &&
		purgeUsernames == "" &&
		purgeDisplayNames == "" {
		return errors.New("must provide at least one selector: --email-pattern | --email-list | --ids | --usernames | --display-names")
	}

	// Remove old target user mode validation; both creation and reassign are optional now.

	var serverOverride string
	var createArgs []string

	// Argument heuristic:
	// Cases:
	//   serverOverride only:                [server]
	//   create (no server):                 [login email ...]
	//   serverOverride + create:            [server login email ...]
	// Determine index of first email-looking arg (contains '@').
	emailIdx := -1
	for i, a := range args {
		if strings.Contains(a, "@") {
			emailIdx = i
			break
		}
	}

	switch {
	case len(args) == 0:
		// nothing (server-range will drive)
	case len(args) == 1 && emailIdx == -1:
		// single non-email -> server override
		serverOverride = args[0]
	case emailIdx == 1:
		// login email ... (create only)
		createArgs = args
	case emailIdx == 2:
		// server login email ...
		serverOverride = args[0]
		createArgs = args[1:]
	case emailIdx == -1:
		// Multiple args but no email -> ambiguous / invalid
		return errors.New("could not detect create args (no email found); supply login email after '--' or reduce to a single server arg")
	case emailIdx == 0:
		return errors.New("first create arg cannot be an email; expected <login> <email>")
	default:
		// email at position >2 unsupported (would be ambiguous)
		return errors.New("unexpected argument layout; email must appear as second (login email) or third (server login email) positional")
	}

	if len(createArgs) > 0 && len(createArgs) < 2 {
		return errors.New("insufficient create args (need at least <login> <email>)")
	}

	// Disallow conflicting specification
	if len(createArgs) > 0 && purgeReassignToUser != "" {
		return errors.New("cannot use both create args (after '--') and --reassign-to-user")
	}

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
	users, usersErr := findTargetUsers(cmd, server, container, emailList, idSet, usernameSet, displayNameSet)
	if usersErr != nil {
		return fmt.Errorf("find users: %w", usersErr)
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

	// 2. Obtain target user only if specified (creation args or --reassign-to-user)
	var (
		newUserID string
		err       error
		hasTarget bool
	)
	if len(createArgs) > 0 {
		newUserID, err = ensureTargetUser(cmd, server, container, createArgs)
		hasTarget = true
	} else if purgeReassignToUser != "" {
		newUserID, err = resolveExistingTargetUser(cmd, server, container, purgeReassignToUser)
		hasTarget = true
	}
	if err != nil {
		return fmt.Errorf("determine target user: %w", err)
	}
	if hasTarget && newUserID == "" {
		return errors.New("could not determine target user ID")
	}

	// Build list excluding target user if we have one
	sourceUsers := users
	if hasTarget {
		src := sourceUsers[:0]
		for _, u := range users {
			if strconv.Itoa(u.ID) == newUserID {
				continue
			}
			src = append(src, u)
		}
		sourceUsers = src
	}

	if len(sourceUsers) == 0 {
		fmt.Println("    No non-target users to process.")
		return nil
	}

	// 3. Post reassignment only if a target user is present AND we are not deleting (legacy behavior)
	if hasTarget && !purgeDelete {
		for _, u := range sourceUsers {
			if err := reassignPosts(cmd, server, container, strconv.Itoa(u.ID), newUserID); err != nil {
				fmt.Fprintf(os.Stderr, "    Reassign posts (%d): %v\n", u.ID, err)
			}
		}
	}

	// 3a. Role adjustment (allowed even without target user)
	if purgeSetRole != "" && !purgeDelete {
		roleSlug, level, err := normalizeRole(purgeSetRole)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    Role normalization error: %v\n", err)
		} else {
			fmt.Printf("    Setting role '%s'%s for matched users%s.\n",
				roleSlug, level, func() string {
					if hasTarget {
						return " (excluding target)"
					}
					return ""
				}())
			for _, u := range sourceUsers {
				if err := setUserRole(cmd, server, container, strconv.Itoa(u.ID), roleSlug); err != nil {
					fmt.Fprintf(os.Stderr, "      Set role user %d: %v\n", u.ID, err)
				}
			}
		}
	}

	// 3b. Password updates (applies whether deleting or not; does not require target)
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

	// 3c. Email scrambling / replacement (does not require target)
	if f := cmd.Flags().Lookup("scramble-email-addrs"); f != nil && f.Changed && purgeScrambleEmail != "" {
		spec, err := parseScrambleEmailSpec(purgeScrambleEmail)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    Scramble spec error: %v\n", err)
		} else {
			fmt.Println("    Scrambling/replacing emails for matched source users...")
			for _, u := range sourceUsers {
				newEmail, err := scrambleEmail(u.Email, spec)
				if err != nil {
					fmt.Fprintf(os.Stderr, "      User %d email scramble error: %v\n", u.ID, err)
					continue
				}
				if strings.EqualFold(newEmail, u.Email) {
					continue
				}
				if err := updateUserEmail(cmd, server, container, strconv.Itoa(u.ID), newEmail); err != nil {
					fmt.Fprintf(os.Stderr, "      Update email user %d: %v\n", u.ID, err)
				}
			}
		}
	}

	// 4. Backup DB before any delete operations
	if purgeDelete {
		if err := backupDatabase(cmd, server, container); err != nil {
			fmt.Fprintf(os.Stderr, "    DB backup warning: %v\n", err)
		}
	}

	// 5. Deletion (requires target user for reassignment)
	if !purgeDelete {
		if !hasTarget {
			fmt.Println("    No target user specified; reassignment skipped (no deletion requested).")
		} else {
			fmt.Printf("    Deletion skipped (use --delete to remove %d user(s)).\n", len(sourceUsers))
		}
		return nil
	}
	if purgeDelete && !hasTarget {
		return errors.New("deletion requested but no target user provided (need --reassign-to-user or create args)")
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
		fmt.Printf("      [DRY RUN] wp user reset-password %s --skip-email\n", userID)
		return nil
	}
	// reset-password generates a new password internally; custom password values cannot be forced here.
	wpCmd := []string{"user", "reset-password", userID, "--skip-email"}
	_, stderr, err := runWP(cmd, server, container, wpCmd)
	if err != nil {
		return fmt.Errorf("wp user reset-password %s failed (stderr: %s): %w", userID, strings.TrimSpace(stderr), err)
	}
	fmt.Printf("      Password reset for user %s (WP generated)\n", userID)
	return nil
}

// NEW: update a user's email
func updateUserEmail(cmd *cobra.Command, server, container, userID, email string) error {
	if purgeDryRun {
		fmt.Printf("      [DRY RUN] wp user update %s --user_email=%s\n", userID, email)
		return nil
	}
	wpCmd := []string{"user", "update", userID, "--user_email=" + email}
	_, stderr, err := runWP(cmd, server, container, wpCmd)
	if err != nil {
		return fmt.Errorf("wp user update %s email failed (stderr: %s): %w", userID, strings.TrimSpace(stderr), err)
	}
	fmt.Printf("      Email updated for user %s -> %s\n", userID, email)
	return nil
}

// NEW: scramble spec
type scrambleSpec struct {
	replace      string
	injectBefore bool
	injectAfter  bool
}

var reEmailSimple = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

// Parse --scramble-email-addrs value
func parseScrambleEmailSpec(val string) (scrambleSpec, error) {
	val = strings.TrimSpace(val)
	var spec scrambleSpec
	if val == "" {
		return spec, errors.New("empty scramble spec")
	}
	if strings.HasPrefix(strings.ToLower(val), "replace-with=") {
		raw := strings.TrimSpace(val[len("replace-with="):])
		if !reEmailSimple.MatchString(raw) {
			return spec, fmt.Errorf("invalid replacement email: %s", raw)
		}
		spec.replace = raw
		return spec, nil
	}
	toks := strings.Split(val, "|")
	for _, t := range toks {
		t = strings.ToLower(strings.TrimSpace(t))
		switch t {
		case "inject-hash-before", "ihb":
			spec.injectBefore = true
		case "inject-hash-after", "iha":
			spec.injectAfter = true
		case "":
			// skip
		default:
			return spec, fmt.Errorf("unknown scramble token: %s", t)
		}
	}
	if !spec.injectBefore && !spec.injectAfter {
		return spec, errors.New("no valid scramble actions found (expected ihb/iha or replace-with=...)")
	}
	return spec, nil
}

// NEW: perform email scrambling per spec
func scrambleEmail(original string, spec scrambleSpec) (string, error) {
	if spec.replace != "" {
		return spec.replace, nil
	}
	at := strings.IndexByte(original, '@')
	if at <= 0 || at == len(original)-1 {
		return "", fmt.Errorf("not a normal email: %s", original)
	}
	local := original[:at]
	domain := original[at+1:]
	modLocal := local
	modDomain := domain

	if spec.injectBefore {
		// Find first non-alphanumeric boundary
		var boundary = -1
		for i, r := range local {
			if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
				boundary = i
				break
			}
		}
		randSeg, _ := randomBase64Segment(16)
		if boundary >= 0 && boundary < len(local) {
			modLocal = local[:boundary] + randSeg + local[boundary:]
		} else {
			modLocal = local + randSeg
		}
	}

	if spec.injectAfter {
		randSeg, _ := randomBase64Segment(10)
		// Insert as new left-most label
		modDomain = randSeg + "." + domain
	}

	return modLocal + "@" + modDomain, nil
}

// NEW: random base64 (sanitized) segment
func randomBase64Segment(n int) (string, error) {
	if n <= 0 {
		n = 12
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base64.StdEncoding.EncodeToString(b)
	// Remove padding and non-alphanumerics for email safety
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	out := sb.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return out, nil
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

func randomBase64Password() (string, error) {
	// Generate a random base64 string and sanitize it for password use
	b := make([]byte, 16) // 16 bytes = 24 base64 characters
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base64.StdEncoding.EncodeToString(b)
	// Remove padding and non-alphanumerics for password safety
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		}
	}
	out := sb.String()
	if len(out) < 8 {
		return "", errors.New("generated password too short")
	}
	return out, nil
}

// NEW: resolve existing target user by selector (ID, login, email, or display name)
func resolveExistingTargetUser(cmd *cobra.Command, server, container, selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", errors.New("empty target user selector")
	}
	fmt.Printf("    Resolving target user '%s'...\n", selector)

	// Fast path: numeric ID
	if _, err := strconv.Atoi(selector); err == nil {
		idOut, stderr, err := runWP(cmd, server, container, []string{"user", "get", selector, "--field=ID"})
		if err != nil {
			return "", fmt.Errorf("verify user ID %s failed (stderr: %s): %w", selector, strings.TrimSpace(stderr), err)
		}
		return strings.TrimSpace(idOut), nil
	}

	// If looks like email or login (wp user get supports login/email)
	if strings.Contains(selector, "@") || selector != "" {
		idOut, stderr, err := runWP(cmd, server, container, []string{"user", "get", selector, "--field=ID"})
		if err == nil && strings.TrimSpace(idOut) != "" {
			return strings.TrimSpace(idOut), nil
		}

		if err != nil {
			return "", fmt.Errorf("get user by login/email '%s' failed (stderr: %s): %w", selector, strings.TrimSpace(stderr), err)
		}

		// Fall through to display name search if that failed
	}

	// Display name fallback (case-insensitive exact)
	listCmd := []string{"user", "list", "--fields=ID,display_name,user_login,user_email", "--format=json"}
	out, stderr, err := runWP(cmd, server, container, listCmd)
	if err != nil {
		return "", fmt.Errorf("list users for display name search failed (stderr: %s): %w", strings.TrimSpace(stderr), err)
	}
	var users []struct {
		ID          int    `json:"ID"`
		DisplayName string `json:"display_name"`
		Login       string `json:"user_login"`
		Email       string `json:"user_email"`
	}
	if jErr := json.Unmarshal([]byte(out), &users); jErr != nil {
		return "", fmt.Errorf("decode users (display search): %w", jErr)
	}
	targetLower := strings.ToLower(selector)
	for _, u := range users {
		if strings.ToLower(u.DisplayName) == targetLower {
			return strconv.Itoa(u.ID), nil
		}
	}
	return "", fmt.Errorf("could not resolve target user '%s' in container %s", selector, container)
}

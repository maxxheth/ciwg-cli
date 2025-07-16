package utils

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// InspectZipFile inspects a zip file to find a WordPress installation and a MySQL dump file.
func InspectZipFile(zipPath string) (wpPath, sqlPath string, err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", "", err
	}
	defer r.Close()

	var wpFound, sqlFound bool

	for _, f := range r.File {
		// Check for WordPress installation
		if strings.HasSuffix(f.Name, "wp-config.php") {
			wpPath = filepath.Dir(f.Name)
			// Check for wp-content as well
			for _, f2 := range r.File {
				if strings.HasPrefix(f2.Name, filepath.Join(wpPath, "wp-content")) {
					wpFound = true
					break
				}
			}
		}

		// Check for SQL file
		if strings.HasSuffix(f.Name, ".sql") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			defer rc.Close()

			scanner := bufio.NewScanner(rc)
			for scanner.Scan() {
				if strings.Contains(scanner.Text(), "MySQL dump") {
					sqlPath = f.Name
					sqlFound = true
					break
				}
			}
		}

		if wpFound && sqlFound {
			return wpPath, sqlPath, nil
		}
	}

	if !wpFound {
		return "", "", fmt.Errorf("WordPress installation not found in zip")
	}
	if !sqlFound {
		return "", "", fmt.Errorf("MySQL dump file not found in zip")
	}

	return "", "", fmt.Errorf("could not find both WordPress and MySQL dump in zip")
}

// SpinUpSite creates and starts a WordPress site using the Docker SDK.
func SpinUpSite(domain, url, dbName, zipPath, wpContentPath, sqlFilePath, projectPath string, dryRun bool) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("error creating docker client: %w", err)
	}

	if dryRun {
		fmt.Println("--- Dry Run Mode ---")
		fmt.Printf("Domain: %s\n", domain)
		fmt.Printf("URL: %s\n", url)
		fmt.Printf("Database Name: %s\n", dbName)
		fmt.Printf("Zip Path: %s\n", zipPath)
		fmt.Printf("WordPress Content Path: %s\n", wpContentPath)
		fmt.Printf("SQL File Path: %s\n", sqlFilePath)
		fmt.Printf("Project Path: %s\n", projectPath)
		fmt.Println("--------------------")
		return nil
	}

	// Create the project directory
	if err := os.MkdirAll(projectPath, 0755); err != nil {
		return fmt.Errorf("error creating project directory: %w", err)
	}

	// Copy skeleton and replace variables
	domain, url, dbName, _, _, err = copySkeletonAndReplace(projectPath, domain, url, dbName, dryRun)
	if err != nil {
		return fmt.Errorf("error copying skeleton and replacing variables: %w", err)
	}

	// Unzip the file
	if err := unzip(zipPath, filepath.Join(projectPath, "www")); err != nil {
		return fmt.Errorf("error unzipping file: %w", err)
	}

	// Replace wp-content
	wwwPath := filepath.Join(projectPath, "www")
	if err := os.RemoveAll(filepath.Join(wwwPath, "wp-content")); err != nil {
		return fmt.Errorf("error removing existing wp-content: %w", err)
	}
	if err := os.Rename(filepath.Join(wwwPath, wpContentPath), filepath.Join(wwwPath, "wp-content")); err != nil {
		return fmt.Errorf("error moving new wp-content: %w", err)
	}

	// Run docker-compose up
	cmd := "docker-compose"
	args := []string{"-f", filepath.Join(projectPath, "docker-compose.yml"), "up", "-d"}
	if err := runCommand(projectPath, cmd, args...); err != nil {
		return fmt.Errorf("error running docker-compose up: %w", err)
	}

	// Import SQL
	containerName := dbName
	// container.Exe

	execConfig := container.ExecOptions{
		Cmd:          []string{"wp", "--allow-root", "db", "import", sqlFilePath},
		AttachStdout: true,
		AttachStderr: true,
	}

	execID, err := cli.ContainerExecCreate(ctx, containerName, execConfig)
	if err != nil {
		return fmt.Errorf("error creating exec command: %w", err)
	}

	if err := cli.ContainerExecStart(ctx, execID.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("error starting exec command: %w", err)
	}

	return nil
}

func copySkeletonAndReplace(projectPath, domain, url, dbName string, dryRun bool) (string, string, string, string, string, error) {
	var dbUser, dbPass string
	reader := bufio.NewReader(os.Stdin)

	if domain == "" {
		for {
			fmt.Print("We need the domain name (TLD, e.g.: bobhvac.com -OR- staging site subdomain, e.g.: bobhvac.wp99.ciwgserver.com): ")
			domain, _ = reader.ReadString('\n')
			domain = strings.TrimSpace(domain)
			if domain != "" && !strings.Contains(domain, "www.") && !strings.Contains(domain, "/") {
				// Simplified validation. In a real-world scenario, you'd want more robust validation.
				break
			}
			fmt.Println("Invalid domain. Please try again.")
		}
	}

	if url == "" {
		for {
			fmt.Print("Please enter the full URL of the website (make sure to include www if that was the way the site was): ")
			url, _ = reader.ReadString('\n')
			url = strings.TrimSpace(url)
			if strings.HasPrefix(url, "https://") {
				break
			}
			fmt.Println("Invalid URL. Must start with https://. Please try again.")
		}
	}

	if dbName == "" {
		for {
			fmt.Print("What would you like for a database name/user? (Note: 'wp_' is automatically prepended): ")
			dbName, _ = reader.ReadString('\n')
			dbName = strings.TrimSpace(dbName)
			if dbName != "" {
				dbUser = dbName
				dbName = "wp_" + dbName
				break
			}
			fmt.Println("Invalid database name. Please try again.")
		}
	}

	// Generate a 16-character alphanumeric password
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 16)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", "", "", "", "", fmt.Errorf("failed to generate random password: %w", err)
		}
		b[i] = chars[n.Int64()]
	}
	dbPass = string(b)

	if dryRun {
		fmt.Println("--- Dry Run: Skeleton and Replace ---")
		fmt.Printf("Project Path: %s\n", projectPath)
		fmt.Printf("Domain: %s\n", domain)
		fmt.Printf("URL: %s\n", url)
		fmt.Printf("Database Name: %s\n", dbName)
		fmt.Printf("Database User: %s\n", dbUser)
		fmt.Printf("Database Password: %s\n", dbPass)
		fmt.Println("------------------------------------")
		return domain, url, dbName, dbUser, dbPass, nil
	}

	// Copy the skeleton directory
	if err := copyDirectory("./.skel", projectPath); err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to copy skeleton directory: %w", err)
	}

	// Read and update docker-compose.yml
	composePath := filepath.Join(projectPath, "docker-compose.yml")
	composeBytes, err := os.ReadFile(composePath)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to read docker-compose.yml: %w", err)
	}

	composeContent := string(composeBytes)
	composeContent = strings.ReplaceAll(composeContent, "%DOMAIN%", domain)
	composeContent = strings.ReplaceAll(composeContent, "%URL%", url)
	composeContent = strings.ReplaceAll(composeContent, "%DB_NAME%", dbName)
	composeContent = strings.ReplaceAll(composeContent, "%DB_USER%", dbUser)
	composeContent = strings.ReplaceAll(composeContent, "%DB_PASS%", dbPass)

	if err := os.WriteFile(composePath, []byte(composeContent), 0644); err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to write updated docker-compose.yml: %w", err)
	}

	// Read and update robots.txt
	robotsPath := filepath.Join(projectPath, "robots.txt")
	robotsBytes, err := os.ReadFile(robotsPath)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to read robots.txt: %w", err)
	}

	robotsContent := string(robotsBytes)
	robotsContent = strings.ReplaceAll(robotsContent, "%URL%", url)

	if err := os.WriteFile(robotsPath, []byte(robotsContent), 0644); err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to write updated robots.txt: %w", err)
	}

	return domain, url, dbName, dbUser, dbPass, nil
}

func copyDirectory(src, dest string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(src, entry.Name())
		destPath := filepath.Join(dest, entry.Name())

		fileInfo, err := os.Stat(sourcePath)
		if err != nil {
			return err
		}

		switch fileInfo.Mode() & os.ModeType {
		case os.ModeDir:
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return err
			}
			if err := copyDirectory(sourcePath, destPath); err != nil {
				return err
			}
		default:
			if err := copyFile(sourcePath, destPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		_, err = io.Copy(outFile, rc)

		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}

func runCommand(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

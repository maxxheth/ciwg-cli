#!/bin/bash
# filepath: /var/www/ciwg-cli/deploy.sh

HOME_PATH="$1"

# Create installation directory
mkdir -p /usr/local/bin/ciwg-cli-utils
tar_path="./dist/ciwg-cli-utils.tgz"

echo "Tarball found at $tar_path"

# Function to extract with pax (more portable than GNU tar)
extract_with_pax() {
    local tarball="$1"
    local destination="$2"
    local strip_levels="$3"
    
    # Use pax which is more portable and part of POSIX
    if command -v pax >/dev/null 2>&1; then
        echo "Using pax for extraction..."
        
        # Create strip pattern for pax
        strip_pattern=""
        for ((i=0; i<strip_levels; i++)); do
            strip_pattern="${strip_pattern}[^/]*/"
        done
        
        # Extract using pax with stripping
        if gunzip -c "$tarball" | pax -r -s "|^${strip_pattern}||" -C "$destination"; then
            echo "Successfully extracted with pax"
            return 0
        else
            echo "pax extraction failed"
            return 1
        fi
    else
        echo "pax not available"
        return 1
    fi
}

# Function to extract with GNU tar if available
extract_with_gnu_tar() {
    local tarball="$1"
    local destination="$2"
    local strip_levels="$3"
    
    # Check if we're using GNU tar
    if tar --version 2>/dev/null | grep -q "GNU tar"; then
        echo "Using GNU tar..."
        if tar -xzf "$tarball" -C "$destination" --strip-components="$strip_levels"; then
            echo "Successfully extracted with GNU tar"
            return 0
        else
            echo "GNU tar extraction failed"
            return 1
        fi
    else
        echo "GNU tar not available"
        return 1
    fi
}

# Function to extract with fallback method
extract_with_fallback() {
    local tarball="$1"
    local destination="$2"
    local strip_levels="$3"
    
    echo "Using fallback extraction method..."
    
    # Create temporary directory
    temp_dir=$(mktemp -d)
    
    # Extract to temp directory first
    if tar -xzf "$tarball" -C "$temp_dir"; then
        echo "Extracted to temporary directory: $temp_dir"
        
        # Navigate down the directory structure
        current_dir="$temp_dir"
        for ((i=0; i<strip_levels; i++)); do
            # Find the first directory at this level
            next_dir=$(find "$current_dir" -mindepth 1 -maxdepth 1 -type d | head -1)
            if [ -z "$next_dir" ]; then
                echo "Error: Cannot strip $strip_levels levels from tarball"
                echo "Directory structure:"
                find "$temp_dir" -type d | head -10
                rm -rf "$temp_dir"
                return 1
            fi
            current_dir="$next_dir"
        done
        
        echo "Source directory after stripping: $current_dir"
        
        # Copy contents to destination
        if cp -r "$current_dir"/* "$destination/" 2>/dev/null; then
            echo "Successfully copied files to destination"
            rm -rf "$temp_dir"
            return 0
        else
            echo "Failed to copy files to destination"
            echo "Contents of source directory:"
            ls -la "$current_dir"
            rm -rf "$temp_dir"
            return 1
        fi
    else
        echo "Failed to extract tarball to temporary directory"
        rm -rf "$temp_dir"
        return 1
    fi
}

# Main extraction logic
if [[ -f $tar_path ]]; then
    echo "Starting extraction process..."
    
    # First, examine the tarball structure
    echo "Examining tarball structure:"
    tar -tzf "$tar_path" | head -5
    
    # Try extraction with strip level 1 (removes the ciwg-cli-utils/ directory)
    # Extract to /usr/local/bin/ciwg-cli-utils (the contents will go there)
    if extract_with_pax "$tar_path" "/usr/local/bin/ciwg-cli-utils" 1; then
        echo "Extraction completed successfully with pax"
    elif extract_with_gnu_tar "$tar_path" "/usr/local/bin/ciwg-cli-utils" 1; then
        echo "Extraction completed successfully with GNU tar"
    elif extract_with_fallback "$tar_path" "/usr/local/bin/ciwg-cli-utils" 1; then
        echo "Extraction completed successfully with fallback method"
    else
        echo "All extraction methods failed. Debugging tarball structure..."
        echo "Tarball contents:"
        tar -tzf "$tar_path" | head -20
        echo ""
        echo "Total files in tarball: $(tar -tzf "$tar_path" | wc -l)"
        exit 1
    fi
    
    # Verify extraction and setup
    echo "Verifying extraction..."
    BINARY_FOUND=false
    
    # Check for the binary in expected location
    if [[ -f "/usr/local/bin/ciwg-cli-utils/ciwg-cli" ]]; then
        chmod +x /usr/local/bin/ciwg-cli-utils/ciwg-cli
        echo "Binary found and made executable at /usr/local/bin/ciwg-cli-utils/ciwg-cli"
        BINARY_FOUND=true
    else
        # Look for binary recursively in the installation directory
        FOUND_BINARY=$(find "/usr/local/bin/ciwg-cli-utils" -name "ciwg-cli" -type f 2>/dev/null | head -1)
        if [ -n "$FOUND_BINARY" ]; then
            echo "Found binary at: $FOUND_BINARY"
            # Move to correct location if needed
            if [ "$FOUND_BINARY" != "/usr/local/bin/ciwg-cli-utils/ciwg-cli" ]; then
                cp "$FOUND_BINARY" "/usr/local/bin/ciwg-cli-utils/ciwg-cli"
                echo "Copied binary to /usr/local/bin/ciwg-cli-utils/ciwg-cli"
            fi
            chmod +x /usr/local/bin/ciwg-cli-utils/ciwg-cli
            echo "Binary made executable"
            BINARY_FOUND=true
        fi
    fi
    
    if [ "$BINARY_FOUND" = false ]; then
        echo "Error: Could not find ciwg-cli binary after extraction"
        echo "Contents of /usr/local/bin/ciwg-cli-utils:"
        ls -la /usr/local/bin/ciwg-cli-utils/ || echo "Directory not found"
        exit 1
    fi
    
    # Verify .skel directory
    if [[ -d "/usr/local/bin/ciwg-cli-utils/.skel" ]]; then
        echo "Skeleton files found at /usr/local/bin/ciwg-cli-utils/.skel"
    else
        # Look for .skel directory
        SKEL_DIR=$(find "/usr/local/bin/ciwg-cli-utils" -name ".skel" -type d 2>/dev/null | head -1)
        if [ -n "$SKEL_DIR" ]; then
            echo "Found .skel directory at: $SKEL_DIR"
            if [ "$SKEL_DIR" != "/usr/local/bin/ciwg-cli-utils/.skel" ]; then
                cp -r "$SKEL_DIR" "/usr/local/bin/ciwg-cli-utils/.skel"
                echo "Copied .skel to /usr/local/bin/ciwg-cli-utils/.skel"
            fi
        else
            echo "Warning: .skel directory not found"
        fi
    fi

    
    
    # Create symlink in /usr/local/bin for easy access
    if [ ! -L "/usr/local/bin/ciwg-cli" ]; then
        ln -s /usr/local/bin/ciwg-cli-utils/ciwg-cli /usr/local/bin/ciwg-cli
        echo "Created symlink: /usr/local/bin/ciwg-cli -> /usr/local/bin/ciwg-cli-utils/ciwg-cli"
    else
        echo "Symlink already exists: /usr/local/bin/ciwg-cli"
    fi
    
    # Add to PATH if HOME_PATH is provided
    if [ -n "$HOME_PATH" ] && [ -f "$HOME_PATH/.bashrc" ]; then
        if ! grep -q "/usr/local/bin" "$HOME_PATH/.bashrc"; then
            echo 'export PATH="$PATH:/usr/local/bin"' >> "$HOME_PATH/.bashrc"
            echo "Added /usr/local/bin to PATH in $HOME_PATH/.bashrc"
        else
            echo "/usr/local/bin already in PATH"
        fi
    fi
    
    # Test the binary via symlink
    if /usr/local/bin/ciwg-cli --help >/dev/null 2>&1; then
        echo "Binary test successful"
    else
        echo "Warning: Binary test failed, but installation completed"
    fi
    
    echo "Deployment completed successfully"
    echo "Binary location: /usr/local/bin/ciwg-cli-utils/ciwg-cli"
    echo "Symlink: /usr/local/bin/ciwg-cli -> /usr/local/bin/ciwg-cli-utils/ciwg-cli"
    echo "Skeleton files: /usr/local/bin/ciwg-cli-utils/.skel"
    
else
    echo "ciwg-cli-utils.tgz not found at $tar_path"
    echo "Please run 'make build' first to create the tarball"
    exit 1
fi
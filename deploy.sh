#!/bin/bash
# filepath: /var/www/ciwg-cli/deploy.sh

HOME_PATH="$1"

# Create installation directory
mkdir -p /usr/local/bin/ciwg-cli-utils
tar_path="./dist/ciwg-cli-utils.tgz"

echo "Tarball found at $tar_path"

# Removed pax support: rely on GNU tar if present, else fallback copy method.

# Function to extract with GNU tar if available
extract_with_gnu_tar() {
    local tarball="$1"
    local destination="$2"
    local strip_levels="$3"
    
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

# Fallback extraction (portable, no strip support directly)
extract_with_fallback() {
    local tarball="$1"
    local destination="$2"
    local strip_levels="$3"
    
    echo "Using fallback extraction method..."
    temp_dir=$(mktemp -d)
    if tar -xzf "$tarball" -C "$temp_dir"; then
        current_dir="$temp_dir"
        for ((i=0; i<strip_levels; i++)); do
            next_dir=$(find "$current_dir" -mindepth 1 -maxdepth 1 -type d | head -1)
            if [ -z "$next_dir" ]; then
                echo "Error: Cannot strip $strip_levels levels"
                find "$temp_dir" -type d | head -10
                rm -rf "$temp_dir"
                return 1
            fi
            current_dir="$next_dir"
        done
        mkdir -p "$destination"
        if cp -r "$current_dir"/* "$destination/" 2>/dev/null; then
            echo "Successfully copied files to destination"
            rm -rf "$temp_dir"
            return 0
        else
            echo "Failed to copy files"
            ls -la "$current_dir"
            rm -rf "$temp_dir"
            return 1
        fi
    else
        echo "Failed to extract tarball (fallback)"
        rm -rf "$temp_dir"
        return 1
    fi
}

# Main extraction logic
if [[ -f $tar_path ]]; then
    echo "Starting extraction process..."
    echo "Examining tarball structure:"
    tar -tzf "$tar_path" | head -5
    
    if extract_with_gnu_tar "$tar_path" "/usr/local/bin/ciwg-cli-utils" 1; then
        echo "Extraction completed successfully (GNU tar)"
    elif extract_with_fallback "$tar_path" "/usr/local/bin/ciwg-cli-utils" 1; then
        echo "Extraction completed successfully (fallback)"
    else
        echo "All extraction methods failed."
        echo "Tarball contents (first 20):"
        tar -tzf "$tar_path" | head -20
        echo "Total files: $(tar -tzf "$tar_path" | wc -l)"
        exit 1
    fi
    
    echo "Verifying extraction..."
    BINARY_FOUND=false
    if [[ -f "/usr/local/bin/ciwg-cli-utils/ciwg-cli" ]]; then
        chmod +x /usr/local/bin/ciwg-cli-utils/ciwg-cli
        echo "Binary found at expected path."
        BINARY_FOUND=true
    else
        FOUND_BINARY=$(find "/usr/local/bin/ciwg-cli-utils" -name "ciwg-cli" -type f 2>/dev/null | head -1)
        if [ -n "$FOUND_BINARY" ]; then
            echo "Found binary at: $FOUND_BINARY"
            cp "$FOUND_BINARY" "/usr/local/bin/ciwg-cli-utils/ciwg-cli"
            chmod +x /usr/local/bin/ciwg-cli-utils/ciwg-cli
            echo "Binary copied & made executable."
            BINARY_FOUND=true
        fi
    fi
    
    if [ "$BINARY_FOUND" = false ]; then
        echo "Error: ciwg-cli binary not found after extraction"
        ls -la /usr/local/bin/ciwg-cli-utils/ || true
        exit 1
    fi
    
    if [[ -d "/usr/local/bin/ciwg-cli-utils/.skel" ]]; then
        echo "Skeleton directory present."
    else
        SKEL_DIR=$(find "/usr/local/bin/ciwg-cli-utils" -name ".skel" -type d 2>/dev/null | head -1)
        if [ -n "$SKEL_DIR" ]; then
            cp -r "$SKEL_DIR" "/usr/local/bin/ciwg-cli-utils/.skel"
            echo "Copied skeleton directory."
        else
            echo "Warning: .skel directory not found."
        fi
    fi
    
    if [ ! -L "/usr/local/bin/ciwg-cli" ]; then
        ln -s /usr/local/bin/ciwg-cli-utils/ciwg-cli /usr/local/bin/ciwg-cli
        echo "Symlink created."
    else
        echo "Symlink already exists."
    fi
    
    if [ -n "$HOME_PATH" ] && [ -f "$HOME_PATH/.bashrc" ]; then
        if ! grep -q "/usr/local/bin" "$HOME_PATH/.bashrc"; then
            echo 'export PATH="$PATH:/usr/local/bin"' >> "$HOME_PATH/.bashrc"
            echo "Added /usr/local/bin to PATH in .bashrc"
        fi
    fi
    
    if /usr/local/bin/ciwg-cli --help >/dev/null 2>&1; then
        echo "Binary test OK."
    else
        echo "Warning: Binary help test failed."
    fi
    
    echo "Deployment complete."
else
    echo "ciwg-cli-utils.tgz not found at $tar_path"
    echo "Run: make build"
    exit
fi
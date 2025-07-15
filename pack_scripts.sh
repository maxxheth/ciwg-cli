#!/bin/bash

# This script finds and archives Bash and Python scripts from the current directory.

set -e

# --- Configuration and Argument Parsing ---

SEARCH_DIR="."
DEST_DIR="."
BASH_TARBALL_NAME="ciwg_bash_scripts"
PYTHON_TARBALL_NAME="ciwg_python_scripts"
DRY_RUN=false

usage() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  -d, --directory <path>      Directory to search in (default: current directory)."
    echo "  --dest <path>               Destination directory for the tarballs (default: current directory)."
    echo "  -o, --output-names <b|p>    Basenames for the output tarballs, delimited by '|'."
    echo "                              Example: 'bash_archive|python_archive'"
    echo "  --dry-run                   Run the script without creating files or directories."
    echo "  -h, --help                  Display this help message."
    exit 0
}

# --- Helper function for dry run ---
run_cmd() {
    if [ "$DRY_RUN" = true ]; then
        echo "DRY RUN: Would execute: $*"
    else
        "$@"
    fi
}

while [[ "$#" -gt 0 ]]; do
    case $1 in
        -d|--directory)
            SEARCH_DIR="$2"
            if [ ! -d "$SEARCH_DIR" ]; then
                echo "Error: Directory '$SEARCH_DIR' not found."
                exit 1
            fi
            shift
            ;;
        --dest)
            DEST_DIR="$2"
            shift
            ;;
        -o|--output-names)
            IFS='|' read -r BASH_TARBALL_NAME PYTHON_TARBALL_NAME <<< "$2"
            if [ -z "$PYTHON_TARBALL_NAME" ]; then
                # If only one name is provided, use it for both with suffixes
                PYTHON_TARBALL_NAME="${BASH_TARBALL_NAME}_py"
                BASH_TARBALL_NAME="${BASH_TARBALL_NAME}_sh"
            fi
            shift
            ;;
        --dry-run) DRY_RUN=true ;;
        -h|--help) usage ;;
        *) echo "Unknown parameter passed: $1"; usage; exit 1 ;;
    esac
    shift
done

run_cmd mkdir -p "$DEST_DIR"

# --- Bash Script Archiving ---
echo "Processing Bash scripts..."
BASH_SCRIPT_DIR="ciwg_bash_scripts_temp"
run_cmd mkdir -p "$BASH_SCRIPT_DIR"

# Find all .sh files (excluding this script itself) and copy them.
find "$SEARCH_DIR" -type f -name "*.sh" -not -name "$(basename "$0")" -print0 | while IFS= read -r -d $'\0' file; do
    run_cmd cp "$file" "$BASH_SCRIPT_DIR/"
done

# Create a tar archive if any scripts were found.
if [ "$(ls -A "$BASH_SCRIPT_DIR")" ]; then
    run_cmd tar -czf "${DEST_DIR}/${BASH_TARBALL_NAME}.tar.gz" "$BASH_SCRIPT_DIR"
    echo "All Bash scripts have been copied to '$BASH_SCRIPT_DIR' and compressed into '${DEST_DIR}/${BASH_TARBALL_NAME}.tar.gz'."
else
    echo "No Bash scripts were found to archive."
fi
run_cmd rm -rf "$BASH_SCRIPT_DIR"

echo ""

# --- Python Script Archiving ---
echo "Processing Python scripts..."
PYTHON_SCRIPT_DIR="ciwg_python_scripts_temp"
PYTHON_PROJECTS_DIR="$PYTHON_SCRIPT_DIR/projects"
PYTHON_STANDALONE_DIR="$PYTHON_SCRIPT_DIR/standalone"

run_cmd mkdir -p "$PYTHON_PROJECTS_DIR"
run_cmd mkdir -p "$PYTHON_STANDALONE_DIR"

processed_dirs=()

# Function to check if a directory has already been processed
is_processed() {
    for dir in "${processed_dirs[@]}"; do
        if [[ "$1" == "$dir" ]]; then
            return 0
        fi
    done
    return 1
}

# Find and handle Python projects
find "$SEARCH_DIR" -type f -name "main.py" | while read -r main_py_file; do
    dir=$(dirname "$main_py_file")
    if [ -f "$dir/requirements.txt" ]; then
        if ! is_processed "$dir"; then
            echo "Found Python project in '$dir'. Copying..."
            # Use rsync to copy directory contents
            run_cmd rsync -a --exclude='__pycache__/' --exclude='*.pyc' "$dir/" "$PYTHON_PROJECTS_DIR/$(basename "$dir")"
            processed_dirs+=("$dir")
        fi
    fi
done

# Find and handle standalone Python scripts
find "$SEARCH_DIR" -type f -name "*.py" | while read -r py_file; do
    dir=$(dirname "$py_file")
    if ! is_processed "$dir"; then
        # Check if it's not part of a project we just copied
        if [ "$(basename "$py_file")" != "main.py" ] || [ ! -f "$dir/requirements.txt" ]; then
             echo "Found standalone Python script '$py_file'. Copying..."
             run_cmd cp "$py_file" "$PYTHON_STANDALONE_DIR/"
        fi
    fi
done


# Create a tar archive if any python files were found
if [ -n "$(ls -A "$PYTHON_SCRIPT_DIR")" ]; then
    run_cmd tar -czf "${DEST_DIR}/${PYTHON_TARBALL_NAME}.tar.gz" "$PYTHON_SCRIPT_DIR"
    echo "All Python scripts and projects have been copied to '$PYTHON_SCRIPT_DIR' and compressed into '${DEST_DIR}/${PYTHON_TARBALL_NAME}.tar.gz'."
else
    echo "No Python scripts or projects were found to archive."
fi
run_cmd rm -rf "$PYTHON_SCRIPT_DIR"

echo -e "\nScript execution finished."

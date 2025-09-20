#!/bin/bash

show_help() {
    echo "Usage: $0 [--help] [--dry-run] [--delete] [--container-name <name|name|...>] [--container-file <file>]"
    echo ""
    echo "Options:"
    echo "  --help              Show this help message and exit"
    echo "  --dry-run           Print actions without executing them"
    echo "  --delete             Stop and remove containers, and delete associated directories"
    echo "  --container-name    Pipebar-delimited container names or working directories to process (e.g. wp_foo|wp_bar|/srv/foo)"
    echo "  --container-file    File with newline-delimited container names or working directories to process"
}

DRY_RUN=0
DELETE=0
CONTAINER_INPUTS=()
CONTAINER_FILE=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --help)
            show_help
            exit 0
            ;;
        --dry-run)
            DRY_RUN=1
            shift
            ;;
        --delete)
            DELETE=1
            shift
            ;;
        --container-name)
            IFS='|' read -ra NAMES <<< "$2"
            for name in "${NAMES[@]}"; do
                CONTAINER_INPUTS+=("$name")
            done
            shift 2
            ;;
        --container-file)
            CONTAINER_FILE="$2"
            shift 2
            ;;
        *)
            shift
            ;;
    esac
done

if [[ -n "$CONTAINER_FILE" ]]; then
    if [[ ! -f "$CONTAINER_FILE" ]]; then
        echo "Container file '$CONTAINER_FILE' not found."
        exit 1
    fi
    while IFS= read -r line; do
        [[ -n "$line" ]] && CONTAINER_INPUTS+=("$line")
    done < "$CONTAINER_FILE"
fi

# Build a list of (container, working_dir) pairs
declare -a CONTAINER_PAIRS

if [[ ${#CONTAINER_INPUTS[@]} -eq 0 ]]; then
    # Default: all wp_ containers
    while IFS= read -r cname; do
        wdir=$(docker inspect "$cname" 2>/dev/null | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"')
        [[ -n "$wdir" && "$wdir" != "null" ]] && CONTAINER_PAIRS+=("$cname|$wdir")
    done < <(docker ps --format '{{.Names}}' | grep '^wp_')
else
    for input in "${CONTAINER_INPUTS[@]}"; do
        if [[ "$input" == /* ]]; then
            # Absolute path: treat as working_dir
            cname=$(docker ps --format '{{.Names}}' | while read -r name; do
                wdir=$(docker inspect "$name" 2>/dev/null | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"')
                [[ "$wdir" == "$input" ]] && echo "$name"
            done | head -n1)
            if [[ -n "$cname" ]]; then
                CONTAINER_PAIRS+=("$cname|$input")
            else
                echo "No running container found for directory '$input'. Skipping..."
            fi
        else
            # Try as container name first
            wdir=$(docker inspect "$input" 2>/dev/null | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"')
            if [[ -n "$wdir" && "$wdir" != "null" ]]; then
                CONTAINER_PAIRS+=("$input|$wdir")
            else
                # Try as a directory under /var/opt
                candidate_dir="/var/opt/$input"
                cname=$(docker ps --format '{{.Names}}' | while read -r name; do
                    wdir=$(docker inspect "$name" 2>/dev/null | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"')
                    [[ "$wdir" == "$candidate_dir" ]] && echo "$name"
                done | head -n1)
                if [[ -n "$cname" ]]; then
                    CONTAINER_PAIRS+=("$cname|$candidate_dir")
                else
                    echo "No running container or directory found for '$input'. Skipping..."
                fi
            fi
        fi
    done
fi

if [[ ${#CONTAINER_PAIRS[@]} -eq 0 ]]; then
    echo "No containers found to process."
    exit 1
fi

for pair in "${CONTAINER_PAIRS[@]}"; do
    container="${pair%%|*}"
    working_dir="${pair#*|}"

    echo "Processing container: $container"
    echo "Working directory: $working_dir"

    timestamp=$(date +%Y%m%d-%H%M%S)
    tarball_name="$(basename "$working_dir")-$timestamp.tgz"

    if [ $DRY_RUN -eq 1 ]; then
        echo "[DRY RUN] Would run: docker exec -u 0 $container wp --allow-root db export"
        echo "[DRY RUN] Would cd to /var/opt and tar -czf $tarball_name --exclude='*.tgz' --exclude='*.tar.gz' --exclude='*.zip' $working_dir"
        echo "[DRY RUN] Would mkdir -p /var/opt/backup-tarballs/"
        echo "[DRY RUN] Would mv /var/opt/$tarball_name /var/opt/backup-tarballs/"
        if [ $DELETE -eq 1 ]; then
            echo "[DRY RUN] Would run: docker stop $container && docker rm $container"
            echo "[DRY RUN] Would run: rm -rf $working_dir"
        fi
    else
        echo "Exporting DB in $container..."

        # Delete all SQL files located in /var/www/html in the Docker container that are older than 2 hours.

        docker exec -u 0 "$container" find /var/www/html -name "*.sql" -type f -mmin +120 -exec rm -f {} \;

        docker exec -u 0 "$container" wp --allow-root db export

        echo "Creating tarball..."
        cd /var/opt || { echo "Failed to cd to /var/opt"; continue; }
        tar -czf "$tarball_name" --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "$working_dir"

        echo "Moving tarball to backup directory..."
        mkdir -p /var/opt/backup-tarballs/
        mv "/var/opt/$tarball_name" /var/opt/backup-tarballs/

        if [ $DELETE -eq 1 ]; then
            echo "Deleting directory $working_dir..."
            rm -rf "$working_dir"
            echo "Stopping and removing Docker container $container..."
            docker stop "$container" 2>/dev/null || true # Ignore errors
            docker rm "$container" 2>/dev/null || true # Ignore errors
        fi
    fi

    echo "Done with $container"
    echo ""
done
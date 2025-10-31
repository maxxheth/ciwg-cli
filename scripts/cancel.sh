#!/bin/bash

# source "$(dirname "$0")/../.env" # Is this necessary? 

# Load environment variables from .env file if it exists
SCRIPT_PATH="$(dirname "$(readlink -f "$0")")"
if [ -f "$SCRIPT_PATH/../.env" ]; then
  export "$(grep -v '^#' "$SCRIPT_PATH/../.env" | xargs)"
else
  echo "Warning: .env file not found. Using default environment variables."
fi

# Colors for messages
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No color

# Function to display help
show_help() {
  echo "Usage: $0 <domain> [NEW_ADMIN_EMAIL] [--dry-run] [--help]"
  echo
  echo "Script to cancel a WordPress site by deactivating plugins, removing license keys,"
  echo "exporting the database, and archiving the site directory."
  echo
  echo "Arguments:"
  echo "  <domain>            The base domain name of the WordPress site (e.g., example.com)."
  echo "  NEW_ADMIN_EMAIL     (Optional) The new email address to set for 'admin_email'."
  echo
  echo "Options:"
  echo "  --help              Display this help message."
  echo "  --dry-run           Perform a trial run with no changes made."
  echo "  --container-name-prefix  Prefix for the container name (default: 'wp_')."
  echo
}


# --- Add dry-run and container-prefix flags and parse arguments ---
DRY_RUN=false
CONTAINER_PREFIX="wp_"                # default prefix
while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h)
      show_help
      exit 0
      ;;
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --container-name-prefix)
      CONTAINER_PREFIX="$2"
      shift 2
      ;;
    *)
      break
      ;;
  esac
done

# Validate domain parameter
if [ -z "$1" ]; then
  echo -e "${RED}Error: Domain parameter is required.${NC}"
  echo "Run '$0 --help' for usage."
  exit 1
fi

DOMAIN_SEARCH_PHRASE=$1
NEW_ADMIN_EMAIL=$2

# --- Discover the matching container by WP_HOME ---
MATCHED_CONTAINER=""
for cont in $(docker ps --format='{{.Names}}' | grep "^${CONTAINER_PREFIX}"); do
  WP_HOME=$(docker inspect "$cont" \
    | jq -r '.[].Config.Env | map(select(contains("WP_HOME="))) | .[0] | split("=")[1]')
  if [[ "$WP_HOME" == *"$DOMAIN_SEARCH_PHRASE"* ]]; then
    MATCHED_CONTAINER="$cont"
    break
  fi
done

if [ -z "$MATCHED_CONTAINER" ]; then
  echo -e "${RED}Error: No container with prefix '${CONTAINER_PREFIX}' has WP_HOME matching '${DOMAIN_SEARCH_PHRASE}'.${NC}"
  exit 1
fi

CONTAINER_NAME="$MATCHED_CONTAINER"

echo "Container found: $CONTAINER_NAME"

# Grab the container’s working directory and switch into it
WORKING_DIR=$(docker inspect "$CONTAINER_NAME" \
  | jq -r '.[].Config.Labels["com.docker.compose.project.working_dir"]')
if [ ! -d "$WORKING_DIR" ]; then
  echo -e "${RED}Error: Working dir '$WORKING_DIR' from container not found locally.${NC}"
  exit 1
fi
pushd "$WORKING_DIR" > /dev/null || {
  echo -e "${RED}Error: Could not change to working directory '$WORKING_DIR'.${NC}"
  exit 1
}

# Now SITE_DIR is relative to this working directory

# Remove any trailing slashes or "http://" or "https://" from $WP_HOME
WP_HOME=$(echo "$WP_HOME" | sed -e 's|^https\?://||' -e 's|^http://||' -e 's|/$||' -e 's|/$||')

echo "WP_HOME after cleanup: $WP_HOME"

SITE_DIR="$WORKING_DIR"
ZIP_FILE="${WP_HOME}.zip"
WP_CONTENT_DIR="${SITE_DIR}/www/wp-content"

# Options to remove
OPTIONS_TO_REMOVE=(
  "license_number"
  "_elementor_pro_license_data"
  "_elementor_pro_license_data_fallback"
  "_elementor_pro_license_v2_data_fallback"
  "_elementor_pro_license_v2_data"
  "_transient_timeout_rg_gforms_license"
  "_transient_rg_gforms_license"
  "_transient_timeout_uael_license_status"
  "_transient_timeout_astra-addon_license_status"
)

# Function to run wp-cli command inside Docker container
run_wp() {
  # figure out where WP lives inside the container
  local CONTAINER_WORKDIR
  CONTAINER_WORKDIR=$(
    docker inspect "$CONTAINER_NAME" \
      | jq -r '.[].Config.WorkingDir // "/var/www/html"'
  )
  docker exec -w "$CONTAINER_WORKDIR" \
    "$CONTAINER_NAME" wp "$@" --skip-themes --quiet
}

# Verify that the domain's directory exists
if [ ! -d "$SITE_DIR" ]; then
  echo -e "${RED}Error: Directory ${SITE_DIR} does not exist. Ensure the domain is correct.${NC}"
  exit 1
fi

# Check if the container exists
if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  echo -e "${RED}Error: Container ${CONTAINER_NAME} not found.${NC}"
  exit 1
fi

# Announce dry‐run
if [[ "$DRY_RUN" == true ]]; then
  echo -e "${GREEN}[DRY RUN] No changes will be made.${NC}"
fi

# Disconnect from malcare
if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY RUN] Would run: wp malcare disconnect"
else
  run_wp malcare disconnect
fi

# Remove specified options
echo "Removing specified WordPress options..."
for OPTION in "${OPTIONS_TO_REMOVE[@]}"; do
  if [[ "$DRY_RUN" == true ]]; then
    echo "[DRY RUN] Would delete option: $OPTION"
  else
    echo "Removing option: $OPTION"
    run_wp option delete "$OPTION"
  fi
done

# Update transient
if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY RUN] Would update _transient_astra-addon_license_status to 0"
else
  echo "Updating option: _transient_astra-addon_license_status to value 0"
  run_wp option update "_transient_astra-addon_license_status" 0
fi

# Update admin_email
if [ -n "$NEW_ADMIN_EMAIL" ]; then
  if [[ "$DRY_RUN" == true ]]; then
    echo "[DRY RUN] Would update admin_email to $NEW_ADMIN_EMAIL"
  else
    echo "Updating 'admin_email' to $NEW_ADMIN_EMAIL"
    run_wp option update "admin_email" "$NEW_ADMIN_EMAIL" \
      && echo -e "${GREEN}Admin email updated successfully.${NC}" \
      || echo -e "${RED}Failed to update admin_email.${NC}"
  fi
else
  NEW_ADMIN_EMAIL=$ADMIN_EMAIL
fi

# Add a new admin user
RANDOM_PASSWORD=$(< /dev/urandom tr -dc 'A-Za-z0-9' | head -c12)
if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY RUN] Would create admin user $NEW_ADMIN_EMAIL with password $RANDOM_PASSWORD"
else
  run_wp user create $NEW_ADMIN_EMAIL $NEW_ADMIN_EMAIL --role=administrator --display_name="New Admin" --user_nicename="New Admin" --first_name="New" --last_name="Admin" --user_pass="${RANDOM_PASSWORD}"
  run_wp user update $NEW_ADMIN_EMAIL $NEW_ADMIN_EMAIL --role=administrator --display_name="New Admin" --user_nicename="New Admin" --first_name="New" --last_name="Admin" --user_pass="${RANDOM_PASSWORD}"
fi

# Export the database
if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY_RUN] Would export DB to ${WP_CONTENT_DIR}/mysql.sql"
else
  echo "Exporting database for $DOMAIN_SEARCH_PHRASE"
  # remove any old directory named mysql.sql
  docker exec "$CONTAINER_NAME" wp db export
fi

# Zip the site directory
if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY RUN] Would zip ${SITE_DIR} to ${ZIP_FILE}"
else
  echo "Zipping site directory to ${ZIP_FILE}..."
  # Exclude any existing zip/tar files and any directory whose name contains "backup"
  (
    cd "$(dirname "$SITE_DIR")" \
      && zip -rq "$ZIP_FILE" "$(basename "$SITE_DIR")" \
        -x "*backup*" "*.zip" "*.tar.gz" "*.tgz"
    
    # Move the zip file to the wp-content directory
    if [ -f "$ZIP_FILE" ]; then
      mv "$ZIP_FILE" "$WP_CONTENT_DIR/"
    else
      echo -e "${RED}Error: Zip file ${ZIP_FILE} was not created successfully.${NC}"
      exit 1
    fi

  )
  echo -e "\n${GREEN}Zipping completed.${NC}"
fi

# Ensure wp-content directory exists
if [ ! -d "$WP_CONTENT_DIR" ]; then
  echo -e "${RED}Error: wp-content directory ${WP_CONTENT_DIR} does not exist. Ensure the site structure is correct.${NC}"
  exit 1
fi

# Change ownership of the zip file and move it to wp-content
if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY RUN] Would chown and mv ${ZIP_FILE} into ${WP_CONTENT_DIR}/"
else
  echo "Changing ownership of the zip file to www-data:www-data"
  chown www-data:www-data "$ZIP_FILE"

  echo "Moving zip file to wp-content directory: ${WP_CONTENT_DIR}"
  mv "$ZIP_FILE" "$WP_CONTENT_DIR/"
fi

echo -e "${GREEN}Cancellation process for $WP_HOME completed successfully: https://$WP_HOME/wp-content/$WP_HOME.zip ${NC}"
echo -e "NEW ADMIN EMAIL: ${NEW_ADMIN_EMAIL}"
echo -e "NEW ADMIN PASS: ${RANDOM_PASSWORD}"

CURRENT_EPOCH=$(date +%s)

# Print the current epoch time to the site dir and save it to a file called 'cancellation-epoch.txt'
if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY RUN] Would write epoch $CURRENT_EPOCH to ${SITE_DIR}/cancellation-epoch.txt"
else
  echo "$CURRENT_EPOCH" > "$SITE_DIR/cancellation-epoch.txt"
fi

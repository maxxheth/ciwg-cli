#!/bin/bash

LOG_FILE="/var/www/log/wordpress-website.log"

# Function to log messages
log_message() {
  TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')

  # ANSI escape codes for colors and formatting
  BOLD_WHITE="\e[1;37m"
  LIGHT_GRAY="\e[0;37m"
  RESET="\e[0m"

  # Output the formatted log message
  echo -e "${BOLD_WHITE}${TIMESTAMP}${RESET} - ${LIGHT_GRAY}$1${RESET}" | tee -a /var/www/log/wordpress-website.log
}

# Ensure /var/www/log has the correct permissions
fix_permissions() {
  if [ -d /var/www/html ]; then
    log_message "Ensuring correct ownership for /var/www/html..."
    chown -R www-data:www-data /var/www/html
  fi
}


# Function to check if the WordPress database exists
check_database() {
  log_message "Checking for the WordPress database: $WORDPRESS_DB_NAME"
  while ! mysql -h "$WORDPRESS_DB_HOST" -u "$WORDPRESS_DB_USER" -p"$WORDPRESS_DB_PASSWORD" -e "USE $WORDPRESS_DB_NAME;" >/dev/null 2>&1; do
    log_message "Database not found ($WORDPRESS_DB_NAME), waiting 5 seconds..."
    sleep 5
  done
  log_message "Database found: $WORDPRESS_DB_NAME"
}

# Function to check if the database has tables
database_has_tables() {
  TABLE_COUNT=$(mysql -h "$WORDPRESS_DB_HOST" -u "$WORDPRESS_DB_USER" -p"$WORDPRESS_DB_PASSWORD" -D "$WORDPRESS_DB_NAME" -e "SHOW TABLES;" | wc -l)
  if [ "$TABLE_COUNT" -gt 0 ]; then
    return 0  # Database has tables
  else
    return 1  # Database is empty
  fi
}

# Import /data/mysql.sql if it exists and the database is empty
import_sql() {
  if [ -f /data/mysql.sql ]; then
    if database_has_tables; then
      log_message "Tables already exist in the database. Skipping SQL import."
    else
      log_message "Importing /data/mysql.sql..."
      mysql -h "$WORDPRESS_DB_HOST" -u "$WORDPRESS_DB_USER" -p"$WORDPRESS_DB_PASSWORD" "$WORDPRESS_DB_NAME" < /data/mysql.sql
      log_message "SQL import completed: $WORDPRESS_DB_NAME"
    fi
  else
    log_message "/data/mysql.sql not found. Skipping import."
  fi
}

# Check and update site URL
update_siteurl() {
  if [ -n "$WP_HOME" ]; then
    log_message "Checking the current site URL in the wp_options table..."
    current_siteurl=$(wp option get siteurl --path=/var/www/html --skip-themes --skip-plugins --allow-root 2>/dev/null)

    if [ -z "$current_siteurl" ]; then
      log_message "Error: Could not retrieve siteurl or siteurl is blank. Skipping search-replace."
      return 1
    fi

    if [ "$current_siteurl" != "$WP_HOME" ]; then
      log_message "Running wp search-replace to update the site URL from $current_siteurl to $WP_HOME..."
      wp search-replace "$current_siteurl" "$WP_HOME" --path=/var/www/html --skip-themes --skip-plugins --allow-root
      log_message "Site URL updated to $WP_HOME."
    else
      log_message "Site URL is already set to $WP_HOME. No changes needed."
    fi
  else
    log_message "WP_HOME environment variable is not set. Skipping site URL update."
  fi
}


# Fix WP Rocket
fix_rocket() {
    log_message "Fixing WP Rocket..."
	wp option update wp_rocket_settings --format=json < /wp_rocket_settings.json
}

# Create Users
garbage_cleanup() {
	if [ -f ./wp-content/mu-plugins/mu-plugin.php ]; then
		rm -fr ./wp-content/mu-plugins/*
	fi;
}

# Main script execution
fix_permissions
check_database
import_sql
update_siteurl
fix_rocket
garbage_cleanup

# Pass control to the original docker-entrypoint.sh
exec docker-entrypoint.sh apache2-foreground

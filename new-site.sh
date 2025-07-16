# ASCII Banner with Black Background and Bold Green Text
echo -e "\e[40m\e[1;32m" # Black background with bold green text
echo "########################################################"
echo "####   Welcome to CIWEBGROUP.COM's WordPress Site   ####"
echo "####     Migration and Generation Utility!!!!       ####"
echo "########################################################"
echo -e "\e[0m"  # Reset colors

# Get the server's public IP address
server_ip=$(curl -s http://checkip.amazonaws.com)

# Function to log messages
log_message() {
  echo -e "\e[1;37m$1\e[0m" # Bold white text for log messages
}


# Function to check if the domain resolves to the server's IP
validate_domain() {
  domain_ip=$(dig +short "$1")
  if [[ $domain_ip == "$server_ip" ]]; then
    log_message "Domain validated successfully."
    return 0  # Indicate success
  else
    echo -e "\e[33mDomain validation failed. $1 does not resolve to $server_ip.\e[0m"
    echo -e "Currently pointing to: $domain_ip"
    echo -e "Verify this is the correct domain by allowing time for DNS propagation or provide a new domain."
    read -p "Press ENTER to try again, or type a new domain to update: " new_domain
    if [[ -n "$new_domain" ]]; then
      domain="$new_domain"  # Update the domain with the new input
    fi
    return 1  # Indicate failure to keep the loop running
  fi
}


# Function to check if the database exists
check_database() {
  if mysql -u root -e "USE $1;" 2>/dev/null; then
    log_message "Database $1 already exists."
    read -p "Would you like to drop it and create a new one? (y/n): " choice
    if [[ $choice == "y" ]]; then
      mysql -u root -e "DROP DATABASE $1;"
      log_message "Database $1 dropped."
      return 0  # Indicate the database can now be created
    else
      return 1  # Indicate the database still exists, prompting for a new name
    fi
  else
    return 0  # Indicate the database does not exist
  fi
}




# Prompt for domain name (no empty input allowed, without "www" or slashes)
while true; do
  read -p "We need the domain name (TLD, e.g.: bobhvac.com -OR- staging site subdomain, e.g.: bobhvac.wp99.ciwgserver.com): " domain
  if [[ -z "$domain" ]]; then
    echo -e "\e[31mDomain name cannot be empty. Please enter a valid domain.\e[0m"
  elif [[ "$domain" =~ www\. ]]; then
    echo -e "\e[31mThe domain name should not include 'www' subdomain. Please enter a valid domain.\e[0m"
    domain=""  # Clear the variable to prompt again
  elif [[ "$domain" =~ / ]]; then
    echo -e "\e[31mThe domain name should not contain any slashes. Please enter a valid domain.\e[0m"
    domain=""  # Clear the variable to prompt again
  elif [[ -d "/var/opt/$domain" ]]; then
    echo -e "\e[31mThe domain '$domain' already exists on this server.\e[0m"
    domain=""  # Clear the variable to prompt again
  else
    if validate_domain "$domain"; then
      break  # Exit the loop if the domain validates successfully
    fi
  fi
done


# Prompt for the full URL of the website (must start with https and no empty input allowed)
while [[ -z "$url" || ! "$url" =~ ^https:// ]]; do
  read -p "Please enter the full URL of the website (make sure to include www if that was the way the site was): " url
  if [[ -z "$url" ]]; then
    echo -e "\e[31mThe URL cannot be empty. Please enter a valid URL.\e[0m"
  elif [[ ! "$url" =~ ^https:// ]]; then
    echo -e "\e[31mThe URL must start with https://. We only do secure hosting here.  Please enter a valid URL.\e[0m"
  fi
done


# Prompt for database name/user (no empty input, all lowercase, letters a-z, and numbers 1-9)
while true; do
  read -p "What would you like for a database name/user? (Note: 'wp_' is automatically prepended): " db_name
  if [[ -z "$db_name" ]]; then
    echo -e "\e[31mDatabase name cannot be empty. Please enter a valid name.\e[0m"
  elif [[ ! "$db_name" =~ ^[a-z0-9]+$ ]]; then
    echo -e "\e[31mDatabase name must be all lowercase, containing only letters (a-z) and numbers (1-9). Please enter a valid name.\e[0m"
  else
    db_user="$db_name"
    db_name="wp_$db_name"  # Prefix with 'wp_' after validation
    if check_database "$db_name"; then
      break  # Exit the loop if the database does not exist or was successfully dropped
    else
      echo -e "\e[33mPlease enter a different database name.\e[0m"
    fi
  fi
done


# Generate a 16-character alphanumeric password
db_pass=$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 16)

log_message "Generated variables:"
log_message "Domain: $domain"
log_message "URL: $url"
log_message "Database Name: $db_name"
log_message "Database User: $db_user"
log_message "Database Password: $db_pass"

# Prompt for optional zip file
read -p "URL to zip file containing site (e.g. WP Engine Backup zip file URL): " old_site

# Copy the skeleton and substitute variables
log_message "Setting up the new site..."
cp -a ./.skel ./$domain

sed -i "s|%URL%|$url|g" ./$domain/docker-compose.yml
sed -i "s|%URL%|$url|g" ./$domain/robots.txt
sed -i "s|%DOMAIN%|$domain|g" ./$domain/docker-compose.yml
sed -i "s|%DB_NAME%|$db_name|g" ./$domain/docker-compose.yml
sed -i "s|%DB_USER%|$db_user|g" ./$domain/docker-compose.yml
sed -i "s|%DB_PASS%|$db_pass|g" ./$domain/docker-compose.yml

# If a zip file was provided, download and unzip it
if [[ ! -z "$old_site" ]]; then
  log_message "Downloading and extracting the site from $old_site..."
  wget -O ./$domain/www/site.zip $old_site
  cd ./$domain/www && unzip site.zip && chown -R 33:33 . && cd ../..
fi

# Build and launch the new site
cd ./$domain && docker compose up -d --build

log_message "The site has been successfully set up and launched!"


FROM wordpress:latest


# Switch to root to install required packages
USER root


# Install necessary packages: curl, mariadb-client, and cron
RUN apt-get update && apt-get install -y curl mariadb-client cron  \
    libpng-dev \
    libjpeg-dev \
    libfreetype6-dev \
    libzip-dev \
    zip \
    unzip \
	zlib1g-dev \
	&& pecl install redis \
    && docker-php-ext-configure gd --with-freetype --with-jpeg \
    && docker-php-ext-install gd zip \
    && docker-php-ext-enable redis \
	&& rm -rf /var/lib/apt/lists/*

# Install Redis extension


# Download WP-CLI, rename it, move it to /usr/local/bin, and make it executable
RUN curl -O https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar && \
    mv wp-cli.phar /usr/local/bin/wp && \
    chmod +x /usr/local/bin/wp

RUN mkdir /data && chown www-data:www-data /data


# Create log directory and ensure correct permissions
RUN mkdir -p /var/www/log && \
    touch /var/www/log/wordpress-website.log && \
    chown -R www-data:www-data /var/www && \
    chmod -R 775 /var/www/log && \
    find -type f -exec chmod 644 {} \; && \
    find -type d -exec chmod 755 {} \;


# Copy cron scripts to the container
COPY ./crons /crons


# Copy WP Rocket Settings
COPY ./wp_rocket_settings.json /wp_rocket_settings.json

# Ensure the cron jobs are executable and cron.d files are properly set
RUN chmod +x /crons/* && chmod 0644 /etc/cron.d/*


# Create crons directory and setup cron jobs in /etc/cron.d/
RUN echo "0 2 * * * root /crons/nightly.sh >> /var/log/nightly.log 2>&1" > /etc/cron.d/nightly && \
    echo "0 2 * * 0 root /crons/weekly.sh >> /var/log/weekly.log 2>&1" > /etc/cron.d/weekly && \
    echo "0 2 1 * * root /crons/monthly.sh >> /var/log/monthly.log 2>&1" > /etc/cron.d/monthly


# Copy the container-init script and make it executable
COPY container-init.sh /usr/local/bin/container-init.sh
RUN chmod +x /usr/local/bin/container-init.sh && chown www-data:www-data /usr/local/bin/container-init.sh


# Start cron service
RUN service cron start


# Switch to www-data for the rest
USER www-data


# Set the entrypoint script
ENTRYPOINT ["/usr/local/bin/container-init.sh"]

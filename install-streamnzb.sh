#!/bin/bash

#===============================================================================
# StreamNZB VPS Installation Script v2.2
# With SSL/TLS via Cloudflare DNS API (Full Strict) or Let's Encrypt
#
# Features:
#   - Detect existing installation (Update/Reinstall)
#   - DNS check before SSL certificate
#   - Better validation (Email, Domain)
#   - Cloudflare DNS API support (Full Strict SSL - most secure)
#   - End-to-end encryption
#===============================================================================

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# Installation directory (global)
INSTALL_DIR="/opt/streamnzb"
DATA_DIR="${INSTALL_DIR}/data"
CADDY_DIR="${INSTALL_DIR}/caddy"
CONFIG_FILE="${INSTALL_DIR}/.install-config"

#===============================================================================
# Helper Functions
#===============================================================================

print_banner() {
    printf "${CYAN}"
    printf "╔═══════════════════════════════════════════════════════════════════╗\n"
    printf "║           StreamNZB VPS Installation Script v2.2                 ║\n"
    printf "║     End-to-End SSL via Cloudflare DNS API or Let's Encrypt       ║\n"
    printf "╚═══════════════════════════════════════════════════════════════════╝\n"
    printf "${NC}\n"
}

print_section() {
    printf "\n${BLUE}=== $1 ===${NC}\n"
}

print_success() {
    printf "${GREEN}✓ $1${NC}\n"
}

print_error() {
    printf "${RED}✗ $1${NC}\n"
}

print_warning() {
    printf "${YELLOW}⚠ $1${NC}\n"
}

print_info() {
    printf "${CYAN}ℹ $1${NC}\n"
}

# Domain validation (RFC 1035 compliant)
validate_domain() {
    local domain="$1"
    
    if [ -z "$domain" ]; then
        return 1
    fi
    
    if [ ${#domain} -gt 253 ]; then
        return 1
    fi
    
    if echo "$domain" | grep -qE '^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$'; then
        return 0
    fi
    
    return 1
}

# Email validation (RFC 5322 simplified)
validate_email() {
    local email="$1"
    
    if [ -z "$email" ]; then
        return 1
    fi
    
    if [ ${#email} -gt 254 ]; then
        return 1
    fi
    
    if echo "$email" | grep -qE '^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$'; then
        if echo "$email" | grep -qE '\.\.'; then
            return 1
        fi
        if echo "$email" | grep -qE '^@|@$'; then
            return 1
        fi
        return 0
    fi
    
    return 1
}

# Get server IPv4
get_server_ipv4() {
    local ip=""
    
    ip=$(curl -4 -s --connect-timeout 5 ifconfig.me 2>/dev/null)
    
    if [ -z "$ip" ]; then
        ip=$(curl -4 -s --connect-timeout 5 ipinfo.io/ip 2>/dev/null)
    fi
    
    if [ -z "$ip" ]; then
        ip=$(curl -4 -s --connect-timeout 5 icanhazip.com 2>/dev/null)
    fi
    
    if [ -z "$ip" ]; then
        ip=$(hostname -I | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -n1)
    fi
    
    echo "$ip"
}

# Get server IPv6
get_server_ipv6() {
    local ip=""
    
    ip=$(curl -6 -s --connect-timeout 5 ifconfig.me 2>/dev/null)
    
    if [ -z "$ip" ]; then
        ip=$(curl -6 -s --connect-timeout 5 icanhazip.com 2>/dev/null)
    fi
    
    if [ -z "$ip" ]; then
        ip=$(hostname -I | tr ' ' '\n' | grep -E ':' | head -n1)
    fi
    
    echo "$ip"
}

# Verify Cloudflare API Token
verify_cloudflare_token() {
    local token="$1"
    
    local response=$(curl -s -X GET "https://api.cloudflare.com/client/v4/user/tokens/verify" \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json")
    
    if echo "$response" | grep -q '"success":true'; then
        return 0
    fi
    return 1
}

# Get Cloudflare Zone ID for domain
get_cloudflare_zone_id() {
    local token="$1"
    local domain="$2"
    
    # Extract root domain (last two parts)
    local root_domain=$(echo "$domain" | awk -F. '{print $(NF-1)"."$NF}')
    
    local response=$(curl -s -X GET "https://api.cloudflare.com/client/v4/zones?name=${root_domain}" \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json")
    
    local zone_id=$(echo "$response" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
    
    echo "$zone_id"
}

# DNS check for Cloudflare (just verify it resolves)
check_dns_cloudflare() {
    local domain="$1"
    
    print_info "Checking DNS for ${domain}..."
    
    local resolved_ipv4=""
    if command -v dig > /dev/null 2>&1; then
        resolved_ipv4=$(dig +short "$domain" A 2>/dev/null | head -n1)
    elif command -v host > /dev/null 2>&1; then
        resolved_ipv4=$(host -t A "$domain" 2>/dev/null | grep "has address" | head -n1 | awk '{print $NF}')
    fi
    
    if [ -n "$resolved_ipv4" ]; then
        printf "  Domain resolves to: ${YELLOW}${resolved_ipv4}${NC}\n"
        print_success "DNS configured!"
        return 0
    fi
    
    print_error "Could not resolve DNS for ${domain}!"
    return 1
}

# DNS check for direct connection (not Cloudflare)
check_dns_direct() {
    local domain="$1"
    local server_ipv4="$2"
    local server_ipv6="$3"
    
    print_info "Checking DNS for ${domain}..."
    
    local resolved_ipv4=""
    if command -v dig > /dev/null 2>&1; then
        resolved_ipv4=$(dig +short "$domain" A 2>/dev/null | head -n1)
    elif command -v host > /dev/null 2>&1; then
        resolved_ipv4=$(host -t A "$domain" 2>/dev/null | grep "has address" | head -n1 | awk '{print $NF}')
    fi
    
    local resolved_ipv6=""
    if command -v dig > /dev/null 2>&1; then
        resolved_ipv6=$(dig +short "$domain" AAAA 2>/dev/null | head -n1)
    elif command -v host > /dev/null 2>&1; then
        resolved_ipv6=$(host -t AAAA "$domain" 2>/dev/null | grep "has IPv6 address" | head -n1 | awk '{print $NF}')
    fi
    
    if [ -n "$resolved_ipv4" ]; then
        printf "  Domain A record:    ${YELLOW}${resolved_ipv4}${NC}\n"
    fi
    if [ -n "$resolved_ipv6" ]; then
        printf "  Domain AAAA record: ${YELLOW}${resolved_ipv6}${NC}\n"
    fi
    if [ -n "$server_ipv4" ]; then
        printf "  Server IPv4:        ${YELLOW}${server_ipv4}${NC}\n"
    fi
    if [ -n "$server_ipv6" ]; then
        printf "  Server IPv6:        ${YELLOW}${server_ipv6}${NC}\n"
    fi
    
    if [ -z "$resolved_ipv4" ] && [ -z "$resolved_ipv6" ]; then
        print_error "Could not resolve DNS for ${domain}!"
        return 1
    fi
    
    if [ -n "$resolved_ipv4" ] && [ -n "$server_ipv4" ] && [ "$resolved_ipv4" = "$server_ipv4" ]; then
        print_success "DNS correctly configured! (IPv4 match)"
        return 0
    fi
    
    if [ -n "$resolved_ipv6" ] && [ -n "$server_ipv6" ] && [ "$resolved_ipv6" = "$server_ipv6" ]; then
        print_success "DNS correctly configured! (IPv6 match)"
        return 0
    fi
    
    print_error "DNS does not point to this server!"
    return 1
}

# Check if installation exists
check_existing_installation() {
    if [ -d "$INSTALL_DIR" ] && [ -f "${INSTALL_DIR}/docker-compose.yml" ]; then
        return 0
    fi
    return 1
}

# Load existing configuration
load_existing_config() {
    if [ -f "$CONFIG_FILE" ]; then
        . "$CONFIG_FILE"
        return 0
    fi
    return 1
}

# Save configuration
save_config() {
    cat > "$CONFIG_FILE" << EOF
# StreamNZB Installation Config
# Created: $(date)
DOMAIN="${DOMAIN}"
EMAIL="${EMAIL}"
SECURITY_TOKEN="${SECURITY_TOKEN}"
TIMEZONE="${TIMEZONE}"
SSL_MODE="${SSL_MODE}"
CF_API_TOKEN="${CF_API_TOKEN}"
INSTALL_DATE="$(date +%Y-%m-%d)"
EOF
    chmod 600 "$CONFIG_FILE"
}

#===============================================================================
# Main Program
#===============================================================================

print_banner

# Root check
if [ "$(id -u)" -ne 0 ]; then 
    print_error "Please run as root:"
    printf "  ${YELLOW}sudo bash install-streamnzb.sh${NC}\n"
    exit 1
fi

# OS check
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS_NAME="$ID"
    OS_VERSION="$VERSION_ID"
    print_info "Detected OS: ${OS_NAME} ${OS_VERSION}"
else
    print_warning "Could not detect OS, continuing anyway..."
    OS_NAME="unknown"
fi

#===============================================================================
# Check for Existing Installation
#===============================================================================

INSTALL_MODE="fresh"

if check_existing_installation; then
    printf "\n${YELLOW}╔═══════════════════════════════════════════════════════════════════╗${NC}\n"
    printf "${YELLOW}║              Existing installation found!                         ║${NC}\n"
    printf "${YELLOW}╚═══════════════════════════════════════════════════════════════════╝${NC}\n\n"
    
    if load_existing_config; then
        print_info "Saved configuration:"
        printf "  Domain:         ${GREEN}${DOMAIN}${NC}\n"
        printf "  Email:          ${GREEN}${EMAIL}${NC}\n"
        printf "  Security Token: ${GREEN}${SECURITY_TOKEN}${NC}\n"
        printf "  SSL Mode:       ${GREEN}${SSL_MODE:-caddy}${NC}\n"
        printf "  Installed on:   ${GREEN}${INSTALL_DATE:-unknown}${NC}\n"
    fi
    
    printf "\n${BOLD}What would you like to do?${NC}\n"
    printf "  ${CYAN}1)${NC} Update - Update containers (keeps configuration)\n"
    printf "  ${CYAN}2)${NC} Reinstall - Fresh install (keeps data, new configuration)\n"
    printf "  ${CYAN}3)${NC} Cancel\n"
    printf "\n"
    
    while true; do
        printf "${YELLOW}Choice [1-3]: ${NC}"
        read CHOICE
        case "$CHOICE" in
            1)
                INSTALL_MODE="update"
                break
                ;;
            2)
                INSTALL_MODE="reinstall"
                break
                ;;
            3)
                print_info "Installation cancelled."
                exit 0
                ;;
            *)
                print_error "Invalid choice. Please enter 1, 2, or 3."
                ;;
        esac
    done
fi

#===============================================================================
# Update Mode (quick)
#===============================================================================

if [ "$INSTALL_MODE" = "update" ]; then
    print_section "Update StreamNZB"
    
    cd "$INSTALL_DIR"
    
    print_info "Stopping containers..."
    docker compose down
    
    print_info "Pulling latest images..."
    docker compose pull
    
    print_info "Starting containers..."
    docker compose up -d
    
    sleep 5
    
    if docker compose ps | grep -q "running"; then
        print_success "Update successful!"
        printf "\n${GREEN}StreamNZB is running at: https://${DOMAIN}/${SECURITY_TOKEN}/${NC}\n\n"
    else
        print_error "Error during startup. Check with: docker compose logs"
    fi
    
    exit 0
fi

#===============================================================================
# SSL Mode Selection
#===============================================================================

print_section "SSL Configuration"

printf "${BOLD}Choose SSL method:${NC}\n\n"

printf "  ${CYAN}1)${NC} ${GREEN}Cloudflare DNS API${NC} ${YELLOW}(Recommended - Most Secure)${NC}\n"
printf "     - End-to-end encryption (Full Strict)\n"
printf "     - DDoS protection, caching, hidden IP\n"
printf "     - Automatic certificate via DNS challenge\n"
printf "     - Requires: Cloudflare API Token\n"
printf "\n"
printf "  ${CYAN}2)${NC} ${GREEN}Caddy + Let's Encrypt (HTTP Challenge)${NC}\n"
printf "     - Direct SSL certificate\n"
printf "     - No Cloudflare needed\n"
printf "     - Server IP exposed\n"
printf "     - Requires: DNS pointing directly to server, Port 80 open\n"
printf "\n"

while true; do
    printf "${YELLOW}Choice [1-2]: ${NC}"
    read SSL_CHOICE
    case "$SSL_CHOICE" in
        1)
            SSL_MODE="cloudflare"
            print_success "Using Cloudflare DNS API mode (Full Strict)"
            break
            ;;
        2)
            SSL_MODE="letsencrypt"
            print_success "Using Caddy + Let's Encrypt mode"
            break
            ;;
        *)
            print_error "Invalid choice. Please enter 1 or 2."
            ;;
    esac
done

#===============================================================================
# Interactive Configuration
#===============================================================================

print_section "Configuration"

# For reinstall: offer existing values as defaults
if [ "$INSTALL_MODE" = "reinstall" ] && [ -n "$DOMAIN" ]; then
    DEFAULT_DOMAIN="$DOMAIN"
    DEFAULT_EMAIL="$EMAIL"
    DEFAULT_TOKEN="$SECURITY_TOKEN"
    DEFAULT_TZ="$TIMEZONE"
    DEFAULT_CF_TOKEN="$CF_API_TOKEN"
else
    DEFAULT_DOMAIN=""
    DEFAULT_EMAIL=""
    DEFAULT_TOKEN=""
    DEFAULT_TZ="UTC"
    DEFAULT_CF_TOKEN=""
fi

# Ask for domain
while true; do
    if [ -n "$DEFAULT_DOMAIN" ]; then
        printf "${YELLOW}Domain for StreamNZB [${DEFAULT_DOMAIN}]: ${NC}"
    else
        printf "${YELLOW}Domain for StreamNZB (e.g. streamnzb.example.com): ${NC}"
    fi
    read INPUT_DOMAIN
    
    if [ -z "$INPUT_DOMAIN" ] && [ -n "$DEFAULT_DOMAIN" ]; then
        DOMAIN="$DEFAULT_DOMAIN"
    else
        DOMAIN="$INPUT_DOMAIN"
    fi
    
    if [ -z "$DOMAIN" ]; then
        print_error "Domain cannot be empty!"
    elif ! validate_domain "$DOMAIN"; then
        print_error "Invalid domain!"
        print_info "Example: streamnzb.example.com"
    else
        print_success "Domain accepted: ${DOMAIN}"
        break
    fi
done

# Ask for email
printf "\n"
while true; do
    if [ -n "$DEFAULT_EMAIL" ]; then
        printf "${YELLOW}Email for SSL certificate [${DEFAULT_EMAIL}]: ${NC}"
    else
        printf "${YELLOW}Email for SSL certificate: ${NC}"
    fi
    read INPUT_EMAIL
    
    if [ -z "$INPUT_EMAIL" ] && [ -n "$DEFAULT_EMAIL" ]; then
        EMAIL="$DEFAULT_EMAIL"
    else
        EMAIL="$INPUT_EMAIL"
    fi
    
    if [ -z "$EMAIL" ]; then
        print_error "Email cannot be empty!"
    elif ! validate_email "$EMAIL"; then
        print_error "Invalid email address!"
        print_info "Example: admin@example.com"
    else
        print_success "Email accepted: ${EMAIL}"
        break
    fi
done

# Ask for Cloudflare API Token if using Cloudflare mode
if [ "$SSL_MODE" = "cloudflare" ]; then
    printf "\n"
    printf "${CYAN}╔═══════════════════════════════════════════════════════════════════╗${NC}\n"
    printf "${CYAN}║                   Cloudflare API Token Setup                      ║${NC}\n"
    printf "${CYAN}╚═══════════════════════════════════════════════════════════════════╝${NC}\n"
    printf "\n"
    printf "To create a Cloudflare API Token:\n"
    printf "  1. Go to: ${GREEN}https://dash.cloudflare.com/profile/api-tokens${NC}\n"
    printf "  2. Click: ${GREEN}Create Token${NC}\n"
    printf "  3. Use template: ${GREEN}Edit zone DNS${NC}\n"
    printf "  4. Zone Resources: ${GREEN}Include > Specific zone > your-domain.com${NC}\n"
    printf "  5. Click: ${GREEN}Continue to summary > Create Token${NC}\n"
    printf "  6. Copy the token (shown only once!)\n"
    printf "\n"
    
    while true; do
        if [ -n "$DEFAULT_CF_TOKEN" ]; then
            # Mask existing token
            MASKED_TOKEN="${DEFAULT_CF_TOKEN:0:8}...${DEFAULT_CF_TOKEN: -4}"
            printf "${YELLOW}Cloudflare API Token [${MASKED_TOKEN}]: ${NC}"
        else
            printf "${YELLOW}Cloudflare API Token: ${NC}"
        fi
        read INPUT_CF_TOKEN
        
        if [ -z "$INPUT_CF_TOKEN" ] && [ -n "$DEFAULT_CF_TOKEN" ]; then
            CF_API_TOKEN="$DEFAULT_CF_TOKEN"
        else
            CF_API_TOKEN="$INPUT_CF_TOKEN"
        fi
        
        if [ -z "$CF_API_TOKEN" ]; then
            print_error "API Token cannot be empty!"
            continue
        fi
        
        # Verify token
        printf "  Verifying token... "
        if verify_cloudflare_token "$CF_API_TOKEN"; then
            printf "${GREEN}Valid!${NC}\n"
            print_success "Cloudflare API Token verified"
            break
        else
            printf "${RED}Invalid!${NC}\n"
            print_error "Token verification failed. Please check your token."
        fi
    done
    
    # Get Zone ID
    printf "  Looking up Zone ID... "
    CF_ZONE_ID=$(get_cloudflare_zone_id "$CF_API_TOKEN" "$DOMAIN")
    
    if [ -n "$CF_ZONE_ID" ]; then
        printf "${GREEN}Found!${NC}\n"
        print_success "Zone ID: ${CF_ZONE_ID}"
    else
        printf "${RED}Not found!${NC}\n"
        print_error "Could not find Zone ID for domain."
        print_info "Make sure the domain is added to your Cloudflare account."
        exit 1
    fi
fi

# Ask for security token
printf "\n"
if [ -n "$DEFAULT_TOKEN" ]; then
    printf "${YELLOW}Security Token [${DEFAULT_TOKEN}]: ${NC}"
else
    printf "${YELLOW}Security Token (empty = auto-generate): ${NC}"
fi
read INPUT_TOKEN

if [ -z "$INPUT_TOKEN" ]; then
    if [ -n "$DEFAULT_TOKEN" ]; then
        SECURITY_TOKEN="$DEFAULT_TOKEN"
    else
        SECURITY_TOKEN=$(openssl rand -hex 16)
        print_success "Generated token: ${SECURITY_TOKEN}"
    fi
else
    SECURITY_TOKEN="$INPUT_TOKEN"
fi

# Ask for timezone
printf "\n"
printf "${YELLOW}Timezone [${DEFAULT_TZ}]: ${NC}"
read INPUT_TZ
if [ -z "$INPUT_TZ" ]; then
    TIMEZONE="$DEFAULT_TZ"
else
    TIMEZONE="$INPUT_TZ"
fi

#===============================================================================
# DNS Check
#===============================================================================

print_section "DNS Check"

SERVER_IPV4=$(get_server_ipv4)
SERVER_IPV6=$(get_server_ipv6)

if [ -z "$SERVER_IPV4" ] && [ -z "$SERVER_IPV6" ]; then
    print_error "Could not determine server IP!"
    exit 1
fi

if [ -n "$SERVER_IPV4" ]; then
    print_info "Server IPv4: ${SERVER_IPV4}"
fi
if [ -n "$SERVER_IPV6" ]; then
    print_info "Server IPv6: ${SERVER_IPV6}"
fi

DNS_OK=false

if [ "$SSL_MODE" = "cloudflare" ]; then
    if check_dns_cloudflare "$DOMAIN"; then
        DNS_OK=true
    fi
else
    if check_dns_direct "$DOMAIN" "$SERVER_IPV4" "$SERVER_IPV6"; then
        DNS_OK=true
    fi
fi

if [ "$DNS_OK" = false ]; then
    printf "\n"
    print_warning "DNS check failed!"
    printf "\n"
    printf "${YELLOW}Continue anyway? [y/N]: ${NC}"
    read DNS_CONTINUE
    case "$DNS_CONTINUE" in
        y|Y|yes|YES)
            print_warning "Continuing without verified DNS..."
            ;;
        *)
            printf "\n"
            if [ "$SSL_MODE" = "cloudflare" ]; then
                print_info "Please configure Cloudflare DNS:"
                printf "  1. Add A record: ${CYAN}${DOMAIN}${NC} -> ${CYAN}${SERVER_IPV4:-$SERVER_IPV6}${NC}\n"
                printf "  2. Proxy status: ${CYAN}Proxied (orange cloud)${NC}\n"
            else
                print_info "Please configure DNS A record:"
                printf "  ${CYAN}Domain:${NC} ${DOMAIN}\n"
                printf "  ${CYAN}Type:${NC}   A\n"
                printf "  ${CYAN}Value:${NC}  ${SERVER_IPV4:-$SERVER_IPV6}\n"
            fi
            printf "\n"
            print_info "After DNS change, wait 5-10 minutes and run the script again."
            exit 1
            ;;
    esac
fi

#===============================================================================
# Summary
#===============================================================================

print_section "Summary"

printf "  Domain:         ${GREEN}${DOMAIN}${NC}\n"
printf "  Email:          ${GREEN}${EMAIL}${NC}\n"
printf "  Security Token: ${GREEN}${SECURITY_TOKEN}${NC}\n"
printf "  Timezone:       ${GREEN}${TIMEZONE}${NC}\n"
printf "  SSL Mode:       ${GREEN}${SSL_MODE}${NC}\n"
if [ "$SSL_MODE" = "cloudflare" ]; then
    printf "  Cloudflare:     ${GREEN}Zone ID verified${NC}\n"
fi
if [ -n "$SERVER_IPV4" ]; then
    printf "  Server IPv4:    ${GREEN}${SERVER_IPV4}${NC}\n"
fi
if [ -n "$SERVER_IPV6" ]; then
    printf "  Server IPv6:    ${GREEN}${SERVER_IPV6}${NC}\n"
fi
printf "  DNS Status:     "
if [ "$DNS_OK" = true ]; then
    printf "${GREEN}OK${NC}\n"
else
    printf "${YELLOW}WARNING${NC}\n"
fi
printf "  Install Mode:   ${GREEN}${INSTALL_MODE}${NC}\n"
printf "\n"

printf "${YELLOW}Start installation? [y/N]: ${NC}"
read CONFIRM
case "$CONFIRM" in
    y|Y|yes|YES) ;;
    *)
        print_info "Installation cancelled."
        exit 0
        ;;
esac

#===============================================================================
# System Update and Dependencies
#===============================================================================

print_section "System Update"
apt-get update
apt-get upgrade -y

print_section "Installing Dependencies"
apt-get install -y \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg \
    lsb-release \
    ufw \
    fail2ban \
    dnsutils

#===============================================================================
# Docker Installation
#===============================================================================

print_section "Docker Installation"

if command -v docker > /dev/null 2>&1; then
    print_success "Docker is already installed."
    docker --version
else
    print_info "Installing Docker..."
    
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc

    echo \
      "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
      $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
      tee /etc/apt/sources.list.d/docker.list > /dev/null

    apt-get update
    apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

    systemctl start docker
    systemctl enable docker
    
    print_success "Docker successfully installed."
fi

#===============================================================================
# Create Directories
#===============================================================================

print_section "Creating Directories"
mkdir -p "${DATA_DIR}"
mkdir -p "${CADDY_DIR}/data"
mkdir -p "${CADDY_DIR}/config"
mkdir -p "${INSTALL_DIR}/logs/caddy"
print_success "Directories created."

#===============================================================================
# Save Configuration
#===============================================================================

save_config
print_success "Configuration saved."

#===============================================================================
# Create Docker Compose and Config based on SSL Mode
#===============================================================================

if [ "$SSL_MODE" = "cloudflare" ]; then
    #---------------------------------------------------------------------------
    # Cloudflare DNS API Mode: Full Strict SSL with DNS Challenge
    #---------------------------------------------------------------------------
    
    print_section "Caddy Configuration (Cloudflare DNS API)"
    
    cat > "${CADDY_DIR}/Caddyfile" << EOF
{
    email ${EMAIL}
    acme_ca https://acme-v02.api.letsencrypt.org/directory
    acme_dns cloudflare {env.CF_API_TOKEN}
}

${DOMAIN} {
    reverse_proxy streamnzb:7000

    header {
        X-Content-Type-Options nosniff
        X-Frame-Options DENY
        X-XSS-Protection "1; mode=block"
        Referrer-Policy strict-origin-when-cross-origin
        Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
        -Server
    }

    log {
        output file /var/log/caddy/access.log {
            roll_size 10mb
            roll_keep 5
        }
    }
}
EOF

    print_success "Caddyfile created (Cloudflare DNS API mode)."
    
    print_section "Docker Compose (Cloudflare DNS API)"
    
    # We need caddy with cloudflare plugin - use custom build
    cat > "${INSTALL_DIR}/docker-compose.yml" << EOF
version: '3.8'

services:
  streamnzb:
    image: ghcr.io/gaisberg/streamnzb:latest
    container_name: streamnzb
    restart: unless-stopped
    environment:
      - TZ=${TIMEZONE}
      - SECURITY_TOKEN=${SECURITY_TOKEN}
    volumes:
      - ${DATA_DIR}:/app/data
    networks:
      - streamnzb-network
    expose:
      - "7000"
    ports:
      - "1119:1119"

  caddy:
    image: slothcroissant/caddy-cloudflaredns:latest
    container_name: caddy
    restart: unless-stopped
    environment:
      - CF_API_TOKEN=${CF_API_TOKEN}
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - ${CADDY_DIR}/Caddyfile:/etc/caddy/Caddyfile:ro
      - ${CADDY_DIR}/data:/data
      - ${CADDY_DIR}/config:/config
      - ${INSTALL_DIR}/logs/caddy:/var/log/caddy
    networks:
      - streamnzb-network
    depends_on:
      - streamnzb

networks:
  streamnzb-network:
    driver: bridge
EOF

    print_success "docker-compose.yml created (Cloudflare DNS API mode)."

else
    #---------------------------------------------------------------------------
    # Let's Encrypt Mode: HTTP Challenge
    #---------------------------------------------------------------------------
    
    print_section "Caddy Configuration (Let's Encrypt)"
    
    cat > "${CADDY_DIR}/Caddyfile" << EOF
{
    email ${EMAIL}
    acme_ca https://acme-v02.api.letsencrypt.org/directory
}

${DOMAIN} {
    reverse_proxy streamnzb:7000

    header {
        X-Content-Type-Options nosniff
        X-Frame-Options DENY
        X-XSS-Protection "1; mode=block"
        Referrer-Policy strict-origin-when-cross-origin
        Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
        -Server
    }

    log {
        output file /var/log/caddy/access.log {
            roll_size 10mb
            roll_keep 5
        }
    }
}
EOF

    print_success "Caddyfile created (Let's Encrypt mode)."
    
    print_section "Docker Compose (Let's Encrypt)"
    
    cat > "${INSTALL_DIR}/docker-compose.yml" << EOF
version: '3.8'

services:
  streamnzb:
    image: ghcr.io/gaisberg/streamnzb:latest
    container_name: streamnzb
    restart: unless-stopped
    environment:
      - TZ=${TIMEZONE}
      - SECURITY_TOKEN=${SECURITY_TOKEN}
    volumes:
      - ${DATA_DIR}:/app/data
    networks:
      - streamnzb-network
    expose:
      - "7000"
    ports:
      - "1119:1119"

  caddy:
    image: caddy:2-alpine
    container_name: caddy
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - ${CADDY_DIR}/Caddyfile:/etc/caddy/Caddyfile:ro
      - ${CADDY_DIR}/data:/data
      - ${CADDY_DIR}/config:/config
      - ${INSTALL_DIR}/logs/caddy:/var/log/caddy
    networks:
      - streamnzb-network
    depends_on:
      - streamnzb

networks:
  streamnzb-network:
    driver: bridge
EOF

    print_success "docker-compose.yml created (Let's Encrypt mode)."
fi

#===============================================================================
# Firewall
#===============================================================================

print_section "Firewall Configuration"

ufw --force enable
ufw allow ssh
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow 1119/tcp
ufw reload

print_success "Firewall configured."

#===============================================================================
# Fail2Ban
#===============================================================================

print_section "Fail2Ban Configuration"

cat > /etc/fail2ban/jail.local << EOF
[DEFAULT]
bantime = 3600
findtime = 600
maxretry = 5

[sshd]
enabled = true
port = ssh
filter = sshd
logpath = /var/log/auth.log
maxretry = 3
EOF

systemctl restart fail2ban
systemctl enable fail2ban

print_success "Fail2Ban configured."

#===============================================================================
# Systemd Service
#===============================================================================

print_section "Systemd Service"

cat > /etc/systemd/system/streamnzb.service << EOF
[Unit]
Description=StreamNZB Docker Compose Service
Requires=docker.service
After=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=${INSTALL_DIR}
ExecStart=/usr/bin/docker compose up -d
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=0

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable streamnzb.service

print_success "Systemd service created."

#===============================================================================
# Info File
#===============================================================================

cat > "${INSTALL_DIR}/INFO.txt" << EOF
===============================================================================
                         StreamNZB Installation Info
===============================================================================

Installation:     $(date)
Version:          Script v2.2
SSL Mode:         ${SSL_MODE}

ACCESS DETAILS
-------------------------------------------------------------------------------
Web UI:           https://${DOMAIN}/${SECURITY_TOKEN}/
Manifest URL:     https://${DOMAIN}/${SECURITY_TOKEN}/manifest.json
Security Token:   ${SECURITY_TOKEN}
NNTP Proxy:       ${DOMAIN}:1119

DIRECTORIES
-------------------------------------------------------------------------------
Installation:     ${INSTALL_DIR}
Data:             ${DATA_DIR}
Caddy:            ${CADDY_DIR}
Logs:             ${INSTALL_DIR}/logs

COMMANDS
-------------------------------------------------------------------------------
Status:           systemctl status streamnzb
Logs:             cd ${INSTALL_DIR} && docker compose logs -f
Restart:          systemctl restart streamnzb
Update:           bash install-streamnzb.sh (choose option 1)

EOF

if [ "$SSL_MODE" = "cloudflare" ]; then
    cat >> "${INSTALL_DIR}/INFO.txt" << EOF
CLOUDFLARE SETTINGS (IMPORTANT!)
-------------------------------------------------------------------------------
For end-to-end encryption, configure Cloudflare:

1. Go to: Cloudflare Dashboard > Your Domain > SSL/TLS
2. Set encryption mode to: FULL (STRICT)
3. Under Edge Certificates: Enable "Always Use HTTPS"

This ensures:
- Browser <-> Cloudflare: Encrypted (Cloudflare Edge Certificate)
- Cloudflare <-> Server: Encrypted (Let's Encrypt Certificate via DNS API)

===============================================================================
EOF
else
    cat >> "${INSTALL_DIR}/INFO.txt" << EOF
===============================================================================
EOF
fi

#===============================================================================
# Start Containers
#===============================================================================

print_section "Starting Containers"

cd "${INSTALL_DIR}"
docker compose pull
docker compose up -d

printf "${YELLOW}Waiting for containers to start...${NC}\n"
sleep 15

if docker compose ps | grep -q "running"; then
    print_success "Containers are running!"
    
    # Wait a bit more for certificate generation
    if [ "$SSL_MODE" = "cloudflare" ]; then
        printf "${YELLOW}Waiting for SSL certificate generation (this may take a minute)...${NC}\n"
        sleep 30
    fi
else
    print_error "Error during startup. Check: docker compose logs"
fi

#===============================================================================
# Finish
#===============================================================================

printf "\n"
printf "${GREEN}╔═══════════════════════════════════════════════════════════════════╗${NC}\n"
printf "${GREEN}║                   Installation successful!                        ║${NC}\n"
printf "${GREEN}╚═══════════════════════════════════════════════════════════════════╝${NC}\n"
printf "\n"

if [ "$SSL_MODE" = "cloudflare" ]; then
    printf "${CYAN}╔═══════════════════════════════════════════════════════════════════╗${NC}\n"
    printf "${CYAN}║           IMPORTANT: Configure Cloudflare SSL Settings           ║${NC}\n"
    printf "${CYAN}╚═══════════════════════════════════════════════════════════════════╝${NC}\n"
    printf "\n"
    printf "  1. Go to: ${GREEN}Cloudflare Dashboard > ${DOMAIN} > SSL/TLS${NC}\n"
    printf "  2. Set encryption mode to: ${GREEN}Full (Strict)${NC}\n"
    printf "  3. Enable: ${GREEN}Always Use HTTPS${NC} (under Edge Certificates)\n"
    printf "\n"
    printf "  ${YELLOW}This ensures end-to-end encryption!${NC}\n"
    printf "\n"
fi

printf "${CYAN}StreamNZB URLs:${NC}\n"
printf "  Web UI:    ${GREEN}https://${DOMAIN}/${SECURITY_TOKEN}/${NC}\n"
printf "  Manifest:  ${GREEN}https://${DOMAIN}/${SECURITY_TOKEN}/manifest.json${NC}\n"
printf "\n"
printf "${CYAN}Security Token:${NC} ${GREEN}${SECURITY_TOKEN}${NC}\n"
printf "${CYAN}NNTP Proxy:${NC}     ${GREEN}${DOMAIN}:1119${NC}\n"
printf "${CYAN}Info file:${NC}      ${GREEN}${INSTALL_DIR}/INFO.txt${NC}\n"
printf "\n"
printf "${BOLD}Next steps:${NC}\n"
if [ "$SSL_MODE" = "cloudflare" ]; then
    printf "  1. Configure Cloudflare SSL to Full (Strict)\n"
    printf "  2. Open the Web UI\n"
    printf "  3. Add a Usenet provider\n"
    printf "  4. Add NZBHydra2 or Prowlarr as indexer\n"
    printf "  5. Install the addon in Stremio\n"
else
    printf "  1. Open the Web UI\n"
    printf "  2. Add a Usenet provider\n"
    printf "  3. Add NZBHydra2 or Prowlarr as indexer\n"
    printf "  4. Install the addon in Stremio\n"
fi
printf "\n"

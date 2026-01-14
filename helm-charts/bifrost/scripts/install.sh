#!/bin/bash
# Bifrost Helm Chart Installation Script
# This script helps you install Bifrost with different configurations

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Print colored output
print_info() {
    echo -e "${BLUE}ℹ ${NC}$1"
}

print_success() {
    echo -e "${GREEN}✓ ${NC}$1"
}

print_warning() {
    echo -e "${YELLOW}⚠ ${NC}$1"
}

print_error() {
    echo -e "${RED}✗ ${NC}$1"
}

# Print banner
print_banner() {
    echo ""
    echo -e "${BLUE}╔═══════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║                                           ║${NC}"
    echo -e "${BLUE}║      Bifrost Helm Chart Installer        ║${NC}"
    echo -e "${BLUE}║                                           ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════╝${NC}"
    echo ""
}

# Check prerequisites
check_prerequisites() {
    print_info "Checking prerequisites..."
    
    if ! command -v helm &> /dev/null; then
        print_error "Helm is not installed. Please install Helm 3.2.0 or later."
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        print_error "kubectl is not installed. Please install kubectl."
        exit 1
    fi
    
    # Check kubectl connection
    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot connect to Kubernetes cluster. Please check your kubeconfig."
        exit 1
    fi
    
    print_success "All prerequisites met"
}

# Show menu
show_menu() {
    echo ""
    echo "Select a deployment configuration:"
    echo ""
    echo "  1) SQLite only (simple, local development)"
    echo "  2) PostgreSQL only (production-ready database)"
    echo "  3) PostgreSQL + Weaviate (semantic caching with Weaviate)"
    echo "  4) PostgreSQL + Redis (semantic caching with Redis)"
    echo "  5) SQLite + Weaviate (local dev with semantic caching)"
    echo "  6) SQLite + Redis (local dev with Redis caching)"
    echo "  7) External PostgreSQL (use your own database)"
    echo "  8) Production HA (high-availability setup)"
    echo "  9) Custom (use your own values file)"
    echo ""
    echo "  0) Exit"
    echo ""
}

# Get user input
get_input() {
    read -p "Enter your choice [0-9]: " choice
    case $choice in
        1) CONFIG="sqlite-only" ;;
        2) CONFIG="postgres-only" ;;
        3) CONFIG="postgres-weaviate" ;;
        4) CONFIG="postgres-redis" ;;
        5) CONFIG="sqlite-weaviate" ;;
        6) CONFIG="sqlite-redis" ;;
        7) CONFIG="external-postgres" ;;
        8) CONFIG="production-ha" ;;
        9) CONFIG="custom" ;;
        0) exit 0 ;;
        *) 
            print_error "Invalid choice. Please try again."
            return 1
            ;;
    esac
    return 0
}

# Get release name
get_release_name() {
    read -p "Enter release name (default: bifrost): " RELEASE_NAME
    RELEASE_NAME=${RELEASE_NAME:-bifrost}
}

# Get namespace
get_namespace() {
    read -p "Enter namespace (default: default): " NAMESPACE
    NAMESPACE=${NAMESPACE:-default}
    
    # Check if namespace exists
    if ! kubectl get namespace "$NAMESPACE" &> /dev/null; then
        read -p "Namespace '$NAMESPACE' does not exist. Create it? (y/n): " CREATE_NS
        if [[ "$CREATE_NS" =~ ^[Yy]$ ]]; then
            kubectl create namespace "$NAMESPACE"
            print_success "Namespace '$NAMESPACE' created"
        else
            print_error "Installation aborted"
            exit 1
        fi
    fi
}

# Get custom values file
get_custom_values() {
    read -p "Enter path to custom values file: " CUSTOM_VALUES
    if [[ ! -f "$CUSTOM_VALUES" ]]; then
        print_error "File not found: $CUSTOM_VALUES"
        exit 1
    fi
}

# Install chart
install_chart() {
    local values_file=""
    
    if [[ "$CONFIG" == "custom" ]]; then
        # Validate that CUSTOM_VALUES is non-empty
        if [[ -z "$CUSTOM_VALUES" ]]; then
            print_error "Custom values file path is empty"
            exit 1
        fi
        values_file="$CUSTOM_VALUES"
        # Validate that the custom values file exists and is a regular file
        if [[ ! -f "$values_file" ]]; then
            print_error "Custom values file does not exist or is not a regular file: $values_file"
            exit 1
        fi
    else
        values_file="${CHART_DIR}/values-examples/${CONFIG}.yaml"
        # Validate that the predefined values file exists
        if [[ ! -f "$values_file" ]]; then
            print_error "Values file does not exist: $values_file"
            exit 1
        fi
    fi
    
    print_info "Installing Bifrost..."
    print_info "Release: $RELEASE_NAME"
    print_info "Namespace: $NAMESPACE"
    print_info "Configuration: $CONFIG"
    echo ""
    
    # Ask for confirmation
    read -p "Proceed with installation? (y/n): " CONFIRM
    if [[ ! "$CONFIRM" =~ ^[Yy]$ ]]; then
        print_warning "Installation cancelled"
        exit 0
    fi
    
    # Run helm install with explicit chart directory
    if helm install "$RELEASE_NAME" "$CHART_DIR" \
        --namespace "$NAMESPACE" \
        -f "$values_file" \
        --create-namespace; then
        
        print_success "Bifrost installed successfully!"
        echo ""
        print_info "To check the status:"
        echo "  helm status $RELEASE_NAME -n $NAMESPACE"
        echo ""
        print_info "To get the application URL:"
        echo "  kubectl --namespace $NAMESPACE port-forward svc/$RELEASE_NAME 8080:8080"
        echo "  Then visit: http://localhost:8080"
        echo ""
        print_info "To view logs:"
        echo "  kubectl logs -l app.kubernetes.io/name=bifrost -n $NAMESPACE -f"
        echo ""
    else
        print_error "Installation failed"
        exit 1
    fi
}

# Main function
main() {
    print_banner
    check_prerequisites
    
    while true; do
        show_menu
        if get_input; then
            break
        fi
    done
    
    get_release_name
    get_namespace
    
    if [[ "$CONFIG" == "custom" ]]; then
        get_custom_values
    fi
    
    # Set explicit chart directory (parent of scripts directory)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    CHART_DIR="$SCRIPT_DIR/.."
    
    install_chart
}

# Run main function
main


#!/bin/bash
# Bifrost Values File Generator
# This interactive script helps you generate a custom values.yaml file

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_info() { echo -e "${BLUE}ℹ ${NC}$1"; }
print_success() { echo -e "${GREEN}✓ ${NC}$1"; }
print_warning() { echo -e "${YELLOW}⚠ ${NC}$1"; }
print_error() { echo -e "${RED}✗ ${NC}$1"; }

print_banner() {
    echo ""
    echo -e "${BLUE}╔═══════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║                                           ║${NC}"
    echo -e "${BLUE}║     Bifrost Values Generator             ║${NC}"
    echo -e "${BLUE}║                                           ║${NC}"
    echo -e "${BLUE}╚═══════════════════════════════════════════╝${NC}"
    echo ""
}

print_banner

OUTPUT_FILE="my-values.yaml"

# Storage mode
echo "1. Select storage mode:"
echo "   1) SQLite (simple, single node)"
echo "   2) PostgreSQL (production, scalable)"
read -p "Choice [1-2]: " storage_choice

case $storage_choice in
    1) STORAGE_MODE="sqlite" ;;
    2) STORAGE_MODE="postgres" ;;
    *) print_error "Invalid choice"; exit 1 ;;
esac

# Vector store
echo ""
echo "2. Do you need vector store for semantic caching?"
read -p "Enable vector store? (y/n): " vector_choice

if [[ "$vector_choice" =~ ^[Yy]$ ]]; then
    echo "   1) Weaviate"
    echo "   2) Redis"
    echo "   3) Qdrant"
    read -p "Choice [1-3]: " vector_type_choice
    case $vector_type_choice in
        1) VECTOR_TYPE="weaviate" ;;
        2) VECTOR_TYPE="redis" ;;
        3) VECTOR_TYPE="qdrant" ;;
        *) print_error "Invalid choice"; exit 1 ;;
    esac
    VECTOR_ENABLED="true"
else
    VECTOR_ENABLED="false"
    VECTOR_TYPE="none"
fi

# Deployment type
echo ""
echo "3. Deployment type:"
echo "   1) Development (1 replica, minimal resources)"
echo "   2) Production (3+ replicas, auto-scaling)"
read -p "Choice [1-2]: " deploy_choice

case $deploy_choice in
    1)
        REPLICAS="1"
        AUTOSCALING="false"
        CPU_REQUEST="250m"
        MEM_REQUEST="256Mi"
        CPU_LIMIT="1000m"
        MEM_LIMIT="1Gi"
        ;;
    2)
        REPLICAS="3"
        AUTOSCALING="true"
        CPU_REQUEST="1000m"
        MEM_REQUEST="1Gi"
        CPU_LIMIT="4000m"
        MEM_LIMIT="4Gi"
        ;;
    *) print_error "Invalid choice"; exit 1 ;;
esac

# Ingress
echo ""
read -p "4. Do you want to enable Ingress? (y/n): " ingress_choice
if [[ "$ingress_choice" =~ ^[Yy]$ ]]; then
    INGRESS_ENABLED="true"
    read -p "   Enter your domain (e.g., bifrost.yourdomain.com): " DOMAIN
else
    INGRESS_ENABLED="false"
    DOMAIN="bifrost.local"
fi

# Encryption key
echo ""
read -p "5. Enter encryption key (leave empty to skip): " ENCRYPTION_KEY

# Check if output file already exists
if [[ -f "$OUTPUT_FILE" ]]; then
    echo ""
    print_warning "File '$OUTPUT_FILE' already exists."
    read -p "Do you want to overwrite it? (y/n): " overwrite_choice
    if [[ ! "$overwrite_choice" =~ ^[Yy]$ ]]; then
        print_info "Generation aborted. No files were modified."
        exit 0
    fi
fi

# Generate the file
print_info "Generating values file..."

cat > "$OUTPUT_FILE" <<EOF
# Generated Bifrost values file
# Generated on: $(date)

# Deployment configuration
replicaCount: ${REPLICAS}

autoscaling:
  enabled: ${AUTOSCALING}
  minReplicas: 3
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70

resources:
  limits:
    cpu: ${CPU_LIMIT}
    memory: ${MEM_LIMIT}
  requests:
    cpu: ${CPU_REQUEST}
    memory: ${MEM_REQUEST}

# Storage configuration
storage:
  mode: ${STORAGE_MODE}
EOF

if [[ "$STORAGE_MODE" == "sqlite" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
  persistence:
    enabled: true
    size: 10Gi
EOF
fi

cat >> "$OUTPUT_FILE" <<EOF
  configStore:
    enabled: true
  logsStore:
    enabled: true

EOF

# PostgreSQL configuration
if [[ "$STORAGE_MODE" == "postgres" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
# PostgreSQL configuration
postgresql:
  enabled: true
  auth:
    username: bifrost
    password: "CHANGE_ME_SECURE_PASSWORD"
    database: bifrost
  primary:
    persistence:
      enabled: true
      size: 20Gi
    resources:
      limits:
        cpu: 1000m
        memory: 2Gi
      requests:
        cpu: 500m
        memory: 1Gi

EOF
else
    cat >> "$OUTPUT_FILE" <<EOF
# PostgreSQL disabled (using SQLite)
postgresql:
  enabled: false

EOF
fi

# Vector store configuration
cat >> "$OUTPUT_FILE" <<EOF
# Vector store configuration
vectorStore:
  enabled: ${VECTOR_ENABLED}
  type: ${VECTOR_TYPE}
EOF

if [[ "$VECTOR_TYPE" == "weaviate" ]] && [[ "$VECTOR_ENABLED" == "true" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
  weaviate:
    enabled: true
    replicas: 1
    persistence:
      enabled: true
      size: 10Gi
    resources:
      limits:
        cpu: 1000m
        memory: 2Gi
      requests:
        cpu: 500m
        memory: 1Gi
EOF
elif [[ "$VECTOR_TYPE" == "redis" ]] && [[ "$VECTOR_ENABLED" == "true" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
  redis:
    enabled: true
    auth:
      enabled: true
      password: "CHANGE_ME_REDIS_PASSWORD"
    master:
      persistence:
        enabled: true
        size: 8Gi
      resources:
        limits:
          cpu: 500m
          memory: 512Mi
        requests:
          cpu: 250m
          memory: 256Mi
EOF
elif [[ "$VECTOR_TYPE" == "qdrant" ]] && [[ "$VECTOR_ENABLED" == "true" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
  qdrant:
    enabled: true
    persistence:
      enabled: true
      size: 10Gi
    resources:
      limits:
        cpu: 1000m
        memory: 2Gi
      requests:
        cpu: 500m
        memory: 1Gi
EOF
fi

# Ingress
cat >> "$OUTPUT_FILE" <<EOF

# Ingress configuration
ingress:
  enabled: ${INGRESS_ENABLED}
EOF

if [[ "$INGRESS_ENABLED" == "true" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
  className: "nginx"
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
  hosts:
    - host: ${DOMAIN}
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: bifrost-tls
      hosts:
        - ${DOMAIN}
EOF
fi

# Bifrost configuration
cat >> "$OUTPUT_FILE" <<EOF

# Bifrost application configuration
bifrost:
EOF

if [[ -n "$ENCRYPTION_KEY" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
  encryptionKey: "${ENCRYPTION_KEY}"
EOF
fi

cat >> "$OUTPUT_FILE" <<EOF
  
  client:
    enableLogging: true
    allowedOrigins:
      - "*"
    maxRequestBodySizeMb: 100
  
  # Add your provider keys here
  providers: {}
    # Example:
    # openai:
    #   keys:
    #     - value: "sk-..."
    #       weight: 1
    # anthropic:
    #   keys:
    #     - value: "sk-ant-..."
    #       weight: 1
  
  plugins:
    telemetry:
      enabled: true
      config: {}
    
    logging:
      enabled: true
      config: {}
EOF

if [[ "$VECTOR_ENABLED" == "true" ]]; then
    cat >> "$OUTPUT_FILE" <<EOF
    
    semanticCache:
      enabled: true
      config:
        provider: "openai"
        keys:
          - "sk-..."  # Add your OpenAI key for embeddings
        embeddingModel: "text-embedding-3-small"
        dimension: 1536
        threshold: 0.8
        ttl: "5m"
EOF
fi

print_success "Values file generated: $OUTPUT_FILE"
echo ""
print_warning "Please review and edit the generated file:"
print_warning "  - Add your provider API keys"
if [[ "$STORAGE_MODE" == "postgres" ]]; then
    print_warning "  - Change PostgreSQL password"
fi
if [[ "$VECTOR_TYPE" == "redis" ]] && [[ "$VECTOR_ENABLED" == "true" ]]; then
    print_warning "  - Change Redis password"
fi
if [[ -z "$ENCRYPTION_KEY" ]]; then
    print_warning "  - Add encryption key for production"
fi
echo ""
print_info "Install with: helm install bifrost ./bifrost -f $OUTPUT_FILE"


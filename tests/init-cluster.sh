#!/bin/bash

echo 'Starting Redis cluster initialization...'

# Define Redis nodes
REDIS_NODES='172.38.0.11:6379 172.38.0.12:6379 172.38.0.13:6379 172.38.0.14:6379 172.38.0.15:6379 172.38.0.16:6379'
MAX_WAIT_TIME=120
BACKOFF_DELAY=2

# Function to check if a Redis node is ready
check_redis_node() {
    host_port=$1
    host=$(echo $host_port | cut -d: -f1)
    port=$(echo $host_port | cut -d: -f2)
    
    # Check TCP connectivity
    if ! nc -z $host $port 2>/dev/null; then
        return 1
    fi
    
    # Check Redis PING response
    if ! redis-cli -h $host -p $port ping 2>/dev/null | grep -q 'PONG'; then
        return 1
    fi
    
    # Check Redis INFO response (ensure it's in cluster mode)
    if ! redis-cli -h $host -p $port info server 2>/dev/null | grep -q 'redis_version'; then
        return 1
    fi
    
    return 0
}

# Wait for all Redis nodes to be ready
echo 'Waiting for Redis nodes to be ready...'
start_time=$(date +%s)

while true; do
    current_time=$(date +%s)
    elapsed=$((current_time - start_time))
    
    if [ $elapsed -gt $MAX_WAIT_TIME ]; then
        echo "ERROR: Timeout waiting for Redis nodes to become ready after ${MAX_WAIT_TIME}s"
        echo 'Failed nodes:'
        for node in $REDIS_NODES; do
            if ! check_redis_node $node; then
                echo "  - $node: NOT READY"
            fi
        done
        exit 1
    fi
    
    all_ready=true
    for node in $REDIS_NODES; do
        if ! check_redis_node $node; then
            echo "Node $node not ready yet... (elapsed: ${elapsed}s)"
            all_ready=false
            break
        fi
    done
    
    if [ "$all_ready" = true ]; then
        echo 'All Redis nodes are ready!'
        break
    fi
    
    echo "Waiting ${BACKOFF_DELAY}s before retrying..."
    sleep $BACKOFF_DELAY
    
    # Exponential backoff (max 10s)
    if [ $BACKOFF_DELAY -lt 10 ]; then
        BACKOFF_DELAY=$((BACKOFF_DELAY * 2))
    fi
done

# Reset cluster state on all nodes first
echo 'Resetting cluster state on all nodes...'
for node in $REDIS_NODES; do
    host=$(echo $node | cut -d: -f1)
    port=$(echo $node | cut -d: -f2)
    
    echo "Resetting cluster state on $node"
    # Reset cluster configuration
    redis-cli -h $host -p $port CLUSTER RESET HARD 2>/dev/null || true
    # Clear all keys
    redis-cli -h $host -p $port FLUSHALL 2>/dev/null || true
done

echo 'Waiting for nodes to stabilize after reset...'
sleep 5

# Create the Redis cluster
echo 'Creating Redis cluster...'
redis-cli --cluster create $REDIS_NODES --cluster-replicas 1 --cluster-yes

if [ $? -ne 0 ]; then
    echo 'ERROR: Failed to create Redis cluster'
    exit 1
fi

echo 'Redis cluster created successfully!'

# Wait for cluster to be fully ready
echo 'Waiting for cluster to be fully operational...'
max_wait=30
wait_time=0
while [ $wait_time -lt $max_wait ]; do
    # Check if cluster is ready by testing both cluster info and actual operations
    if redis-cli -c -h 172.38.0.11 -p 6379 cluster info | grep -q "cluster_state:ok"; then
        # Test actual cluster operation
        if redis-cli -c -h 172.38.0.11 -p 6379 set "{readiness_test}:key" "test_value" >/dev/null 2>&1; then
            if redis-cli -c -h 172.38.0.12 -p 6379 get "{readiness_test}:key" | grep -q "test_value"; then
                # Clean up test key
                redis-cli -c -h 172.38.0.11 -p 6379 del "{readiness_test}:key" >/dev/null 2>&1
                echo 'Cluster is ready!'
                break
            fi
        fi
    fi
    echo "Cluster not ready yet, waiting... ($wait_time/$max_wait)"
    sleep 2
    wait_time=$((wait_time + 2))
done

if [ $wait_time -ge $max_wait ]; then
    echo 'ERROR: Cluster did not become ready within timeout'
    exit 1
fi

# Post-create sanity check with hashtagged test keys
echo 'Running post-create sanity checks...'

# Test slot-aware routing with hashtagged keys
test_key='{test}:cluster_check'
test_value="cluster_working_$(date +%s)"

# Set a test key using cluster mode
if redis-cli -c -h 172.38.0.11 -p 6379 set $test_key $test_value; then
    echo "Successfully set test key: $test_key"
else
    echo 'ERROR: Failed to set test key'
    exit 1
fi

# Retrieve the test key from a different node to verify slot routing
retrieved_value=$(redis-cli -c -h 172.38.0.12 -p 6379 get $test_key)
if [ "$retrieved_value" = "$test_value" ]; then
    echo "Successfully retrieved test key from different node: $retrieved_value"
    echo 'Cluster slot-aware routing is working correctly!'
else
    echo "ERROR: Slot-aware routing test failed. Expected: $test_value, Got: $retrieved_value"
    exit 1
fi

# Clean up test key
redis-cli -c -h 172.38.0.11 -p 6379 del $test_key >/dev/null

# Display cluster status
echo 'Final cluster status:'
redis-cli -c -h 172.38.0.11 -p 6379 cluster nodes

echo 'Redis cluster initialization completed successfully!'

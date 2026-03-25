#!/bin/sh
# Test: consumer-of reads producer stdout via pipe.
# Validates: log-type=pipe, consumer-of relationship, pipe I/O.

wait_for_service "pipe-producer" "STARTED" 10
wait_for_service "pipe-consumer" "STARTED" 10

# Give the producer time to generate output
sleep 4

# The consumer should have received the producer's output
output=$(slinitctl --system catlog pipe-consumer 2>&1)
assert_contains "$output" "consumed:" "consumer received pipe data"

assert_service_state "pipe-producer" "STARTED" "producer is running"
assert_service_state "pipe-consumer" "STARTED" "consumer is running"

test_summary

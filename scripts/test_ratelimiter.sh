#!/bin/bash

# A simple script to test the rate limiter API.
#
# Prerequisites:
# 1. The rate limiter server must be running in another terminal.
#    Run it with: go run ./cmd/ratelimiter-api/main.go
# 2. You must have `curl` and `jq` (for pretty-printing) installed.

# --- Configuration ---
HOST="http://localhost:8080"
API_KEY_ALICE="alice-key"
API_KEY_BOB="bob-key"
# The server is hardcoded to a limit of 1000.
# We will make 1001 requests to test the limit.
LIMIT=1000
REQUEST_COUNT_TO_FAIL=$((LIMIT + 1))

# --- Helper function ---
function check_key() {
  echo "--> Checking key: $1"
  # -s for silent mode, -i to include headers
  curl -s -i "$HOST/check?api_key=$1"
  echo
  echo "-------------------------------------"
}

# --- Main Test Logic ---
echo "Starting rate limiter test..."
echo "================================="
echo "First, ensure the server is running in another terminal:"
echo "  go run ./cmd/ratelimiter-api/main.go"
echo "================================="
echo
sleep 2

# 1. Test Alice's key a few times to show the counter decrementing
echo "Step 1: Making a few requests for Alice to show counter decrementing."
check_key $API_KEY_ALICE
check_key $API_KEY_ALICE
check_key $API_KEY_ALICE
echo

# 2. Test Bob's key to show it's tracked independently
echo "Step 2: Making a request for Bob to show his limit is separate."
check_key $API_KEY_BOB
echo

# 3. Test the rate limit exhaustion for Alice
echo "Step 3: Firing requests for Alice to trigger the rate limit."
echo "(This may take a moment...)"

# Use a loop to send many requests quickly.
# We only care about the HTTP status code.
# We start from 4 because we already made 3 requests for Alice.
for i in $(seq 4 $REQUEST_COUNT_TO_FAIL); do
  # The -s flag makes it silent, -o /dev/null discards body output.
  # The -w flag writes out only the http_code.
  status_code=$(curl -s -o /dev/null -w "%{http_code}" "$HOST/check?api_key=$API_KEY_ALICE")

  # Print progress without spamming the console
  if (( i % 100 == 0 )); then
    echo "  ... sent $i requests for Alice."
  fi

  # Check if we got the expected "Too Many Requests" status
  if [ "$status_code" -eq 429 ]; then
    echo
    echo "SUCCESS: Received status 429 Too Many Requests after $i requests."
    echo "Test complete."
    exit 0
  fi

  # Check if we've sent all requests and didn't get a 429
  if [ "$i" -eq "$REQUEST_COUNT_TO_FAIL" ]; then
    echo
    echo "FAILURE: Sent $REQUEST_COUNT_TO_FAIL requests but did not receive a 429 status code."
    exit 1
  fi
done

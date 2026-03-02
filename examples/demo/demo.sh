#!/bin/sh
set -e

# Interactive demo walkthrough for the 3-machine travel agency scenario.
# Run this from any machine after all 3 machines are set up.

SOCKET="${TAILBUS_SOCKET:-/tmp/tailbusd.sock}"

TB="tailbus -socket $SOCKET"

echo "=== Tailbus Travel Agency Demo ==="
echo ""

# Step 1: List all agents across the mesh
echo "--- Step 1: Discover all agents ---"
$TB list
echo ""

# Step 2: List by tag
echo "--- Step 2: Filter by tag 'booking' ---"
$TB list booking
echo ""

echo "--- Step 3: Filter by tag 'data' ---"
$TB list data
echo ""

# Step 3: Introspect specific agents
echo "--- Step 4: Introspect 'flights' agent ---"
$TB introspect flights
echo ""

echo "--- Step 5: Introspect 'concierge' agent ---"
$TB introspect concierge
echo ""

# Step 4: Open a session from concierge to flights
echo "--- Step 6: Open session concierge -> flights ---"
RESULT=$($TB open concierge flights "Search flights from NYC to London, Dec 20-27")
echo "$RESULT"
SESSION_ID=$(echo "$RESULT" | grep "Session:" | awk '{print $2}')
echo ""

# Step 5: Send a reply
echo "--- Step 7: Reply from flights ---"
$TB send "$SESSION_ID" flights "Found 3 flights: BA117 $450, VS3 $520, AA100 $380"
echo ""

# Step 6: Resolve the session
echo "--- Step 8: Resolve session ---"
$TB resolve "$SESSION_ID" concierge "Booked AA100 for \$380"
echo ""

# Step 7: Check sessions
echo "--- Step 9: List sessions for concierge ---"
$TB sessions concierge
echo ""

echo "=== Demo complete ==="
